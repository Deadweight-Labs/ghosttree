package store

import (
	"database/sql"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
)

// ghostBranchSchema (REQ-597, Kriterium 8): die Branch-Achse der Ghost-Dateien.
//
// ghost_files bleibt die Projektebene (Branch ""): UNIQUE(project, path), von
// vielen Lesern und Upserts so erwartet. Eine Beschreibung auf einem Feature-
// Branch liegt daneben in ghost_branch_files, eine Zeile je (Projekt, Branch,
// Pfad). Das ist additiv; bestehende Beschreibungen ändern sich nicht.
//
// Die Beschreibung auf dem Branch ist ein Entwurf bis zum Merge: es gibt je
// Branch und Pfad nur die aktuelle Fassung (keine Historie wie in
// ghost_file_versions), und sie wird beim Merge des PRs als neue Fassung mit
// Anker auf den Base-Branch gehoben (promoteGhostBranchTx).
const ghostBranchSchema = `
CREATE TABLE IF NOT EXISTS ghost_branch_files(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL,
  branch TEXT NOT NULL CHECK(branch != ''),
  path TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'file',
  description TEXT NOT NULL,
  content_sha TEXT NOT NULL DEFAULT '',
  git_blob TEXT NOT NULL DEFAULT '',
  line_count INTEGER NOT NULL DEFAULT 0,
  person TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL DEFAULT '',
  session_ref TEXT NOT NULL DEFAULT '',
  described_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  anchor_id INTEGER REFERENCES git_anchors(id),
  UNIQUE(project, branch, path));
CREATE INDEX IF NOT EXISTS ghost_branch_files_branch ON ghost_branch_files(project, branch);
CREATE INDEX IF NOT EXISTS ghost_branch_files_anchor ON ghost_branch_files(anchor_id) WHERE anchor_id IS NOT NULL;
`

// ensureGhostBranches legt die Tabelle additiv an; läuft nach ensureGitAnchors.
func ensureGhostBranches(db *sql.DB) error {
	_, err := db.Exec(ghostBranchSchema)
	return err
}

const ghostBranchCols = `id, project, path, kind, description, content_sha, git_blob,
	line_count, person, harness, session_ref, described_at, updated_at, branch`

func scanGhostBranchRows(rows *sql.Rows) ([]GhostFile, error) {
	defer rows.Close()
	out := []GhostFile{}
	for rows.Next() {
		var g GhostFile
		if err := rows.Scan(&g.ID, &g.Project, &g.Path, &g.Kind, &g.Description,
			&g.ContentSHA, &g.GitBlob, &g.LineCount, &g.Person, &g.Harness,
			&g.SessionRef, &g.DescribedAt, &g.UpdatedAt, &g.Branch); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// maxRefLen begrenzt Branch-Namen aus Fremddaten.
const maxRefLen = 250

// cleanRef prüft einen Branch-Namen, der von außen kommt (Webhook, Anfrage):
// einzeilig, ohne Steuer- und Formatzeichen (Zeilenumbrüche, Bidi-Umschalter,
// unsichtbare Zeichen) und in Maßen lang. Was das nicht erfüllt, wird nicht
// bereinigt, sondern abgelehnt: ein "bereinigter" Name wäre ein anderer
// Branch, als GitHub meldet.
func cleanRef(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxRefLen || !utf8.ValidString(s) {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) || r == utf8.RuneError {
			return "", false
		}
	}
	return s, true
}

// ValidBranchName sagt, ob ein Branch-Name von außen angenommen wird (einzeilig,
// ohne Steuer- und Formatzeichen, in Maßen lang).
func ValidBranchName(s string) bool { _, ok := cleanRef(s); return ok }

// cleanLine macht aus Fremdtext (PR-Titel) eine kurze, einzeilige Zeile ohne
// Steuer- und Formatzeichen.
func cleanLine(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == utf8.RuneError:
		default:
			b.WriteRune(r)
		}
	}
	line := strings.Join(strings.Fields(b.String()), " ")
	if r := []rune(line); len(r) > max {
		line = string(r[:max])
	}
	return line
}

