package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

var (
	ErrGhostArchiveInvalid  = errors.New("invalid ghost archive request")
	ErrGhostArchiveConflict = errors.New("ghost description changed; review it again")
)

const MaxGhostArchivePaths = 64

type GhostArchiveTarget struct {
	Path          string `json:"path"`
	ExpectedToken string `json:"expected_token"`
}

type GhostArchiveInput struct {
	Project        string               `json:"project"`
	Targets        []GhostArchiveTarget `json:"targets"`
	Reason         string               `json:"reason"`
	ConfirmDeleted bool                 `json:"confirm_deleted"`
	Person         string               `json:"-"`
}

type GhostArchiveResult struct {
	Archived        []string `json:"archived"`
	AlreadyArchived []string `json:"already_archived"`
}

func ghostArchiveToken(g GhostFile, revision int64) string {
	raw, _ := json.Marshal(struct {
		File     GhostFile
		Revision int64
	}{g, revision})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

type GhostArchiveCandidate struct {
	File   GhostFile          `json:"file"`
	Target GhostArchiveTarget `json:"target"`
}

func (s *Store) PrepareGhostArchive(project, path string) (GhostArchiveCandidate, error) {
	if strings.TrimSpace(project) == "" {
		return GhostArchiveCandidate{}, ErrGhostArchiveInvalid
	}
	if err := ValidateGhostArchivePath(path); err != nil {
		return GhostArchiveCandidate{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return GhostArchiveCandidate{}, err
	}
	defer tx.Rollback()
	candidate, err := ghostArchiveCandidateTx(tx, project, path)
	if err != nil {
		return GhostArchiveCandidate{}, err
	}
	if err := tx.Commit(); err != nil {
		return GhostArchiveCandidate{}, err
	}
	return candidate, nil
}

func ghostArchiveCandidateTx(tx *sql.Tx, project, path string) (GhostArchiveCandidate, error) {
	rows, err := tx.Query(`SELECT `+ghostCols+` FROM ghost_files WHERE project=? AND path=?`, project, path)
	if err != nil {
		return GhostArchiveCandidate{}, err
	}
	gs, err := scanGhostFiles(rows)
	if err != nil {
		return GhostArchiveCandidate{}, err
	}
	if len(gs) == 0 {
		return GhostArchiveCandidate{}, sql.ErrNoRows
	}
	var revision int64
	if err := tx.QueryRow(`SELECT revision FROM ghost_path_revisions WHERE project=? AND path=?`, project, path).Scan(&revision); err != nil {
		return GhostArchiveCandidate{}, fmt.Errorf("ghost path revision unavailable: %w", err)
	}
	return GhostArchiveCandidate{File: gs[0], Target: GhostArchiveTarget{Path: path, ExpectedToken: ghostArchiveToken(gs[0], revision)}}, nil
}

func ValidateGhostArchivePath(p string) error {
	if p == "" || p == "." || path.IsAbs(p) || path.Clean(p) != p ||
		p == ".." || strings.HasPrefix(p, "../") || strings.ContainsAny(p, "\\\x00") ||
		p == ".ghosttree" || strings.HasPrefix(p, ".ghosttree/") {
		return fmt.Errorf("%w: select exact non-root repository paths: %q", ErrGhostArchiveInvalid, p)
	}
	return nil
}

func (s *Store) ArchiveGhostFiles(in GhostArchiveInput) (GhostArchiveResult, error) {
	out := GhostArchiveResult{Archived: []string{}, AlreadyArchived: []string{}}
	if strings.TrimSpace(in.Project) == "" || strings.TrimSpace(in.Person) == "" ||
		!in.ConfirmDeleted || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 1024 ||
		len(in.Targets) == 0 || len(in.Targets) > MaxGhostArchivePaths {
		return out, fmt.Errorf("%w: project, actor, reason, explicit deletion confirmation and 1..%d paths required", ErrGhostArchiveInvalid, MaxGhostArchivePaths)
	}
	seen := map[string]bool{}
	for _, target := range in.Targets {
		if err := ValidateGhostArchivePath(target.Path); err != nil {
			return out, err
		}
		if seen[target.Path] {
			return out, fmt.Errorf("%w: duplicate path %q", ErrGhostArchiveInvalid, target.Path)
		}
		seen[target.Path] = true
		if token, err := hex.DecodeString(target.ExpectedToken); err != nil || len(token) != sha256.Size {
			return out, fmt.Errorf("%w: expected token required for %q", ErrGhostArchiveInvalid, target.Path)
		}
	}
	if s.writer != nil {
		return queueValue(s, []any{in}, func(d *Store, p []any) (GhostArchiveResult, error) {
			return d.ArchiveGhostFiles(p[0].(GhostArchiveInput))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE ghost_files SET id=id WHERE 0`); err != nil {
		return GhostArchiveResult{}, err
	}
	for _, target := range in.Targets {
		candidate, err := ghostArchiveCandidateTx(tx, in.Project, target.Path)
		if err == sql.ErrNoRows {
			var matches int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM ghost_archive_receipts WHERE project=? AND path=? AND token=?`, in.Project, target.Path, target.ExpectedToken).Scan(&matches); err != nil {
				return GhostArchiveResult{}, err
			}
			if matches == 0 {
				return GhostArchiveResult{}, fmt.Errorf("%w: %q has no matching archive receipt", ErrGhostArchiveConflict, target.Path)
			}
			out.AlreadyArchived = append(out.AlreadyArchived, target.Path)
			continue
		}
		if err != nil {
			return GhostArchiveResult{}, err
		}
		if candidate.Target.ExpectedToken != target.ExpectedToken {
			return GhostArchiveResult{}, fmt.Errorf("%w: %q", ErrGhostArchiveConflict, target.Path)
		}
		ts := now()
		reason := "archiviert: " + in.Reason + "; bestätigt von " + in.Person
		if err := archiveGhostFileTx(tx, in.Project, target.Path, ts, reason); err != nil {
			return GhostArchiveResult{}, err
		}
		if _, err := tx.Exec(`DELETE FROM ghost_files WHERE project=? AND path=?`, in.Project, target.Path); err != nil {
			return GhostArchiveResult{}, err
		}
		if _, err := tx.Exec(`INSERT INTO ghost_archive_receipts(project,path,token,person,reason,at) VALUES(?,?,?,?,?,?)`, in.Project, target.Path, target.ExpectedToken, in.Person, in.Reason, ts); err != nil {
			return GhostArchiveResult{}, err
		}
		out.Archived = append(out.Archived, target.Path)
	}
	if err := tx.Commit(); err != nil {
		return GhostArchiveResult{}, err
	}
	return out, nil
}
