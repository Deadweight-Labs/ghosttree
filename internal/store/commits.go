package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/knref"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Commits (REQ-599). Der Server hat das Repo nicht und bekommt keinen push-Webhook
// (der bräuchte das App-Recht "Contents", also Lesezugriff auf den Code). Er kennt
// einen Commit deshalb aus zwei Quellen:
//
//   - den Git-Ankern im Ledger (git_anchors): jeder Eintrag, der geschrieben wurde,
//     nennt den HEAD-Commit, den Branch und die Zeit;
//   - den Commits eines PRs, die er über "Pull requests: read" von GitHub holt
//     (git_pr_commits, gefüllt beim Webhook pull_request und bei ctx github
//     sync-prs): Titelzeile, Autor, Zeit.
//
// Ein Commit außerhalb von PRs, zu dem nie etwas gesagt wurde, ist dem Server
// unbekannt. Beide Quellen werden nach dem vollen SHA zusammengeführt.

// Grenzen.
const (
	CommitDefaultLimit = 30
	CommitMaxLimit     = 100
	// maxCommitRows begrenzt, wie viele Zeilen (Eintrag mal Commit) eine Abfrage
	// aus dem Ledger liest; es zählen die jüngsten.
	maxCommitRows = 20000
)

// anchoredRowsSQL ist die Teilabfrage aller Einträge eines Projekts, die einen
// Anker tragen: (anchor_id, origin_id, kind, id, path). ?1 ist das Projekt. Die
// Einträge sind die Tabellen von projectAnchorIDs, dazu die Fassungen der
// Kommentare und die Nachrichten des Projektraums (Direkt- und Gruppenräume
// gehören zu keinem Projekt und fehlen). Eine Zeile je
// Fassung: ein Eintrag mit mehreren Fassungen erscheint mehrfach, die Aufrufer
// zählen je (kind, id, path).
const anchoredRowsSQL = `
	SELECT anchor_id, origin_id, 'knowledge' AS kind, id, '' AS path FROM knowledge WHERE project=?1 AND +anchor_id IS NOT NULL
	UNION ALL SELECT kv.anchor_id, kv.origin_id, 'knowledge', k.id, '' FROM knowledge k CROSS JOIN knowledge_versions kv ON kv.knowledge_id=k.id WHERE k.project=?1 AND +kv.anchor_id IS NOT NULL
	UNION ALL SELECT anchor_id, origin_id, 'ghost', 0, path FROM ghost_files WHERE project=?1 AND +anchor_id IS NOT NULL
	UNION ALL SELECT anchor_id, origin_id, 'ghost', 0, path FROM ghost_file_versions WHERE project=?1 AND +anchor_id IS NOT NULL
	UNION ALL SELECT anchor_id, NULL, 'ghost', 0, path FROM ghost_branch_files WHERE project=?1 AND +anchor_id IS NOT NULL
	UNION ALL SELECT e.anchor_id, e.origin_id, 'request', r.id, '' FROM requests r CROSS JOIN request_evidence e ON e.request_id=r.id WHERE r.project=?1 AND e.anchor_id IS NOT NULL
	UNION ALL SELECT a.anchor_id, a.origin_id, 'request', r.id, '' FROM requests r CROSS JOIN request_activity a ON a.request_id=r.id WHERE r.project=?1 AND a.anchor_id IS NOT NULL
	UNION ALL SELECT pe.anchor_id, pe.origin_id, 'pain', p.id, '' FROM pains p CROSS JOIN pain_events pe ON pe.pain_id=p.id WHERE p.project=?1 AND +pe.anchor_id IS NOT NULL
	UNION ALL SELECT anchor_id, origin_id, 'idea', id, '' FROM ideas WHERE project=?1 AND +anchor_id IS NOT NULL
	UNION ALL SELECT fv.anchor_id, fv.origin_id, 'feature', f.id, '' FROM features f CROSS JOIN feature_versions fv ON fv.feature_id=f.id WHERE f.project=?1 AND +fv.anchor_id IS NOT NULL
	UNION ALL SELECT cv.anchor_id, cv.origin_id, 'comment', c.id, '' FROM comments c CROSS JOIN comment_versions cv ON cv.comment_id=c.id WHERE c.project=?1 AND +cv.anchor_id IS NOT NULL
	UNION ALL SELECT anchor_id, origin_id, 'message', id, '' FROM coord_messages WHERE destination_kind='room' AND destination_id='project:'||?1 AND +anchor_id IS NOT NULL`

// ensureCommitQueryIndexes legt additiv die Indizes an, über die anchoredRowsSQL
// beim Projekt anfängt statt bei allen Ankern der Instanz: Wissen, Issues und
// Kommentare hatten keinen Index auf dem Projekt, Nachweise und Aktivitäten
// keinen auf dem Issue, und origin_paths keinen auf dem Pfad. Die Indizes sind
// schmal (eine Spalte, die übrigen Schlüssel trägt der Index selbst); auf einer
// großen Datenbank läuft das Anlegen einmal beim Start.
func ensureCommitQueryIndexes(db *sql.DB) error {
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS knowledge_project ON knowledge(project)`,
		`CREATE INDEX IF NOT EXISTS requests_project ON requests(project)`,
		`CREATE INDEX IF NOT EXISTS comments_project ON comments(project)`,
		`CREATE INDEX IF NOT EXISTS request_evidence_request ON request_evidence(request_id)`,
		`CREATE INDEX IF NOT EXISTS request_activity_request ON request_activity(request_id)`,
		// NOCASE, weil der Pfadvergleich (LIKE) Groß- und Kleinschreibung nicht
		// unterscheidet; der Index trägt origin_id über den Primärschlüssel mit.
		`CREATE INDEX IF NOT EXISTS origin_paths_path ON origin_paths(path COLLATE NOCASE)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("commit query index: %w", err)
		}
	}
	return nil
}