func (s *Store) putGhostBranchFile(g GhostFile, ts string) (int64, error) {
	branch, ok := cleanRef(g.Branch)
	if !ok {
		return 0, fmt.Errorf("%w: branch name", ErrInvalidInput)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	anchorID, err := insertAnchorTx(tx, g.GitAnchor)
	if err != nil {
		return 0, err
	}
	if err := upsertGhostBranchTx(tx, g, branch, ts, anchorID); err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRow(`SELECT id FROM ghost_branch_files WHERE project=? AND branch=? AND path=?`, g.Project, branch, g.Path).Scan(&id); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func upsertGhostBranchTx(tx *sql.Tx, g GhostFile, branch, ts string, anchorID any) error {
	_, err := tx.Exec(`INSERT INTO ghost_branch_files(project,branch,path,kind,description,content_sha,git_blob,
		line_count,person,harness,session_ref,described_at,updated_at,anchor_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(project,branch,path) DO UPDATE SET
			kind=excluded.kind, description=excluded.description,
			content_sha=excluded.content_sha, git_blob=excluded.git_blob,
			line_count=excluded.line_count, person=excluded.person,
			harness=excluded.harness, session_ref=excluded.session_ref,
			described_at=excluded.described_at, updated_at=excluded.updated_at,
			anchor_id=excluded.anchor_id`,
		g.Project, branch, g.Path, g.Kind, g.Description, g.ContentSHA, g.GitBlob,
		g.LineCount, g.Person, g.Harness, g.SessionRef, g.DescribedAt, ts, anchorID)
	return err
}

// branchLayers sind die Branches, deren Beschreibungen für eine Lesung auf
// branch gelten: er selbst und seine Vorfahren.
func branchLayers(branch string, lineage []string) []string {
	if strings.TrimSpace(branch) == "" {
		return nil
	}
	out := []string{branch}
	seen := map[string]bool{branch: true}
	for _, l := range lineage {
		if l = strings.TrimSpace(l); l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// pickLayer wählt je Pfad die gültige Beschreibung: die des Branches selbst,
// sonst unter den Vorfahren die zuletzt geschriebene, sonst die der
// Projektebene. Eine Beschreibung eines Vorfahren-Branches verdrängt die der
// Projektebene, weil sie näher am gelesenen Stand liegt (#713).
func pickLayer(branch string, base GhostFile, hasBase bool, overlays []GhostFile) (GhostFile, bool) {
	var best GhostFile
	found := false
	for _, o := range overlays {
		switch {
		case !found:
			best, found = o, true
		case o.Branch == branch && best.Branch != branch:
			best = o
		case best.Branch == branch:
		case o.UpdatedAt > best.UpdatedAt:
			best = o
		}
	}
	if found {
		return best, true
	}
	return base, hasBase
}

// ghostAtTx liefert die auf dem Branch gültige Beschreibung eines Pfades (null
// oder eine). Ohne Branch ist das die Projektebene.
func ghostAtTx(q rowsQuerier, project, path, branch string, lineage []string) ([]GhostFile, error) {
	rows, err := q.Query(`SELECT `+ghostCols+` FROM ghost_files WHERE project=? AND path=?`, project, path)
	if err != nil {
		return nil, err
	}
	base, err := scanGhostFilesRaw(rows)
	if err != nil {
		return nil, err
	}
	layers := branchLayers(branch, lineage)
	if len(layers) == 0 {
		return base, nil
	}
	args := []any{project, path}
	for _, l := range layers {
		args = append(args, l)
	}
	rows, err = q.Query(`SELECT `+ghostBranchCols+` FROM ghost_branch_files WHERE project=? AND path=? AND branch IN (`+placeholders(len(layers))+`)`, args...)
	if err != nil {
		return nil, err
	}
	overlays, err := scanGhostBranchRows(rows)
	if err != nil {
		return nil, err
	}
	var b GhostFile
	if len(base) > 0 {
		b = base[0]
	}
	if g, ok := pickLayer(branch, b, len(base) > 0, overlays); ok {
		return []GhostFile{g}, nil
	}
	return nil, nil
}

// GhostFileAt ist GhostFileByPath auf einem Branch: die Beschreibung des
// Branches, sonst eines Vorfahren, sonst der Projektebene.
func (s *Store) GhostFileAt(project, path, branch string, lineage []string) (GhostFile, error) {
	if s.reader != nil {
		return s.reader.GhostFileAt(project, path, branch, lineage)
	}
	gs, err := ghostAtTx(s.db, project, path, branch, lineage)
	if err != nil {
		return GhostFile{}, err
	}
	if len(gs) == 0 {
		return GhostFile{}, sql.ErrNoRows
	}
	if gs[0].Branch != "" {
		if err := s.attachBranchGhostAnchors(gs); err != nil {
			return GhostFile{}, err
		}
		return gs[0], nil
	}
	return s.GhostFileByPath(project, path)
}

func (s *Store) attachBranchGhostAnchors(gs []GhostFile) error {
	var ids []int64
	for _, g := range gs {
		if g.Branch != "" {
			ids = append(ids, g.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	anchors, err := s.anchorsFor("ghost_branch_files", ids)
	if err != nil {
		return err
	}
	for i := range gs {
		if gs[i].Branch != "" {
			gs[i].GitAnchor = anchors[gs[i].ID]
		}
	}
	return nil
}

// GhostFilesUnderAt ist GhostFilesUnder auf einem Branch: der Baum folgt Branch
// und Abstammung wie das Wissen. Ohne Branch ist es die Projektebene.
func (s *Store) GhostFilesUnderAt(project, prefix, branch string, lineage []string) ([]GhostFile, error) {
	if s.reader != nil {
		return s.reader.GhostFilesUnderAt(project, prefix, branch, lineage)
	}
	base, err := s.GhostFilesUnder(project, prefix)
	layers := branchLayers(branch, lineage)
	if err != nil || len(layers) == 0 {
		return base, err
	}
	query := `SELECT ` + ghostBranchCols + ` FROM ghost_branch_files WHERE project=? AND branch IN (` + placeholders(len(layers)) + `)`
	args := []any{project}
	for _, l := range layers {
		args = append(args, l)
	}
	if prefix != "" {
		query += ` AND (path = ? OR path LIKE ?)`
		args = append(args, prefix, prefix+"/%")
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	overlays, err := scanGhostBranchRows(rows)
	if err != nil {
		return nil, err
	}
	if len(overlays) == 0 {
		return base, nil
	}
	byPath := map[string][]GhostFile{}
	for _, o := range overlays {
		byPath[o.Path] = append(byPath[o.Path], o)
	}
	out := make([]GhostFile, 0, len(base)+len(byPath))
	seen := map[string]bool{}
	for _, b := range base {
		seen[b.Path] = true
		if ovs := byPath[b.Path]; len(ovs) > 0 {
			g, _ := pickLayer(branch, b, true, ovs)
			out = append(out, g)
			continue
		}
		out = append(out, b)
	}
	for path, ovs := range byPath {
		if !seen[path] {
			g, _ := pickLayer(branch, GhostFile{}, false, ovs)
			out = append(out, g)
		}
	}
	sortGhosts(out)
	if err := s.attachBranchGhostAnchors(out); err != nil {
		return nil, err
	}
	return out, nil
}

func sortGhosts(gs []GhostFile) {
	for i := 1; i < len(gs); i++ {
		for j := i; j > 0 && gs[j].Path < gs[j-1].Path; j-- {
			gs[j], gs[j-1] = gs[j-1], gs[j]
		}
	}
}

// promoteGhostBranchTx hebt beim Merge eines PRs die Beschreibungen des
// Head-Branches auf die Ebene des Base-Branches. Das Ziel ist die Projektebene,
// wenn base der Default-Branch des Repos ist (vom Webhook gemeldet); ist der
// Default unbekannt, gilt base als Projektebene, solange er weder eigene
// Branch-Beschreibungen trägt noch Head eines offenen PRs ist. Sonst ist das
// Ziel die Branch-Ebene von base.
//
// Jede Beschreibung wird als neue Fassung mit eigenem Anker geschrieben (Commit
// des Merges, Branch base, die PR-Nummer). Hat das Ziel für den Pfad eine
// jüngere Beschreibung als der Head, bleibt sie: sie ist nach dem Branch
// entstanden und kennt den Merge-Stand besser. Die Beschreibungen des Heads
// entfallen danach in jedem Fall. Liefert (übernommen, behalten).
func promoteGhostBranchTx(tx *sql.Tx, project, head, base, defaultBranch string, pr int64, mergeSHA, ts string) (promoted, kept int, err error) {
	rows, err := tx.Query(`SELECT `+ghostBranchCols+`, COALESCE(anchor_id,0) FROM ghost_branch_files WHERE project=? AND branch=? ORDER BY path`, project, head)
	if err != nil {
		return 0, 0, err
	}
	type item struct {
		g      GhostFile
		anchor int64
	}
	var items []item
	for rows.Next() {
		var it item
		g := &it.g
		if err := rows.Scan(&g.ID, &g.Project, &g.Path, &g.Kind, &g.Description, &g.ContentSHA, &g.GitBlob,
			&g.LineCount, &g.Person, &g.Harness, &g.SessionRef, &g.DescribedAt, &g.UpdatedAt, &g.Branch, &it.anchor); err != nil {
			rows.Close()
			return 0, 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(items) == 0 {
		return 0, 0, err
	}
	target := base
	if defaultBranch != "" {
		if base == defaultBranch {
			target = ""
		}
	} else {
		var n int
		if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM ghost_branch_files WHERE project=? AND branch=?)
			+ (SELECT COUNT(*) FROM git_pull_requests WHERE project=? AND head_ref=? AND state='open')`, project, base, project, base).Scan(&n); err != nil {
			return 0, 0, err
		}
		if n == 0 {
			target = ""
		}
	}
	for _, it := range items {
		g := it.g
		var existing string
		var err error
		if target == "" {
			err = tx.QueryRow(`SELECT described_at FROM ghost_files WHERE project=? AND path=?`, project, g.Path).Scan(&existing)
		} else {
			err = tx.QueryRow(`SELECT described_at FROM ghost_branch_files WHERE project=? AND branch=? AND path=?`, project, target, g.Path).Scan(&existing)
		}
		if err != nil && err != sql.ErrNoRows {
			return 0, 0, err
		}
		if err == nil && existing > g.DescribedAt {
			kept++
		} else {
			commit := mergeSHA
			if commit == "" && it.anchor > 0 {
				_ = tx.QueryRow(`SELECT commit_sha FROM git_anchors WHERE id=?`, it.anchor).Scan(&commit)
			}
			var anchorID any
			if commit != "" {
				p := pr
				a := &gitanchor.Anchor{Commit: commit, Branch: base, PR: &p}
				if anchorID, err = insertAnchorWithPRTx(tx, a); err != nil {
					return 0, 0, err
				}
			}
			g.Branch = ""
			if target == "" {
				if _, err := putProjectGhostTx(tx, g, ts, "ersetzt", anchorID, nil); err != nil {
					return 0, 0, err
				}
			} else if err := upsertGhostBranchTx(tx, g, target, ts, anchorID); err != nil {
				return 0, 0, err
			}
			promoted++
		}
		if _, err := tx.Exec(`DELETE FROM ghost_branch_files WHERE id=?`, it.g.ID); err != nil {
			return 0, 0, err
		}
	}
	return promoted, kept, nil
}
