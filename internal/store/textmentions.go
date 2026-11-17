package store

import (
	"database/sql"
	"errors"
	"strings"

	actordomain "github.com/Deadweight-Labs/ghosttree/internal/actor"
)

// Mentions in texts outside the rooms (REQ-677): a comment, the description of
// an issue, the text of a pain. An @name in such a text is resolved the way a
// room resolves it (the same handles, mentionCandidatesTx), and each person it
// reaches gets a row in text_mentions that the inbox shows under "what was said
// to you". A name that reaches nobody (no such person in the project, no access,
// ambiguous) is handed back to the sender, who is told right after writing.

// Kinds of text a mention stands in.
const (
	MentionInComment = "comment"
	MentionInRequest = "request"
	MentionInPain    = "pain"
)

const textMentionSchema = `
CREATE TABLE IF NOT EXISTS text_mentions(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project TEXT NOT NULL,
  source_kind TEXT NOT NULL CHECK(source_kind IN ('comment','request','pain')),
  source_id INTEGER NOT NULL,
  object_kind TEXT NOT NULL,
  object_id INTEGER NOT NULL,
  account_id INTEGER NOT NULL,
  author TEXT NOT NULL DEFAULT '',
  author_account_id INTEGER NOT NULL DEFAULT 0,
  snippet TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  read_at TEXT NOT NULL DEFAULT '',
  UNIQUE(source_kind, source_id, account_id)
);
CREATE INDEX IF NOT EXISTS text_mentions_account ON text_mentions(account_id, read_at);`

func ensureTextMentions(db *sql.DB) error {
	_, err := db.Exec(textMentionSchema)
	return err
}

// TextMention is one mention of the viewer in a text, as the inbox lists it.
type TextMention struct {
	ID         int64  `json:"id"`
	Project    string `json:"project"`
	SourceKind string `json:"source_kind"`
	SourceID   int64  `json:"source_id"`
	ObjectKind string `json:"object_kind"`
	ObjectID   int64  `json:"object_id"`
	Author     string `json:"author"`
	AuthorID   int64  `json:"-"`
	Snippet    string `json:"snippet"`
	CreatedAt  string `json:"created_at"`
}

// textMentionSource names the text a recording is about.
type textMentionSource struct {
	Project    string
	SourceKind string
	SourceID   int64
	ObjectKind string
	ObjectID   int64
	// Author is the writing account and its name.
	AuthorAccount int64
	Author        string
}