// Arten von Einträgen, die an einem Commit hängen können.
const (
	CommitKindKnowledge = "knowledge"
	CommitKindGhost     = "ghost"
	CommitKindRequest   = "request"
	CommitKindPain      = "pain"
	CommitKindIdea      = "idea"
	CommitKindFeature   = "feature"
	CommitKindComment   = "comment"
	// CommitKindMessage ist eine Nachricht im Projektraum, die einen Anker trägt.
	CommitKindMessage = "message"
)

// anchoredRow ist ein Eintrag an einem Commit, wie er in der Datenbank steht,
// ohne Sichtprüfung.
type anchoredRow struct {
	Kind   string
	ID     int64
	Path   string
	SHA    string
	Branch string
	At     string
	PR     int64
	Origin int64
}

// rowCond schränkt die Abfrage der Ledger-Zeilen ein; eine leere Bedingung
// liefert alle Zeilen des Projekts.
type rowCond struct {
	Branch string
	SHAs   []string
	// Origins sind Entstehungskontexte (origin_contexts.id), z. B. die, die einen
	// Pfad nennen.
	Origins []int64
}

// jsonList übergibt eine lange Liste als einen einzigen Parameter (json_each):
// so gibt es keine Obergrenze der Parameter und keine Aufteilung in mehrere
// Abfragen, die die Union je Stück neu berechnen.
func jsonList[T any](v []T) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// anchoredRowsQuery setzt die Abfrage der Ledger-Zeilen zusammen; ?1 ist das
// Projekt. Das Limit gilt nach dem Projektfilter der Union.
func anchoredRowsQuery(where string, args []any) (string, []any) {
	args = append(args, maxCommitRows)
	return `SELECT DISTINCT r.kind, r.id, r.path, a.commit_sha, a.branch, a.captured_at, COALESCE(a.pr,0), COALESCE(r.origin_id,0)
		FROM (` + anchoredRowsSQL + `) r JOIN git_anchors a ON a.id=r.anchor_id` + where + `
		ORDER BY a.captured_at DESC, r.kind, r.id, r.path LIMIT ?` + strconv.Itoa(len(args)), args
}

