package store

import (
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
)

// pullCommitSchema: die Commits eines PRs, wie GitHub sie über
// GET /repos/{owner}/{repo}/pulls/{n}/commits meldet (Recht "Pull requests:
// read", kein Recht auf den Code). Eine Zeile je (Projekt, PR, Commit). in_pr
// ist 1 für die Commits, die der PR beim letzten Abgleich hatte; ein Force-Push
// setzt den alten Commit auf 0, die Zeile bleibt (ein Eintrag im Ledger kann an
// ihm hängen). Titel und Namen sind Fremddaten und stehen nur bereinigt hier.
const pullCommitSchema = `
CREATE TABLE IF NOT EXISTS git_pr_commits(
  project TEXT NOT NULL,
  pr INTEGER NOT NULL CHECK(pr > 0),
  sha TEXT NOT NULL CHECK(length(sha) >= 7),
  author TEXT NOT NULL DEFAULT '',
  author_name TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  committed_at TEXT NOT NULL DEFAULT '',
  position INTEGER NOT NULL DEFAULT 0,
  in_pr INTEGER NOT NULL DEFAULT 1 CHECK(in_pr IN (0,1)),
  recorded_at TEXT NOT NULL,
  PRIMARY KEY(project, pr, sha)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS git_pr_commits_sha ON git_pr_commits(project, sha);
`

// MaxPullCommits ist, wie viele Commits je PR gespeichert werden. GitHub liefert
// selbst höchstens 250.
const MaxPullCommits = 250

var loginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-]{0,38}(\[bot\])?$`)

// cleanLogin lässt nur gültige GitHub-Logins durch (auch Bots: name[bot]); alles
// andere wird leer, nicht "bereinigt": ein halber Login wäre ein anderer Mensch.
func cleanLogin(s string) string {
	s = strings.TrimSpace(s)
	if !loginPattern.MatchString(s) {
		return ""
	}
	return s
}

// PullCommit ist ein Commit eines PRs in der Form, die der Store braucht.
type PullCommit struct {
	SHA string
	// Author ist der GitHub-Login (leer, wenn GitHub den Autor keinem Konto
	// zuordnet), AuthorName der Name aus dem Commit.
	Author, AuthorName string
	// Title ist die erste Zeile der Commit-Nachricht.
	Title       string
	CommittedAt string
}

// PullCommitsChange ersetzt die Commits eines PRs durch den Stand von GitHub.
type PullCommitsChange struct {
	InstallationID int64
	// Remote ist das Repo des PRs (Base).
	Remote  string
	Number  int64
	Commits []PullCommit
}

// PullCommitsResult sagt, was der Abgleich bewirkt hat.
type PullCommitsResult struct {
	// Applied: der PR ist bekannt und gehört zu einem Projekt dieser Installation.
	// Unbekannt, fremd und ungültig sehen gleich aus (false).
	Applied  bool
	Recorded int
	// Dropped sind Commits, die der PR nicht mehr trägt (Force-Push).
	Dropped int
}

func validCommitSHA(s string) bool {
	return (len(s) == 40 || len(s) == 64) && gitanchor.ValidPrefix(s)
}

// RecordPullCommits speichert die Commits eines PRs (Webhook pull_request und
// ctx github sync-prs). Der PR muss schon über RecordPullRequest bekannt sein; so
// bleiben die Prüfungen der Installation an einer Stelle. Ungültige Einträge
// werden übersprungen, nicht der ganze Abgleich verworfen.
func (s *Store) RecordPullCommits(c PullCommitsChange) (PullCommitsResult, error) {
	if s.writer != nil {
		return queueValue(s, []any{c}, func(d *Store, p []any) (PullCommitsResult, error) {
			return d.RecordPullCommits(p[0].(PullCommitsChange))
		})
	}
	remote, err := normalizeRemote(c.Remote)
	if err != nil || c.Number <= 0 || c.InstallationID <= 0 {
		return PullCommitsResult{}, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return PullCommitsResult{}, err
	}
	defer tx.Rollback()
	var srcOrg int64
	var kind, login string
	if err := tx.QueryRow(`SELECT org_id, kind, login FROM git_sources WHERE installation_id=?`, c.InstallationID).Scan(&srcOrg, &kind, &login); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PullCommitsResult{}, nil
		}
		return PullCommitsResult{}, err
	}
	host, _ := sourceHost(kind)
	if !strings.HasPrefix(remote, host+"/"+login+"/") {
		return PullCommitsResult{}, nil
	}
	proj, known := projectTx(tx, remote)
	if !known || proj.OrgID != srcOrg {
		return PullCommitsResult{}, nil
	}
	var one int
	if err := tx.QueryRow(`SELECT 1 FROM git_pull_requests WHERE project=? AND number=?`, remote, c.Number).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PullCommitsResult{}, nil
		}
		return PullCommitsResult{}, err
	}
	res := PullCommitsResult{Applied: true}
	ts := now()
	if _, err := tx.Exec(`UPDATE git_pr_commits SET in_pr=0 WHERE project=? AND pr=? AND in_pr=1`, remote, c.Number); err != nil {
		return res, err
	}
	list := c.Commits
	if len(list) > MaxPullCommits {
		list = list[:MaxPullCommits]
	}
	for i, k := range list {
		sha := gitanchor.Normalize(k.SHA)
		if !validCommitSHA(sha) {
			continue
		}
		committed := ""
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(k.CommittedAt)); err == nil {
			committed = t.UTC().Format(time.RFC3339)
		}
		title := cleanLine(firstLine(k.Title), 200)
		if _, err := tx.Exec(`INSERT INTO git_pr_commits(project,pr,sha,author,author_name,title,committed_at,position,in_pr,recorded_at)
			VALUES(?,?,?,?,?,?,?,?,1,?)
			ON CONFLICT(project,pr,sha) DO UPDATE SET author=excluded.author, author_name=excluded.author_name, title=excluded.title,
				committed_at=excluded.committed_at, position=excluded.position, in_pr=1`,
			remote, c.Number, sha, cleanLogin(k.Author), cleanLine(k.AuthorName, 100), title, committed, i, ts); err != nil {
			return res, err
		}
		res.Recorded++
	}
	// Was wieder im PR ist, zählt nicht als abgefallen.
	var stillOut int64
	if err := tx.QueryRow(`SELECT COUNT(*) FROM git_pr_commits WHERE project=? AND pr=? AND in_pr=0`, remote, c.Number).Scan(&stillOut); err != nil {
		return res, err
	}
	res.Dropped = int(stillOut)
	return res, tx.Commit()
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// PullsWithoutCommits nennt die PRs eines Projekts, zu denen noch kein Commit
// gespeichert ist: was ctx github sync-prs --commits nachholt (PRs, die vor dem
// Speichern der Commits entstanden sind oder deren Abruf scheiterte).
func (s *Store) PullsWithoutCommits(project string) ([]int64, error) {
	if s.reader != nil {
		return s.reader.PullsWithoutCommits(project)
	}
	rows, err := s.db.Query(`SELECT p.number FROM git_pull_requests p
		WHERE p.project=? AND NOT EXISTS (SELECT 1 FROM git_pr_commits c WHERE c.project=p.project AND c.pr=p.number)
		ORDER BY p.number`, scopeNormalize(project))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
