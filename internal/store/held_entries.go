package store

import (
	"errors"
	"strings"
)

// Entries other accounts wrote under a remote before it became a project are
// held back when the project comes into existence (review of REQ-623, point
// 2): otherwise anybody could plant text under a repository before its owner
// claims it, and the owner's agents would read it as project knowledge.
//
// Held are knowledge, requests, pains, ideas and documents whose author is no
// owner or member of the project's new owner. A held entry is visible to its
// author, and to the project's owners, leads and reviewers in the list of held
// entries; it reaches no list, search, context or mirror of anybody else
// until a reviewer releases it. Discarding keeps it hidden for good (its
// author still sees it). Nothing is deleted.

const heldSchema = `
CREATE TABLE IF NOT EXISTS held_entries(
  kind TEXT NOT NULL CHECK(kind IN ('knowledge','request','pain','idea','document')),
  entry_id INTEGER NOT NULL,
  project TEXT NOT NULL,
  author_id INTEGER NOT NULL,
  state TEXT NOT NULL DEFAULT 'held' CHECK(state IN ('held','released','discarded')),
  held_at TEXT NOT NULL,
  decided_by INTEGER NOT NULL DEFAULT 0,
  decided_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(kind, entry_id)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS held_entries_project ON held_entries(project, state);
`

// Held entry kinds.
const (
	HeldKnowledge = "knowledge"
	HeldRequest   = "request"
	HeldPain      = "pain"
	HeldIdea      = "idea"
	HeldDocument  = "document"
)

var (
	// ErrHeldNotFound: no held entry of that kind and number.
	ErrHeldNotFound = errors.New("no held entry of that kind and number")
	// ErrNotReviewer: releasing or discarding needs the review right in the
	// project (owner, lead or can_review).
	ErrNotReviewer = errors.New("releasing or discarding a held entry needs the review right in the project")
)

// heldSources say, per kind, where its rows and their recorded author live.
// An author recorded at writing wins; the name is read where none was.
var heldSources = []struct{ kind, query string }{
	{HeldKnowledge, `SELECT t.id, COALESCE(a.account_id, -1), t.person FROM knowledge t LEFT JOIN knowledge_authors a ON a.knowledge_id=t.id WHERE t.project=?`},
	{HeldRequest, `SELECT t.id, COALESCE(a.account_id, -1), t.person FROM requests t LEFT JOIN request_authors a ON a.request_id=t.id WHERE t.project=?`},
	{HeldPain, `SELECT t.id, COALESCE(a.account_id, -1), t.person FROM pains t LEFT JOIN pain_authors a ON a.pain_id=t.id WHERE t.project=?`},
	{HeldIdea, `SELECT t.id, COALESCE(a.account_id, -1), t.person FROM ideas t LEFT JOIN idea_authors a ON a.idea_id=t.id WHERE t.project=?`},
	{HeldDocument, `SELECT t.id, -1, t.person FROM documents t WHERE t.project=?`},
}