func (s *Store) anchoredRowsFor(project string, c rowCond) ([]anchoredRow, error) {
	if s.reader != nil {
		return s.reader.anchoredRowsFor(project, c)
	}
	args := []any{project}
	var conds []string
	if c.Branch != "" {
		args = append(args, c.Branch)
		conds = append(conds, `a.branch=?`+strconv.Itoa(len(args)))
	}
	if len(c.SHAs) > 0 {
		args = append(args, jsonList(c.SHAs))
		conds = append(conds, `a.commit_sha IN (SELECT value FROM json_each(?`+strconv.Itoa(len(args))+`))`)
	}
	if len(c.Origins) > 0 {
		args = append(args, jsonList(c.Origins))
		conds = append(conds, `r.origin_id IN (SELECT value FROM json_each(?`+strconv.Itoa(len(args))+`))`)
	}
	where := ``
	if len(conds) > 0 {
		where = ` WHERE ` + strings.Join(conds, ` AND `)
	}
	q, args := anchoredRowsQuery(where, args)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []anchoredRow
	for rows.Next() {
		var r anchoredRow
		if err := rows.Scan(&r.Kind, &r.ID, &r.Path, &r.SHA, &r.Branch, &r.At, &r.PR, &r.Origin); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// prCommitRow ist ein Commit aus git_pr_commits samt dem Head-Branch seines PRs.
type prCommitRow struct {
	SHA, Author, AuthorName, Title, At, Head string
	PR                                       int64
	Position                                 int
}

// prCommitsFor liest die Commits der PRs eines Projekts (nur die, die ihr PR
// beim letzten Abgleich trug): eines Head-Branches, eines PRs oder bestimmter SHAs.
func (s *Store) prCommitsFor(project string, branch string, pr int64, shas []string) ([]prCommitRow, error) {
	if s.reader != nil {
		return s.reader.prCommitsFor(project, branch, pr, shas)
	}
	args := []any{project}
	where := `c.project=?1 AND c.in_pr=1`
	if branch != "" {
		args = append(args, branch)
		where += ` AND p.head_ref=?` + strconv.Itoa(len(args))
	}
	if pr > 0 {
		args = append(args, pr)
		where += ` AND c.pr=?` + strconv.Itoa(len(args))
	}
	if len(shas) > 0 {
		args = append(args, jsonList(shas))
		where += ` AND c.sha IN (SELECT value FROM json_each(?` + strconv.Itoa(len(args)) + `))`
	}
	rows, err := s.db.Query(`SELECT c.sha,c.author,c.author_name,c.title,c.committed_at,p.head_ref,c.pr,c.position
		FROM git_pr_commits c JOIN git_pull_requests p ON p.project=c.project AND p.number=c.pr WHERE `+where+`
		ORDER BY c.pr, c.position LIMIT 20000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []prCommitRow
	for rows.Next() {
		var r prCommitRow
		if err := rows.Scan(&r.SHA, &r.Author, &r.AuthorName, &r.Title, &r.At, &r.Head, &r.PR, &r.Position); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// maxCommitCandidates ist die Seitengröße, in der ein Präfix seine Kandidaten
// liefert. CommitView blättert weiter, bis zwei sichtbare Treffer feststehen oder
// die Kandidaten erschöpft sind: unsichtbare Treffer verdrängen so keinen
// sichtbaren.
const maxCommitCandidates = 200

// commitCandidatesSQL: ?1 Projekt, ?2 LIKE-Muster, ?3 Limit, ?4 und ?5 der
// Bereich des Präfixes (über den Index), ?6 der letzte schon gelieferte SHA
// (leer am Anfang); LIKE bleibt als Rest, damit sich an der Übereinstimmung
// nichts ändert.
const commitCandidatesSQL = `SELECT sha FROM (
	SELECT a.commit_sha AS sha FROM (` + anchoredRowsSQL + `) r JOIN git_anchors a ON a.id=r.anchor_id WHERE a.commit_sha>=?4 AND a.commit_sha<?5 AND a.commit_sha>?6 AND a.commit_sha LIKE ?2
	UNION SELECT sha FROM git_pr_commits WHERE project=?1 AND sha>=?4 AND sha<?5 AND sha>?6 AND sha LIKE ?2 AND in_pr=1) ORDER BY sha LIMIT ?3`

// commitCandidates liefert die vollen SHAs, die ein Präfix (mindestens 7 Zeichen) im
// Projekt treffen könnte (Anker oder PR-Commit), ohne Sichtprüfung. Ob ein Kandidat
// für einen Betrachter existiert, entscheidet erst ProjectAccess.CommitView.
func (s *Store) commitCandidates(project, prefix, after string) ([]string, error) {
	if s.reader != nil {
		return s.reader.commitCandidates(project, prefix, after)
	}
	if prefix == "" {
		return nil, nil
	}
	upper := prefix[:len(prefix)-1] + string(rune(prefix[len(prefix)-1]+1))
	rows, err := s.db.Query(commitCandidatesSQL, project, prefix+"%", maxCommitCandidates, prefix, upper, after)
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

// originsForPathSQL: ?1 Projekt, ?2 Pfad, ?3 LIKE-Muster der Kinder, ?4 obere
// Grenze des Bereichs. Der Bereich nutzt den Index origin_paths_path; das genaue
// Muster prüft der Rest.
const originsForPathSQL = `SELECT p.origin_id, p.kind, p.weight FROM origin_paths p
	WHERE p.path COLLATE NOCASE >= ?2 AND p.path COLLATE NOCASE < ?4 AND (p.path=?2 OR p.path LIKE ?3 ESCAPE '\')
	AND p.origin_id IN (SELECT origin_id FROM (` + anchoredRowsSQL + `) WHERE origin_id IS NOT NULL) LIMIT 50000`

// originsForPath liefert die Entstehungskontexte, die diesen Pfad oder etwas
// darunter nennen, mit Art und Gewicht. Es zählen nur Kontexte, an denen im
// Projekt ein Eintrag mit Anker hängt: der Projektfilter kommt vor dem Limit,
// damit die Pfade anderer Projekte keinen Treffer verdrängen.
func (s *Store) originsForPath(project, path string) (map[int64]originHit, error) {
	if s.reader != nil {
		return s.reader.originsForPath(project, path)
	}
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(path)
	rows, err := s.db.Query(originsForPathSQL, project, path, esc+"/%", path+"0")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]originHit{}
	for rows.Next() {
		var id int64
		var kind string
		var w int
		if err := rows.Scan(&id, &kind, &w); err != nil {
			return nil, err
		}
		h := out[id]
		if !slices.Contains(h.Kinds, kind) {
			h.Kinds = append(h.Kinds, kind)
		}
		h.Weight += w
		out[id] = h
	}
	return out, rows.Err()
}

type originHit struct {
	Kinds  []string
	Weight int
}

// ---------------------------------------------------------------- Sicht

// CommitEntry ist ein Eintrag des Ledgers, der an einem Commit hängt: das, was
// dazu gesagt wurde.
type CommitEntry struct {
	Kind string `json:"kind"`
	// ID ist die Nummer des Objekts (bei einem Kommentar die des Kommentars), bei
	// einer Ghost-Beschreibung 0; dort nennt Path die Datei.
	ID     int64  `json:"id,omitempty"`
	Ref    string `json:"ref"`
	Path   string `json:"path,omitempty"`
	Title  string `json:"title"`
	Person string `json:"person,omitempty"`
	// At ist die früheste Zeit, zu der etwas an diesem Commit dazu geschrieben
	// wurde.
	At string `json:"at"`
	// Branch ist der Branch des Ankers.
	Branch string `json:"branch,omitempty"`
	// ObjectKind und ObjectID nennen bei einem Kommentar das Objekt, an dem er hängt.
	ObjectKind string `json:"object_kind,omitempty"`
	ObjectID   int64  `json:"object_id,omitempty"`
}

// CommitInfo ist ein Commit, wie der Server ihn kennt.
type CommitInfo struct {
	SHA   string `json:"sha"`
	Short string `json:"short"`
	// Title, Author und AuthorName kommen aus der PR-Quelle und fehlen bei einem
	// Commit, der nur im Ledger vorkommt.
	Title      string `json:"title,omitempty"`
	Author     string `json:"author,omitempty"`
	AuthorName string `json:"author_name,omitempty"`
	// At ist die Zeit des Commits (PR-Quelle), sonst die des ersten Eintrags.
	At       string   `json:"at"`
	Branches []string `json:"branches,omitempty"`
	PRs      []int64  `json:"prs,omitempty"`
	// Entries ist die Zahl der Einträge des Ledgers an diesem Commit, die der
	// Betrachter lesen darf (ein Eintrag zählt einmal, auch mit mehreren Fassungen).
	Entries int `json:"entries"`
	// Sources sagt, woher der Server den Commit kennt: ledger und/oder pr.
	Sources []string `json:"sources"`
}

// CommitList ist eine Seite der Commits eines Projekts oder Branches.
type CommitList struct {
	Project string       `json:"project"`
	Branch  string       `json:"branch,omitempty"`
	Items   []CommitInfo `json:"items"`
	Total   int          `json:"total"`
	Limit   int          `json:"limit"`
	Offset  int          `json:"offset"`
}

// CommitDetail ist ein Commit mit dem, was zu ihm gesagt wurde.
type CommitDetail struct {
	CommitInfo
	Project string        `json:"project"`
	Said    []CommitEntry `json:"said"`
}

// PathCommit ist ein Commit, an dem Einträge hängen, deren Entstehungskontext
// einen Pfad nennt.
type PathCommit struct {
	CommitInfo
	// Kinds sind die Arten, in denen der Pfad im Kontext vorkam (read, write,
	// named), Weight die Summe der Gewichte.
	Kinds   []string      `json:"kinds"`
	Weight  int           `json:"weight"`
	Matched []CommitEntry `json:"matched"`
}

// PathCommitList sind die Commits eines Pfades, die jüngsten zuerst.
type PathCommitList struct {
	Project string       `json:"project"`
	Path    string       `json:"path"`
	Items   []PathCommit `json:"items"`
	Total   int          `json:"total"`
	Limit   int          `json:"limit"`
	Offset  int          `json:"offset"`
}

// PullCommitList sind die Commits eines PRs in der Reihenfolge des PRs.
type PullCommitList struct {
	Project string       `json:"project"`
	PR      int64        `json:"pr"`
	Items   []CommitInfo `json:"items"`
}

type entryKey struct {
	kind string
	id   int64
	path string
}

// entryResolver prüft Einträge gegen die Rechte des Betrachters und merkt sich
// das Ergebnis je Eintrag.
type entryResolver struct {
	a       *ProjectAccess
	project string
	cache   map[entryKey]*CommitEntry
	// roomRead merkt, ob der Betrachter den Projektraum lesen darf (einmal je
	// Auflöser gefragt, erst bei der ersten Nachricht).
	roomRead *bool
}

// canReadRoom sagt, ob der Betrachter den Projektraum lesen darf: dieselbe
// Menge, die CoordAccess.Rooms() ihm zeigt, nicht mehr.
func (e *entryResolver) canReadRoom() bool {
	if e.roomRead == nil {
		ok := false
		key := RoomKeyForProject(e.project)
		if rooms, err := e.a.st.CoordinationFor(e.a.principal, "").Rooms(); err == nil {
			ok = slices.ContainsFunc(rooms, func(r CoordRoom) bool { return r.Key == key })
		}
		e.roomRead = &ok
	}
	return *e.roomRead
}

func (a *ProjectAccess) newEntryResolver(project string) *entryResolver {
	return &entryResolver{a: a, project: project, cache: map[entryKey]*CommitEntry{}}
}

// resolve liefert den Eintrag, wenn der Betrachter ihn lesen darf und er noch im
// Projekt liegt (ein verschobenes Objekt gehört nicht mehr an den Commit dieses
// Projekts); sonst nil.
func (e *entryResolver) resolve(r anchoredRow) *CommitEntry {
	k := entryKey{r.Kind, r.ID, r.Path}
	if c, ok := e.cache[k]; ok {
		return c
	}
	c := e.load(r)
	e.cache[k] = c
	return c
}

func (e *entryResolver) load(r anchoredRow) *CommitEntry {
	a, project := e.a, e.project
	switch r.Kind {
	case CommitKindKnowledge:
		k, err := a.st.KnowledgeByID(r.ID)
		if err != nil || k.Scope.Project != project || !a.CanSeeKnowledge(k) {
			return nil
		}
		return &CommitEntry{Kind: r.Kind, ID: r.ID, Ref: knref.Format(r.ID), Title: oneLine(k.Title, 200), Person: k.Person}
	case CommitKindGhost:
		if g, err := a.st.GhostFileByPath(project, r.Path); err == nil {
			if !a.CanSeeGhost(g) {
				return nil
			}
		} else if !a.Allow(project, ResGhost, ActRead, Object{}) {
			return nil
		}
		return &CommitEntry{Kind: r.Kind, Ref: r.Path, Path: r.Path, Title: r.Path}
	case CommitKindRequest:
		ref, err := a.st.RequestRef(RefRequest, r.ID)
		if err != nil || ref.Project != project || !requestReadable(a, ref) {
			return nil
		}
		return &CommitEntry{Kind: r.Kind, ID: r.ID, Ref: fmt.Sprintf("REQ-%d", r.ID), Title: oneLine(a.st.requestTitle(r.ID), 200), Person: ref.Person}
	case CommitKindPain:
		p, err := a.st.PainByID(r.ID)
		if err != nil || p.Project != project || !a.CanSeePain(p) {
			return nil
		}
		return &CommitEntry{Kind: r.Kind, ID: r.ID, Ref: fmt.Sprintf("PAIN-%d", r.ID), Title: oneLine(p.Title, 200), Person: p.Person}
	case CommitKindIdea:
		i, err := a.st.IdeaByID(r.ID)
		if err != nil || i.Project != project || !a.CanSeeIdea(i) {
			return nil
		}
		return &CommitEntry{Kind: r.Kind, ID: r.ID, Ref: fmt.Sprintf("IDEA-%d", r.ID), Title: oneLine(i.Title, 200), Person: i.Person}
	case CommitKindFeature:
		f, err := a.st.featureByID(r.ID)
		if err != nil || f.Project != project || !a.Allow(f.Project, ResFeature, ActRead, Object{}) {
			return nil
		}
		return &CommitEntry{Kind: r.Kind, ID: r.ID, Ref: fmt.Sprintf("FEAT-%d", r.ID), Title: oneLine(f.Name, 200), Person: f.Person}
	case CommitKindMessage:
		if !e.canReadRoom() {
			return nil
		}
		m, ok := a.st.roomMessageForCommit(r.ID, RoomKeyForProject(project))
		if !ok {
			return nil
		}
		who := ""
		if m.authorKind == AuthorHuman && !a.HidesMachines(project) {
			who = a.st.personNameOf(m.principal)
		}
		return &CommitEntry{Kind: r.Kind, ID: r.ID, Ref: fmt.Sprintf("message:%d", r.ID), Title: oneLine(m.body, 200), Person: who}
	case CommitKindComment:
		c, err := a.st.commentByID(r.ID)
		if err != nil || c.Deleted {
			return nil
		}
		p, err := a.commentTarget(c.ObjectKind, c.ObjectID)
		if err != nil || p != project {
			return nil
		}
		return &CommitEntry{Kind: r.Kind, ID: r.ID, Ref: fmt.Sprintf("comment:%d", r.ID), Title: oneLine(c.Body, 200),
			Person: c.Person, ObjectKind: c.ObjectKind, ObjectID: c.ObjectID}
	}
	return nil
}

func objectRef(kind string, id int64) string {
	switch kind {
	case CommentOnRequest:
		return fmt.Sprintf("REQ-%d", id)
	case CommentOnIdea:
		return fmt.Sprintf("IDEA-%d", id)
	case CommentOnPain:
		return fmt.Sprintf("PAIN-%d", id)
	case CommentOnKnowledge:
		return fmt.Sprintf("KN-%d", id)
	}
	return kind
}

// commitAgg sammelt, was der Server zu einem Commit weiß.
type commitAgg struct {
	sha                       string
	firstAnchor, committed    string
	title, author, authorName string
	branches                  map[string]bool
	prs                       map[int64]bool
	sources                   map[string]bool
	entries                   map[entryKey]*CommitEntry
	order                     []entryKey
	kinds                     map[string]bool
	weight                    int
	originSeen                map[int64]bool
}

func newAgg(sha string) *commitAgg {
	return &commitAgg{sha: sha, branches: map[string]bool{}, prs: map[int64]bool{}, sources: map[string]bool{}, entries: map[entryKey]*CommitEntry{},
		kinds: map[string]bool{}, originSeen: map[int64]bool{}}
}

func (g *commitAgg) info() CommitInfo {
	at := g.committed
	if at == "" {
		at = g.firstAnchor
	}
	ci := CommitInfo{SHA: g.sha, Short: gitanchor.ShortCommit(g.sha), Title: g.title, Author: g.author, AuthorName: g.authorName, At: at, Entries: len(g.entries),
		Sources: []string{}}
	for b := range g.branches {
		ci.Branches = append(ci.Branches, b)
	}
	sort.Strings(ci.Branches)
	for p := range g.prs {
		ci.PRs = append(ci.PRs, p)
	}
	slices.Sort(ci.PRs)
	for _, s := range []string{"ledger", "pr"} {
		if g.sources[s] {
			ci.Sources = append(ci.Sources, s)
		}
	}
	return ci
}

func (g *commitAgg) entryList() []CommitEntry {
	out := make([]CommitEntry, 0, len(g.order))
	for _, k := range g.order {
		out = append(out, *g.entries[k])
	}
	return out
}

// commitIndex führt die beiden Quellen zusammen. Eine Zeile des Ledgers zählt
// nur, wenn der Betrachter den Eintrag lesen darf; ein Commit, der nur aus dem
// Ledger bekannt ist und an dem er nichts lesen darf, fehlt (sonst verriete die
// Liste, dass etwas Verborgenes dort geschrieben wurde).
func (a *ProjectAccess) commitIndex(project string, cond rowCond, prBranch string, prNumber int64, withPR bool) (map[string]*commitAgg, error) {
	rows, err := a.st.anchoredRowsFor(project, cond)
	if err != nil {
		return nil, err
	}
	res := a.newEntryResolver(project)
	aggs := map[string]*commitAgg{}
	for _, r := range rows {
		e := res.resolve(r)
		if e == nil {
			continue
		}
		g := aggs[r.SHA]
		if g == nil {
			g = newAgg(r.SHA)
			aggs[r.SHA] = g
		}
		g.sources["ledger"] = true
		if g.firstAnchor == "" || r.At < g.firstAnchor {
			g.firstAnchor = r.At
		}
		if r.Branch != "" {
			g.branches[r.Branch] = true
		}
		if r.PR > 0 {
			g.prs[r.PR] = true
		}
		k := entryKey{r.Kind, r.ID, r.Path}
		if prev, ok := g.entries[k]; ok {
			if r.At < prev.At {
				prev.At = r.At
			}
			continue
		}
		cp := *e
		cp.At, cp.Branch = r.At, r.Branch
		g.entries[k] = &cp
		g.order = append(g.order, k)
	}
	if withPR {
		prs, err := a.st.prCommitsFor(project, prBranch, prNumber, cond.SHAs)
		if err != nil {
			return nil, err
		}
		for _, c := range prs {
			g := aggs[c.SHA]
			if g == nil {
				g = newAgg(c.SHA)
				aggs[c.SHA] = g
			}
			g.sources["pr"] = true
			g.prs[c.PR] = true
			if c.Head != "" {
				g.branches[c.Head] = true
			}
			if g.title == "" {
				g.title, g.author, g.authorName = c.Title, c.Author, c.AuthorName
			}
			if c.At != "" && (g.committed == "" || c.At < g.committed) {
				g.committed = c.At
			}
		}
	}
	return aggs, nil
}

func sortedInfos(aggs map[string]*commitAgg) []CommitInfo {
	out := make([]CommitInfo, 0, len(aggs))
	for _, g := range aggs {
		out = append(out, g.info())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].At != out[j].At {
			return out[i].At > out[j].At
		}
		return out[i].SHA < out[j].SHA
	})
	return out
}

func pageOf[T any](items []T, limit, offset int) []T {
	if offset >= len(items) {
		return []T{}
	}
	end := min(offset+limit, len(items))
	return items[offset:end]
}

func normalizePage(limit, offset int) (int, int, error) {
	if limit <= 0 {
		limit = CommitDefaultLimit
	}
	limit = min(limit, CommitMaxLimit)
	if offset < 0 {
		return 0, 0, pullRuleError("offset_invalid", "offset must not be negative", "start at 0")
	}
	return limit, offset, nil
}

// readProject prüft das Leserecht am Projekt. Ein Projekt, das es nicht gibt, und
// eines, das der Betrachter nicht lesen darf, sind beide sql.ErrNoRows.
func (a *ProjectAccess) readProject(project string) (string, error) {
	project = scope.NormalizeRemote(strings.TrimSpace(project))
	if project == "" {
		a.Filtered()
		return "", sql.ErrNoRows
	}
	if err := a.Check(project, ResProject, ActRead, Object{}); err != nil {
		return "", sql.ErrNoRows
	}
	return project, nil
}

// Commits liefert die Commits eines Projekts, mit branch nur die eines Branches
// (aus beiden Quellen, nach SHA zusammengeführt, die jüngsten zuerst).
func (a *ProjectAccess) Commits(project, branch string, limit, offset int) (CommitList, error) {
	project, err := a.readProject(project)
	if err != nil {
		return CommitList{}, err
	}
	limit, offset, err = normalizePage(limit, offset)
	if err != nil {
		return CommitList{}, err
	}
	branch = strings.TrimSpace(branch)
	aggs, err := a.commitsOfBranch(project, branch)
	if err != nil {
		return CommitList{}, err
	}
	all := sortedInfos(aggs)
	return CommitList{Project: project, Branch: branch, Items: pageOf(all, limit, offset), Total: len(all), Limit: limit, Offset: offset}, nil
}

// commitsOfBranch führt die Commits eines Branches zusammen. Welche Commits
// dazugehören, bestimmen die Anker auf dem Branch und die Commits seiner PRs; die
// Zahl der Einträge je Commit zählt dann alle Einträge an ihm, nicht nur die, die
// auf diesem Branch geschrieben wurden.
func (a *ProjectAccess) commitsOfBranch(project, branch string) (map[string]*commitAgg, error) {
	if branch == "" {
		return a.commitIndex(project, rowCond{}, "", 0, true)
	}
	onBranch, err := a.commitIndex(project, rowCond{Branch: branch}, branch, 0, true)
	if err != nil {
		return nil, err
	}
	shas := make([]string, 0, len(onBranch))
	for sha := range onBranch {
		shas = append(shas, sha)
	}
	slices.Sort(shas)
	if len(shas) == 0 {
		return map[string]*commitAgg{}, nil
	}
	return a.commitIndex(project, rowCond{SHAs: shas}, "", 0, true)
}

// CommitView liefert einen Commit (voller SHA oder Präfix ab 7 Zeichen) mit den
// Einträgen, die dazu geschrieben wurden und die der Betrachter lesen darf.
// Unbekannt und unlesbar sind sql.ErrNoRows; ein zu kurzer oder mehrdeutiger
// Präfix ist ein Regelfehler.
func (a *ProjectAccess) CommitView(project, sha string) (CommitDetail, error) {
	project, err := a.readProject(project)
	if err != nil {
		return CommitDetail{}, err
	}
	sha = gitanchor.Normalize(sha)
	if !gitanchor.ValidPrefix(sha) {
		return CommitDetail{}, pullRuleError("commit_invalid", fmt.Sprintf("a commit needs at least %d hexadecimal characters", gitanchor.MinPrefix), "pass the hash or its first characters")
	}
	// Der Präfix wird nur gegen Commits aufgelöst, an denen der Betrachter etwas lesen
	// kann: ein zweiter, für ihn unsichtbarer Treffer darf weder "mehrdeutig" noch
	// sonst einen Unterschied machen.
	var full string
	var visible int
	var aggs map[string]*commitAgg
	after := ""
	for visible < 2 {
		candidates, err := a.st.commitCandidates(project, sha, after)
		if err != nil {
			return CommitDetail{}, err
		}
		if len(candidates) == 0 {
			break
		}
		after = candidates[len(candidates)-1]
		// Eine Abfrage für alle Kandidaten der Seite: die Union des Projekts wird
		// einmal berechnet, nicht je Kandidat.
		m, err := a.commitIndex(project, rowCond{SHAs: candidates}, "", 0, true)
		if err != nil {
			return CommitDetail{}, err
		}
		for _, c := range candidates {
			if m[c] != nil {
				full, aggs = c, m
				visible++
			}
		}
		if len(candidates) < maxCommitCandidates {
			break
		}
	}
	switch {
	case visible == 0:
		a.Filtered()
		return CommitDetail{}, sql.ErrNoRows
	case visible > 1:
		return CommitDetail{}, pullRuleError("commit_ambiguous", "the commit prefix matches more than one commit", "give more characters of the hash")
	}
	g := aggs[full]
	if g == nil {
		return CommitDetail{}, sql.ErrNoRows
	}
	return CommitDetail{CommitInfo: g.info(), Project: project, Said: g.entryList()}, nil
}

// PullCommits liefert die Commits eines PRs in der Reihenfolge, in der GitHub sie
// meldet, mit der Zahl der lesbaren Einträge je Commit. Ein PR, den es nicht
// gibt, und ein Projekt ohne Leserecht sind sql.ErrNoRows; ein PR, dessen
// Commits noch nicht abgeglichen sind, hat eine leere Liste.
func (a *ProjectAccess) PullCommits(project string, n int64) (PullCommitList, error) {
	if _, err := a.PullRequestView(project, n); err != nil {
		return PullCommitList{}, err
	}
	project = scope.NormalizeRemote(strings.TrimSpace(project))
	prs, err := a.st.prCommitsFor(project, "", n, nil)
	if err != nil {
		return PullCommitList{}, err
	}
	shas := make([]string, 0, len(prs))
	for _, c := range prs {
		shas = append(shas, c.SHA)
	}
	out := PullCommitList{Project: project, PR: n, Items: []CommitInfo{}}
	if len(shas) == 0 {
		return out, nil
	}
	aggs, err := a.commitIndex(project, rowCond{SHAs: shas}, "", 0, true)
	if err != nil {
		return PullCommitList{}, err
	}
	for _, c := range prs {
		if g := aggs[c.SHA]; g != nil {
			// Die Reihenfolge ist die des PRs, nicht die der Zeit.
			ci := g.info()
			out.Items = append(out.Items, ci)
		}
	}
	return out, nil
}

// CommitsForPath liefert die Commits, an denen Einträge hängen, deren
// Entstehungskontext (Pfade der Session beim Schreiben, REQ-600) diesen Pfad oder
// etwas darunter nennt. Es sind die Einträge, die der Betrachter lesen darf.
func (a *ProjectAccess) CommitsForPath(project, path string, limit, offset int) (PathCommitList, error) {
	project, err := a.readProject(project)
	if err != nil {
		return PathCommitList{}, err
	}
	limit, offset, err = normalizePage(limit, offset)
	if err != nil {
		return PathCommitList{}, err
	}
	path = strings.TrimSpace(path)
	clean, ok := feature.CleanPath(strings.TrimRight(path, "/"))
	if !ok || clean == "" || strings.HasPrefix(path, "/") {
		return PathCommitList{}, pullRuleError("path_invalid", "name a path inside the repository", "use a relative path such as internal/store")
	}
	hits, err := a.st.originsForPath(project, clean)
	if err != nil {
		return PathCommitList{}, err
	}
	out := PathCommitList{Project: project, Path: clean, Items: []PathCommit{}, Limit: limit, Offset: offset}
	if len(hits) == 0 {
		return out, nil
	}
	origins := make([]int64, 0, len(hits))
	for id := range hits {
		origins = append(origins, id)
	}
	slices.Sort(origins)
	rows, err := a.pathRows(project, origins)
	if err != nil {
		return PathCommitList{}, err
	}
	res := a.newEntryResolver(project)
	aggs := map[string]*commitAgg{}
	for _, r := range rows {
		e := res.resolve(r)
		if e == nil {
			continue
		}
		g := aggs[r.SHA]
		if g == nil {
			g = newAgg(r.SHA)
			aggs[r.SHA] = g
		}
		g.sources["ledger"] = true
		if g.firstAnchor == "" || r.At < g.firstAnchor {
			g.firstAnchor = r.At
		}
		if r.Branch != "" {
			g.branches[r.Branch] = true
		}
		if r.PR > 0 {
			g.prs[r.PR] = true
		}
		if !g.originSeen[r.Origin] {
			g.originSeen[r.Origin] = true
			h := hits[r.Origin]
			g.weight += h.Weight
			for _, k := range h.Kinds {
				g.kinds[k] = true
			}
		}
		k := entryKey{r.Kind, r.ID, r.Path}
		if _, dup := g.entries[k]; dup {
			continue
		}
		cp := *e
		cp.At, cp.Branch = r.At, r.Branch
		g.entries[k] = &cp
		g.order = append(g.order, k)
	}
	if len(aggs) > 0 {
		shas := make([]string, 0, len(aggs))
		for sha := range aggs {
			shas = append(shas, sha)
		}
		prs, err := a.st.prCommitsFor(project, "", 0, shas)
		if err != nil {
			return PathCommitList{}, err
		}
		for _, c := range prs {
			g := aggs[c.SHA]
			g.sources["pr"] = true
			g.prs[c.PR] = true
			if g.title == "" {
				g.title, g.author, g.authorName = c.Title, c.Author, c.AuthorName
			}
			if c.At != "" && (g.committed == "" || c.At < g.committed) {
				g.committed = c.At
			}
		}
	}
	list := make([]PathCommit, 0, len(aggs))
	for _, ci := range sortedInfos(aggs) {
		g := aggs[ci.SHA]
		pc := PathCommit{CommitInfo: ci, Weight: g.weight, Matched: g.entryList(), Kinds: []string{}}
		for _, k := range []string{"write", "named", "read"} {
			if g.kinds[k] {
				pc.Kinds = append(pc.Kinds, k)
			}
		}
		list = append(list, pc)
	}
	out.Total = len(list)
	out.Items = pageOf(list, limit, offset)
	return out, nil
}

func (a *ProjectAccess) pathRows(project string, origins []int64) ([]anchoredRow, error) {
	return a.st.anchoredRowsFor(project, rowCond{Origins: origins})
}

// commitMessage ist, was die Commit-Seite von einer Raum-Nachricht braucht.
type commitMessage struct {
	body, authorKind, principal string
}

// roomMessageForCommit liest eine Nachricht des Projektraums. Das Leserecht am
// Raum prüft der Aufrufer.
func (s *Store) roomMessageForCommit(id int64, roomKey string) (commitMessage, bool) {
	if s.reader != nil {
		return s.reader.roomMessageForCommit(id, roomKey)
	}
	var m commitMessage
	err := s.db.QueryRow(`SELECT body, author_kind, author_principal_id FROM coord_messages
		WHERE id=? AND destination_kind='room' AND destination_id=?`, id, roomKey).Scan(&m.body, &m.authorKind, &m.principal)
	return m, err == nil
}

// personNameOf ist der Kontoname eines Absenders "person:N", sonst "".
func (s *Store) personNameOf(principal string) string {
	n, ok := accountNumericID(principal)
	if !ok {
		return ""
	}
	if s.reader != nil {
		return s.reader.personNameOf(principal)
	}
	var name string
	if err := s.db.QueryRow(`SELECT name FROM persons WHERE id=?`, n).Scan(&name); err != nil {
		return ""
	}
	return NormalizeAccountName(name)
}

// pullMessageRows liest die Nachrichten des Projektraums, deren Anker die Nummer
// des PRs trägt oder auf einem seiner Commits sitzt, ohne Sichtprüfung.
func (s *Store) pullMessageRows(project string, n int64, shas []string) ([]anchoredRow, error) {
	if s.reader != nil {
		return s.reader.pullMessageRows(project, n, shas)
	}
	rows, err := s.db.Query(`SELECT m.id, a.commit_sha, a.branch, a.captured_at, COALESCE(a.pr,0), COALESCE(m.origin_id,0)
		FROM coord_messages m JOIN git_anchors a ON a.id=m.anchor_id
		WHERE m.destination_kind='room' AND m.destination_id=?1 AND (a.pr=?2 OR a.commit_sha IN (SELECT value FROM json_each(?3)))
		ORDER BY m.id LIMIT 1000`, RoomKeyForProject(project), n, jsonList(shas))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []anchoredRow
	for rows.Next() {
		r := anchoredRow{Kind: CommitKindMessage}
		if err := rows.Scan(&r.ID, &r.SHA, &r.Branch, &r.At, &r.PR, &r.Origin); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PullMessages liefert die Raum-Nachrichten, die zu einem PR gesagt wurden: die,
// deren Anker die PR-Nummer trägt oder auf einem Commit des PRs sitzt. Es sind
// die Nachrichten eines Raums, den der Betrachter lesen darf; sonst bleibt die
// Liste leer. Ein PR, den es nicht gibt, und ein Projekt ohne Leserecht sind
// sql.ErrNoRows.
func (a *ProjectAccess) PullMessages(project string, n int64) ([]CommitEntry, error) {
	if _, err := a.PullRequestView(project, n); err != nil {
		return nil, err
	}
	project = scope.NormalizeRemote(strings.TrimSpace(project))
	prs, err := a.st.prCommitsFor(project, "", n, nil)
	if err != nil {
		return nil, err
	}
	shas := make([]string, 0, len(prs))
	for _, c := range prs {
		shas = append(shas, c.SHA)
	}
	rows, err := a.st.pullMessageRows(project, n, shas)
	if err != nil {
		return nil, err
	}
	res := a.newEntryResolver(project)
	out := []CommitEntry{}
	for _, r := range rows {
		if e := res.resolve(r); e != nil {
			cp := *e
			cp.At, cp.Branch = r.At, r.Branch
			out = append(out, cp)
		}
	}
	return out, nil
}
