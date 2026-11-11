package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
)

// commitMetaSchema: was GitHub zu einem Commit eines PRs sonst noch sagt (REQ-599,
// P13.5): Eltern, die ganze Nachricht, Mit-Autoren, geänderte Dateien mit Zeilen
// und der Stand der Prüfungen. Eine Zeile je (Projekt, Commit). Eltern, Nachricht
// und Mit-Autoren kommen mit der Liste der PR-Commits (RecordPullCommits); Dateien
// und Prüfungen holt ein eigener Abruf je Commit (RecordCommitDetails), weil sie
// mehr Rechte der App brauchen. files_at und ci_at sind leer, solange der Abruf
// nicht gelungen ist; so zeigt die Seite einen fehlenden Block gar nicht erst.
// Alles ist Fremddaten und steht nur bereinigt hier; E-Mail-Adressen der
// Mit-Autoren werden nie gespeichert.
const commitMetaSchema = `
CREATE TABLE IF NOT EXISTS git_commit_meta(
  project TEXT NOT NULL,
  sha TEXT NOT NULL CHECK(length(sha) >= 7),
  parents TEXT NOT NULL DEFAULT '[]',
  message TEXT NOT NULL DEFAULT '',
  co_authors TEXT NOT NULL DEFAULT '[]',
  files TEXT NOT NULL DEFAULT '[]',
  files_total INTEGER NOT NULL DEFAULT 0,
  additions INTEGER NOT NULL DEFAULT 0,
  deletions INTEGER NOT NULL DEFAULT 0,
  files_at TEXT NOT NULL DEFAULT '',
  ci TEXT NOT NULL DEFAULT '' CHECK(ci IN ('','success','failure','pending')),
  ci_at TEXT NOT NULL DEFAULT '',
  recorded_at TEXT NOT NULL,
  PRIMARY KEY(project, sha)) WITHOUT ROWID;
`

// Grenzen für Fremddaten.
const (
	maxCommitBodyRunes = 6000
	maxCommitParents   = 8
	maxCoAuthors       = 10
	maxCommitFileRows  = 300
	maxCommitFilePath  = 400
)

// Stände der Prüfungen eines Commits.
const (
	CommitCIPending = "pending"
	CommitCISuccess = "success"
	CommitCIFailure = "failure"
)

// CommitFileStat ist eine geänderte Datei eines Commits.
type CommitFileStat struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// CommitMeta ist, was zu einem Commit gespeichert ist.
type CommitMeta struct {
	SHA       string
	Parents   []string
	Message   string
	CoAuthors []string
	// Files sind die geänderten Dateien; FilesKnown sagt, ob der Abruf gelungen ist
	// (eine leere Liste ist dann ein Commit ohne Dateien, sonst unbekannt).
	Files      []CommitFileStat
	FilesTotal int
	Additions  int
	Deletions  int
	FilesKnown bool
	// CI ist der Stand der Prüfungen, leer wenn keiner bekannt ist.
	CI string
}

// splitCommitMessage zerlegt die Nachricht eines Commits in den Textkörper nach der
// Titelzeile und die Namen der Mit-Autoren aus den "Co-authored-by"-Zeilen. Die
// Adressen in den spitzen Klammern werden verworfen.
func splitCommitMessage(msg string) (body string, coAuthors []string) {
	msg = strings.ReplaceAll(strings.ReplaceAll(msg, "\r\n", "\n"), "\r", "\n")
	msg = strings.TrimSpace(msg)
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[i+1:]
	} else {
		return "", nil
	}
	var keep []string
	for _, line := range strings.Split(msg, "\n") {
		trim := strings.TrimSpace(line)
		if len(trim) > 15 && strings.EqualFold(trim[:15], "co-authored-by:") {
			name := strings.TrimSpace(trim[15:])
			if i := strings.IndexByte(name, '<'); i >= 0 {
				name = name[:i]
			}
			if name = cleanLine(name, 100); name != "" && len(coAuthors) < maxCoAuthors {
				coAuthors = append(coAuthors, name)
			}
			continue
		}
		keep = append(keep, line)
	}
	return cleanBody(strings.Join(keep, "\n"), maxCommitBodyRunes), coAuthors
}

// cleanBody behält Zeilenumbrüche und Absätze, wirft aber Steuerzeichen weg und
// kappt die Länge.
func cleanBody(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n', r == '\t':
			b.WriteRune(r)
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == utf8.RuneError:
		default:
			b.WriteRune(r)
		}
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	out := strings.Join(lines, "\n")
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	if r := []rune(out); len(r) > max {
		out = strings.TrimSpace(string(r[:max]))
	}
	return out
}