// holdForeignEntriesTx runs when a project comes into existence (rehold
// false) or gets a new owner (rehold true): it holds every entry under the
// remote whose author is no writer under that owner, an owner or member of it
// or somebody with a writing role (owner, lead, member) in the project itself.
// An entry whose author cannot be told apart (a name two accounts had, an
// entry recorded without owner) is held too; an unknown or empty name is the
// instance owner's, as everywhere else.
//
// Every change of owner is treated alike (ReassignProject, MoveProject,
// ForceMoveProject, TakeOverProject and the projects the owner migration moves
// or creates): what the former owner's reviewers released is held again,
// because the new owner's reviewers have not decided it. A discarded entry
// stays discarded.
func holdForeignEntriesTx(tx execQueryer, remote string, orgID int64, rehold bool) error {
	names := nameIndex(tx)
	owner := instanceOwnerID(tx)
	trusted := map[int64]bool{}
	trust := func(account int64) bool {
		if account <= 0 {
			return false
		}
		t, ok := trusted[account]
		if !ok {
			k := orgViewerKindTx(tx, orgID, account)
			t = k == OrgViewerOwner || k == OrgViewerMember
			if !t {
				var n int
				_ = tx.QueryRow(`SELECT COUNT(*) FROM project_members m JOIN projects p ON p.id=m.project_id
					WHERE p.remote=? AND m.account_id=? AND m.role IN ('owner','lead','member')`, remote, account).Scan(&n)
				t = n > 0
			}
			trusted[account] = t
		}
		return t
	}
	type row struct {
		kind   string
		id     int64
		author int64
	}
	// Read first, decide after: no query runs while rows are open (a store
	// with one connection would wait on itself).
	var all []row
	for _, src := range heldSources {
		rows, err := tx.Query(src.query, remote)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, author int64
			var person string
			if err := rows.Scan(&id, &author, &person); err != nil {
				rows.Close()
				return err
			}
			if author < 0 {
				author = owner
				if key := strings.ToLower(strings.TrimSpace(person)); key != "" {
					if a, known := names[key]; known {
						author = a
					}
				}
				if author < 0 {
					author = 0 // a name two accounts had: nobody's
				}
			}
			all = append(all, row{src.kind, id, author})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	var hold []row
	for _, r := range all {
		if !trust(r.author) {
			hold = append(hold, r)
		}
	}
	at := now()
	for _, h := range hold {
		q := `INSERT OR IGNORE INTO held_entries(kind, entry_id, project, author_id, held_at) VALUES(?,?,?,?,?)`
		if rehold {
			q = `INSERT INTO held_entries(kind, entry_id, project, author_id, held_at) VALUES(?,?,?,?,?)
				ON CONFLICT(kind, entry_id) DO UPDATE SET state='held', project=excluded.project, held_at=excluded.held_at, decided_by=0, decided_at=''
				WHERE held_entries.state='released'`
		}
		if _, err := tx.Exec(q, h.kind, h.id, remote, h.author, at); err != nil {
			return err
		}
	}
	return nil
}

// heldIndex is every entry still held back (held or discarded): kind -> entry
// -> author.
func (s *Store) heldIndex() map[string]map[int64]int64 {
	if s.reader != nil {
		return s.reader.heldIndex()
	}
	out := map[string]map[int64]int64{}
	rows, err := s.db.Query(`SELECT kind, entry_id, author_id FROM held_entries WHERE state<>'released'`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var id, author int64
		if rows.Scan(&kind, &id, &author) == nil {
			if out[kind] == nil {
				out[kind] = map[int64]int64{}
			}
			out[kind][id] = author
		}
	}
	return out
}

// HeldEntry is one entry in the list of held entries: what it is and who wrote
// it, its title so a reviewer can decide, and its state.
type HeldEntry struct {
	Kind    string `json:"kind"`
	ID      int64  `json:"id"`
	Project string `json:"project"`
	Title   string `json:"title"`
	Author  string `json:"author"`
	State   string `json:"state"`
	HeldAt  string `json:"held_at"`
}

var heldTitles = map[string]string{
	HeldKnowledge: `SELECT title FROM knowledge WHERE id=?`,
	HeldRequest:   `SELECT title FROM requests WHERE id=?`,
	HeldPain:      `SELECT title FROM pains WHERE id=?`,
	HeldIdea:      `SELECT title FROM ideas WHERE id=?`,
	HeldDocument:  `SELECT title FROM documents WHERE id=?`,
}

// HeldEntries lists the entries of a project that are held back (state held,
// or with all set, also discarded ones). The caller checks the review right
// (ProjectAccess.HeldEntries).
func (s *Store) HeldEntries(project string, all bool) ([]HeldEntry, error) {
	if s.reader != nil {
		return s.reader.HeldEntries(project, all)
	}
	q := `SELECT h.kind, h.entry_id, h.project, COALESCE(p.name,''), h.state, h.held_at FROM held_entries h
		LEFT JOIN persons p ON p.id=h.author_id WHERE h.project=? AND h.state='held'`
	if all {
		q = strings.Replace(q, `h.state='held'`, `h.state<>'released'`, 1)
	}
	rows, err := s.db.Query(q+` ORDER BY h.held_at, h.kind, h.entry_id LIMIT ?`, project, maxListRows)
	if err != nil {
		return nil, err
	}
	var out []HeldEntry
	for rows.Next() {
		var h HeldEntry
		if err := rows.Scan(&h.Kind, &h.ID, &h.Project, &h.Author, &h.State, &h.HeldAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		_ = s.db.QueryRow(heldTitles[out[i].Kind], out[i].ID).Scan(&out[i].Title)
	}
	return out, nil
}

// DecideHeldEntry releases (release) or discards a held entry. Only who holds
// the review right in its project may (owner, lead or can_review); the
// decision is recorded on the row.
func (s *Store) DecideHeldEntry(actorPrincipal, kind string, id int64, release bool) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, kind, id, release}, func(d *Store, p []any) error {
			return d.DecideHeldEntry(p[0].(string), p[1].(string), p[2].(int64), p[3].(bool))
		})
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var project, state string
	if tx.QueryRow(`SELECT project, state FROM held_entries WHERE kind=? AND entry_id=?`, kind, id).Scan(&project, &state) != nil || state == "released" {
		return ErrHeldNotFound
	}
	if !reviewerTx(tx, project, actor) {
		return ErrNotReviewer
	}
	to := "discarded"
	if release {
		to = "released"
	}
	if _, err := tx.Exec(`UPDATE held_entries SET state=?, decided_by=?, decided_at=? WHERE kind=? AND entry_id=?`, to, actor, now(), kind, id); err != nil {
		return err
	}
	return tx.Commit()
}