// recordTextMentions resolves the @names of body and stores a row for each
// person they reach. It returns the typed names that reached nobody. A text
// edited later records only the people who were not reached before.
func (s *Store) recordTextMentions(src textMentionSource, body string) ([]string, error) {
	if len(textMentionTokens(body)) == 0 || src.Project == "" {
		return nil, nil
	}
	if s.writer != nil {
		return queueValue(s, []any{src, body}, func(d *Store, p []any) ([]string, error) {
			return d.recordTextMentions(p[0].(textMentionSource), p[1].(string))
		})
	}
	reached, unreached, err := s.resolveTextMentions(src.Project, src.AuthorAccount, src.Author, body)
	if err != nil {
		return nil, err
	}
	var stillUnreached []string
	stillUnreached = append(stillUnreached, unreached...)
	// Rights are read before the write transaction opens: the store may have one connection.
	var allowed []int64
	for _, account := range reached {
		if account == src.AuthorAccount {
			continue
		}
		// Whoever cannot read the text it stands in is not told; the sender hears so.
		if !s.mayReadMentionTarget(account, src) {
			stillUnreached = append(stillUnreached, s.personName(account))
			continue
		}
		allowed = append(allowed, account)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	at := now()
	snippet := oneLine(body, 220)
	for _, account := range allowed {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO text_mentions(project,source_kind,source_id,object_kind,object_id,account_id,author,author_account_id,snippet,created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, src.Project, src.SourceKind, src.SourceID, src.ObjectKind, src.ObjectID, account, src.Author, src.AuthorAccount, snippet, at); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dropEmpty(stillUnreached), nil
}

// personName is the name of an account, "" when there is none.
func (s *Store) personName(account int64) string {
	var name string
	_ = s.db.QueryRow(`SELECT name FROM persons WHERE id=?`, account).Scan(&name)
	return name
}

func dropEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// resolveTextMentions fits the @names of body to the people of the project:
// the accounts reached (an agent reaches the person it works for) and the names
// that reached nobody.
func (s *Store) resolveTextMentions(project string, authorAccount int64, author, body string) (reached []int64, unreached []string, err error) {
	tokens := textMentionTokens(body)
	if len(tokens) == 0 {
		return nil, nil, nil
	}
	reader := s
	if s.reader != nil {
		reader = s.reader
	}
	tx, err := reader.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	actor := principalOfID(authorAccount)
	access := reader.CoordinationFor(Principal{ID: actor, Label: author}, "")
	candidates, err := access.mentionCandidatesTx(tx, actor, RoomKeyForProject(project))
	if err != nil {
		return nil, nil, err
	}
	seen := map[int64]bool{}
	for _, token := range tokens {
		if IsBroadcastToken(token) {
			// @agents belongs to the rooms: a text does not wake every agent.
			unreached = append(unreached, token)
			continue
		}
		hit, _ := resolveMentionToken(candidates, token)
		if hit == nil {
			unreached = append(unreached, token)
			continue
		}
		account := int64(0)
		if id, perr := parsePersonPrincipalID(hit.id); perr == nil {
			account = id
		} else if owner := agentOwnerTx(tx, strings.TrimPrefix(hit.id, "agent:")); owner != "" {
			account, _ = parsePersonPrincipalID(owner)
		}
		if account == 0 {
			unreached = append(unreached, token)
			continue
		}
		if !seen[account] {
			seen[account] = true
			reached = append(reached, account)
		}
	}
	return reached, unreached, nil
}

func agentOwnerTx(tx *sql.Tx, external string) string {
	var owner string
	if err := tx.QueryRow(`SELECT COALESCE(principal_id,'') FROM coord_agents WHERE external_id=?`, external).Scan(&owner); err != nil {
		return ""
	}
	return owner
}

// mayReadMentionTarget says whether the mentioned account may read the object
// the text belongs to, by the rights it would have opening the page.
func (s *Store) mayReadMentionTarget(account int64, src textMentionSource) bool {
	reader := s
	if s.reader != nil {
		reader = s.reader
	}
	var name string
	_ = reader.db.QueryRow(`SELECT name FROM persons WHERE id=?`, account).Scan(&name)
	pa := reader.Access(Principal{ID: principalOfID(account), Label: name})
	return pa.canReadTextTarget(src.Project, src.SourceKind, src.ObjectKind, src.ObjectID)
}

// canReadTextTarget asks the right of reading the one object the text belongs to
// (commentTarget, the same rule as opening its page), not just the project: an
// entry held back from a viewer stays unknown to them, mentioned or not.
func (a *ProjectAccess) canReadTextTarget(_, _, objectKind string, objectID int64) bool {
	_, err := a.commentTarget(objectKind, objectID)
	return err == nil
}

// UnreachedTextMentions are the typed @names of a text that would reach nobody
// in project, for the hint after writing. The author is the viewer. Only names
// of a text are returned, never who is or is not in the project.
func (a *ProjectAccess) UnreachedTextMentions(project, body string) []string {
	src := textMentionSource{Project: project, AuthorAccount: a.account, Author: a.principal.Label}
	_, unreached, err := a.st.resolveTextMentions(project, src.AuthorAccount, src.Author, body)
	if err != nil {
		return nil
	}
	return unreached
}

// TextMentions lists what the viewer was told in texts and has not marked read,
// newest first, only where the viewer may still read the text.
func (a *ProjectAccess) TextMentions(project string) ([]TextMention, error) {
	reader := a.st
	if reader.reader != nil {
		reader = reader.reader
	}
	query := `SELECT id,project,source_kind,source_id,object_kind,object_id,author,author_account_id,snippet,created_at FROM text_mentions WHERE account_id=? AND read_at=''`
	args := []any{a.account}
	if project != "" {
		query += ` AND project=?`
		args = append(args, project)
	}
	rows, err := reader.db.Query(query+` ORDER BY created_at DESC, id DESC LIMIT 100`, args...)
	if err != nil {
		return nil, err
	}
	var all []TextMention
	for rows.Next() {
		var m TextMention
		if err := rows.Scan(&m.ID, &m.Project, &m.SourceKind, &m.SourceID, &m.ObjectKind, &m.ObjectID, &m.Author, &m.AuthorID, &m.Snippet, &m.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	// The rights are asked after the rows are read: one connection must be enough.
	rows.Close()
	var out []TextMention
	for _, m := range all {
		if a.canReadTextTarget(m.Project, m.SourceKind, m.ObjectKind, m.ObjectID) {
			out = append(out, m)
		}
	}
	return out, nil
}

// ErrTextMentionNotFound: no such mention among the viewer's own.
var ErrTextMentionNotFound = errors.New("text mention not found")

// MarkTextMentionRead takes one of the viewer's own mentions out of the inbox.
func (a *ProjectAccess) MarkTextMentionRead(id int64) error {
	if a.st.writer != nil {
		return queueWrite(a.st, []any{id, a.account}, func(d *Store, p []any) error {
			return d.markTextMentionRead(p[0].(int64), p[1].(int64))
		})
	}
	return a.st.markTextMentionRead(id, a.account)
}

func (s *Store) markTextMentionRead(id, account int64) error {
	res, err := s.db.Exec(`UPDATE text_mentions SET read_at=? WHERE id=? AND account_id=? AND read_at=''`, now(), id, account)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTextMentionNotFound
	}
	return nil
}

// MentionOption is one person or agent a text of the project can address, as
// the editor offers it after @: the Handle it writes, the Label it shows.
type MentionOption struct {
	ID       string `json:"id"`
	Handle   string `json:"handle"`
	Label    string `json:"label"`
	Provider string `json:"provider,omitempty"`
	Agent    bool   `json:"agent,omitempty"`
}

// mentionDirectory is who an @name in a text of project can mean for the
// viewer. Without the right to read the project's room it is empty: the member
// list is not theirs to see. The viewer is in the list: a pill names them too.
func (a *ProjectAccess) mentionDirectory(project string) []mentionCandidate {
	if project == "" || !a.CanReadProjectRoom(project) {
		return nil
	}
	reader := a.st
	if reader.reader != nil {
		reader = reader.reader
	}
	tx, err := reader.db.Begin()
	if err != nil {
		return nil
	}
	defer tx.Rollback()
	// An actor nobody is excludes nobody, so the viewer's own name resolves too.
	const nobody = "person:0"
	access := reader.CoordinationFor(a.principal, "")
	all, err := access.mentionCandidatesTx(tx, nobody, RoomKeyForProject(project))
	if err != nil {
		return nil
	}
	out := all[:0:0]
	for _, c := range all {
		if c.id != nobody {
			out = append(out, c)
		}
	}
	return out
}

// shortestUniqueHandle is the shortest handle of c that fits c alone, so that the
// editor writes "@mara" and not the whole name; a handle with a colon (person:3)
// is the last resort. "" when none fits.
func shortestUniqueHandle(all []mentionCandidate, c mentionCandidate) string {
	best := ""
	for _, h := range c.handles {
		if hit, _ := resolveMentionToken(all, h); hit == nil || hit.id != c.id {
			continue
		}
		colon := strings.Contains(h, ":")
		bestColon := strings.Contains(best, ":")
		if best == "" || (bestColon && !colon) || (colon == bestColon && len(h) < len(best)) {
			best = h
		}
	}
	return best
}

// MentionOptions are the names the editor offers after @ in a text of the
// project: each with the shortest handle that fits them alone.
func (a *ProjectAccess) MentionOptions(project string) []MentionOption {
	all := a.mentionDirectory(project)
	var out []MentionOption
	for _, c := range all {
		handle := shortestUniqueHandle(all, c)
		if handle == "" {
			continue
		}
		o := MentionOption{ID: c.id, Handle: handle, Label: c.label, Agent: !strings.HasPrefix(c.id, "person:")}
		if o.Agent {
			o.Provider = toolWord(c.provider)
			if strings.Contains(o.Label, c.id) && c.owner != "" {
				// An agent that gave no name is "Claude · Lena", not its identifier.
				o.Label = actorFamilyWord(c.provider) + " · " + c.owner
			}
		}
		out = append(out, o)
	}
	return out
}

// MentionName is what a handle in a text stands for.
type MentionName struct {
	Name  string `json:"name"`
	Agent bool   `json:"agent,omitempty"`
}

// MentionNames resolves the handles written in texts of a project to the full
// names, for the pill. A handle that fits nobody, or several, is left out.
func (a *ProjectAccess) MentionNames(project string, handles []string) map[string]MentionName {
	all := a.mentionDirectory(project)
	out := map[string]MentionName{}
	for _, h := range handles {
		token := normalizeHandle(h)
		if token == "" || IsBroadcastToken(token) {
			continue
		}
		if hit, _ := resolveMentionToken(all, token); hit != nil {
			out[h] = MentionName{Name: hit.label, Agent: !strings.HasPrefix(hit.id, "person:")}
		}
	}
	return out
}

// actorFamilyWord is the tool as a person names it: Claude, Codex.
func actorFamilyWord(provider string) string {
	return actordomain.Family(provider)
}