func validCICode(s string) bool {
	return s == "" || s == CommitCISuccess || s == CommitCIFailure || s == CommitCIPending
}

func jsonStrings(v []string) string {
	if len(v) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// recordCommitMetaTx schreibt Eltern, Nachricht und Mit-Autoren eines PR-Commits;
// Dateien und Prüfungen bleiben, wie sie sind.
func recordCommitMetaTx(tx *sql.Tx, project, sha string, parents []string, message, ts string) error {
	var ps []string
	for _, p := range parents {
		if p = gitanchor.Normalize(p); validCommitSHA(p) && len(ps) < maxCommitParents {
			ps = append(ps, p)
		}
	}
	body, co := splitCommitMessage(message)
	_, err := tx.Exec(`INSERT INTO git_commit_meta(project,sha,parents,message,co_authors,recorded_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(project,sha) DO UPDATE SET parents=excluded.parents, message=excluded.message, co_authors=excluded.co_authors`,
		project, sha, jsonStrings(ps), body, jsonStrings(co), ts)
	return err
}

// CommitDetailsChange sind die Dateien und der Prüfstand eines Commits, wie der
// Abruf bei GitHub sie liefert. Eine Gruppe, die nicht geholt wurde (HaveFiles oder
// HaveCI aus), bleibt in der Zeile, wie sie war.
type CommitDetailsChange struct {
	InstallationID int64
	// Remote ist das Repo des PRs (Base).
	Remote string
	SHA    string

	HaveFiles  bool
	Files      []CommitFileStat
	FilesTotal int
	Additions  int
	Deletions  int

	HaveCI bool
	CI     string
}

// RecordCommitDetails speichert Dateien und Prüfstand eines Commits, der zu einem
// PR des Projekts gehört. Wie RecordPullCommits prüft es die Installation; ein
// Commit, den kein PR des Projekts trägt, ein fremdes Projekt und ein ungültiger
// SHA sehen gleich aus (false). Der Prüfstand wird nie zu "unbekannt" zurückgesetzt:
// ein leerer Stand heißt "keine Prüfung gelaufen".
func (s *Store) RecordCommitDetails(c CommitDetailsChange) (bool, error) {
	if s.writer != nil {
		return queueValue(s, []any{c}, func(d *Store, p []any) (bool, error) {
			return d.RecordCommitDetails(p[0].(CommitDetailsChange))
		})
	}
	remote, err := normalizeRemote(c.Remote)
	sha := gitanchor.Normalize(c.SHA)
	if err != nil || c.InstallationID <= 0 || !validCommitSHA(sha) || !validCICode(c.CI) || (!c.HaveFiles && !c.HaveCI) {
		return false, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var srcOrg int64
	var kind, login string
	if err := tx.QueryRow(`SELECT org_id, kind, login FROM git_sources WHERE installation_id=?`, c.InstallationID).Scan(&srcOrg, &kind, &login); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	host, _ := sourceHost(kind)
	if !strings.HasPrefix(remote, host+"/"+login+"/") {
		return false, nil
	}
	proj, known := projectTx(tx, remote)
	if !known || proj.OrgID != srcOrg {
		return false, nil
	}
	var one int
	if err := tx.QueryRow(`SELECT 1 FROM git_pr_commits WHERE project=? AND sha=? LIMIT 1`, remote, sha).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	ts := now()
	if _, err := tx.Exec(`INSERT INTO git_commit_meta(project,sha,recorded_at) VALUES(?,?,?) ON CONFLICT(project,sha) DO NOTHING`, remote, sha, ts); err != nil {
		return false, err
	}
	if c.HaveFiles {
		rows := make([]CommitFileStat, 0, min(len(c.Files), maxCommitFileRows))
		for _, f := range c.Files {
			if len(rows) >= maxCommitFileRows {
				break
			}
			path := cleanLine(f.Path, maxCommitFilePath)
			if path == "" {
				continue
			}
			rows = append(rows, CommitFileStat{Path: path, Additions: max(f.Additions, 0), Deletions: max(f.Deletions, 0)})
		}
		b, _ := json.Marshal(rows)
		total := max(c.FilesTotal, len(rows))
		if _, err := tx.Exec(`UPDATE git_commit_meta SET files=?, files_total=?, additions=?, deletions=?, files_at=? WHERE project=? AND sha=?`,
			string(b), total, max(c.Additions, 0), max(c.Deletions, 0), ts, remote, sha); err != nil {
			return false, err
		}
	}
	if c.HaveCI {
		if _, err := tx.Exec(`UPDATE git_commit_meta SET ci=?, ci_at=? WHERE project=? AND sha=?`, c.CI, ts, remote, sha); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// CommitsNeedingDetails nennt die Commits der PRs eines Projekts (jüngste zuerst),
// zu denen noch etwas fehlt: was ctx github sync-prs --commits und der Webhook
// nachholen. wantFiles fragt nach den Commits ohne Dateiliste, wantCI nach denen
// ohne Prüfstand und nach denen, deren Prüfung beim letzten Abruf noch lief. Jede
// Gruppe zählt nur nach ihrem eigenen *_at: darf die App eine Gruppe nicht holen
// (Recht fehlt), fragt der Aufrufer sie nicht an, sonst stünde derselbe Commit
// ewig vorn in der Warteschlange und verdrängte die älteren. Ohne beide Gruppen
// ist die Antwort leer.
func (s *Store) CommitsNeedingDetails(project string, limit int, wantFiles, wantCI bool) ([]string, error) {
	if s.reader != nil {
		return s.reader.CommitsNeedingDetails(project, limit, wantFiles, wantCI)
	}
	if !wantFiles && !wantCI {
		return nil, nil
	}
	if limit <= 0 || limit > 2000 {
		limit = 2000
	}
	rows, err := s.db.Query(`SELECT c.sha FROM git_pr_commits c LEFT JOIN git_commit_meta m ON m.project=c.project AND m.sha=c.sha
		WHERE c.project=?1 AND c.in_pr=1 AND (m.sha IS NULL OR (?2 AND m.files_at='') OR (?3 AND (m.ci_at='' OR m.ci='pending')))
		GROUP BY c.sha ORDER BY MAX(c.committed_at) DESC, c.sha LIMIT ?4`, scopeNormalize(project), wantFiles, wantCI, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, err
		}
		out = append(out, sha)
	}
	return out, rows.Err()
}

// commitMetaFor liest, was zu diesen Commits gespeichert ist. full holt auch
// Nachricht und Dateien; ohne sie bleiben die großen Spalten ungelesen (die Liste
// braucht nur den Prüfstand).
func (s *Store) commitMetaFor(project string, shas []string, full bool) (map[string]CommitMeta, error) {
	if s.reader != nil {
		return s.reader.commitMetaFor(project, shas, full)
	}
	out := map[string]CommitMeta{}
	if len(shas) == 0 {
		return out, nil
	}
	cols := `sha, ci, files_at, co_authors`
	if full {
		cols += `, parents, message, files, files_total, additions, deletions`
	}
	rows, err := s.db.Query(`SELECT `+cols+` FROM git_commit_meta WHERE project=?1 AND sha IN (SELECT value FROM json_each(?2))`, project, jsonList(shas))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m CommitMeta
		var filesAt, parents, co, files string
		dest := []any{&m.SHA, &m.CI, &filesAt, &co}
		if full {
			dest = append(dest, &parents, &m.Message, &files, &m.FilesTotal, &m.Additions, &m.Deletions)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		m.FilesKnown = filesAt != ""
		_ = json.Unmarshal([]byte(co), &m.CoAuthors)
		if full {
			_ = json.Unmarshal([]byte(parents), &m.Parents)
			_ = json.Unmarshal([]byte(files), &m.Files)
		}
		out[m.SHA] = m
	}
	return out, rows.Err()
}

// attachCommitMeta hängt Eltern, Nachricht, Mit-Autoren, Dateien und Prüfstand an die
// Commits. Das sind Angaben über den Code, nicht über das, was im Ledger steht; wer
// nicht alle Branches sehen darf (ein Gast), bekommt sie nicht, denn Dateipfade und
// Nachrichten verraten, woran auf einem Branch gearbeitet wird, den er nicht lesen darf.
func (a *ProjectAccess) attachCommitMeta(project string, items []CommitInfo, full bool) error {
	if len(items) == 0 || !a.SeesAllBranches(project) {
		return nil
	}
	shas := make([]string, 0, len(items))
	for _, c := range items {
		shas = append(shas, c.SHA)
	}
	meta, err := a.st.commitMetaFor(project, shas, full)
	if err != nil {
		return err
	}
	for i := range items {
		m, ok := meta[items[i].SHA]
		if !ok {
			continue
		}
		items[i].CI, items[i].CoAuthors = m.CI, m.CoAuthors
		if !full {
			continue
		}
		items[i].Parents = m.Parents
		items[i].Message = m.Message
		if m.FilesKnown {
			items[i].FilesKnown = true
			items[i].Files, items[i].FilesTotal = m.Files, m.FilesTotal
			items[i].Additions, items[i].Deletions = m.Additions, m.Deletions
		}
	}
	return nil
}