// reviewerTx: the account may review in the project (owner, lead, or
// can_review).
func reviewerTx(q rowQuerier, project string, account int64) bool {
	r := projectRoleTx(q, project, account)
	return r.Role == RoleOwner || r.Role == RoleLead || r.CanReview
}

// HeldEntries lists a project's held entries for who may decide them: its
// owners, leads and reviewers. Anybody else gets the answer of an unknown
// project (members without the review right: forbidden).
func (a *ProjectAccess) HeldEntries(project string, all bool) ([]HeldEntry, error) {
	r := a.Role(project)
	switch {
	case r.Role == RoleOwner || r.Role == RoleLead || r.CanReview:
		return a.st.HeldEntries(project, all)
	case RoleRank(r.Role) == 0:
		return nil, ErrAccessNotFound
	}
	return nil, ErrAccessForbidden
}

// HeldPreview is a held entry's title and text, for the reviewer who decides
// it. It exists only for the web page of that decision: never for context,
// MCP, the mirror, search or hover cards.
type HeldPreview struct {
	HeldEntry
	Body string `json:"-"`
}

var heldTexts = map[string]string{
	HeldKnowledge: `SELECT title, body FROM knowledge WHERE id=?`,
	HeldRequest:   `SELECT title, description FROM requests WHERE id=?`,
	HeldPain:      `SELECT title, body FROM pains WHERE id=?`,
	HeldIdea:      `SELECT title, body FROM ideas WHERE id=?`,
	HeldDocument: `SELECT d.title, COALESCE((SELECT r.body FROM document_revisions r WHERE r.document_id=d.id ORDER BY r.revision DESC LIMIT 1), '')
		FROM documents d WHERE d.id=?`,
}

// PreviewHeld is the one exception to heldBack: an owner, lead or reviewer of
// the entry's project reads a held (or discarded) entry to decide it. Anybody
// else, and any entry that is not held, answers like an unknown one.
func (a *ProjectAccess) PreviewHeld(kind string, id int64) (HeldPreview, error) {
	q, ok := heldTexts[kind]
	if !ok {
		return HeldPreview{}, ErrAccessNotFound
	}
	h, ok := a.st.heldRow(kind, id)
	if !ok {
		return HeldPreview{}, ErrAccessNotFound
	}
	r := a.Role(h.Project)
	if !(r.Role == RoleOwner || r.Role == RoleLead || r.CanReview) {
		return HeldPreview{}, ErrAccessNotFound
	}
	p := HeldPreview{HeldEntry: h}
	if err := a.st.heldText(q, id, &p.Title, &p.Body); err != nil {
		return HeldPreview{}, ErrAccessNotFound
	}
	return p, nil
}

func (s *Store) heldRow(kind string, id int64) (HeldEntry, bool) {
	if s.reader != nil {
		return s.reader.heldRow(kind, id)
	}
	h := HeldEntry{Kind: kind, ID: id}
	err := s.db.QueryRow(`SELECT h.project, COALESCE(p.name,''), h.state, h.held_at FROM held_entries h LEFT JOIN persons p ON p.id=h.author_id
		WHERE h.kind=? AND h.entry_id=? AND h.state<>'released'`, kind, id).Scan(&h.Project, &h.Author, &h.State, &h.HeldAt)
	return h, err == nil
}

func (s *Store) heldText(q string, id int64, title, body *string) error {
	if s.reader != nil {
		return s.reader.heldText(q, id, title, body)
	}
	return s.db.QueryRow(q, id).Scan(title, body)
}
