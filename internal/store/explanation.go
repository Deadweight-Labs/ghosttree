package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
)

// explanationSchema is purely additive: two new tables, nothing existing is
// touched. An explanation belongs to one object (kind, id) and carries the hash
// of the text it explains, so a changed object is recognised without a trigger
// on any other table.
const explanationSchema = `
CREATE TABLE IF NOT EXISTS explanations(
  kind TEXT NOT NULL CHECK(kind IN ('knowledge','request','pain','task','question')),
  object_id INTEGER NOT NULL,
  project TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL CHECK(trim(text) != ''),
  language TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL CHECK(source IN ('agent','generated')),
  model TEXT NOT NULL DEFAULT '',
  generated_at TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  PRIMARY KEY(kind, object_id));
CREATE INDEX IF NOT EXISTS explanations_project ON explanations(project, source);
CREATE TABLE IF NOT EXISTS explain_usage(
  day TEXT PRIMARY KEY,
  calls INTEGER NOT NULL DEFAULT 0,
  failures INTEGER NOT NULL DEFAULT 0,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0);
-- A batch of explanations at the provider. It is 'submitting' from the moment
-- the items are reserved until the provider has accepted it, so that a second
-- run cannot send the same objects meanwhile. The result arrives hours later in
-- another process run, so without these rows the next run could not tell an
-- object that is being explained from one that was never sent.
CREATE TABLE IF NOT EXISTS explain_batches(
  id INTEGER PRIMARY KEY,
  provider_batch_id TEXT NOT NULL UNIQUE,
  state TEXT NOT NULL CHECK(state IN ('submitting','open','collected','failed')),
  model TEXT NOT NULL DEFAULT '',
  language TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
-- content_hash is the text as it stood when it was sent; the result is stored
-- only if the object still has it. state: pending (sent, not read yet),
-- saved, stale (object changed meanwhile), superseded (a current text exists
-- already) or failed (the entry failed; blocks a resubmission for a while).
CREATE TABLE IF NOT EXISTS explain_batch_items(
  batch_id INTEGER NOT NULL REFERENCES explain_batches(id) ON DELETE CASCADE,
  custom_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  object_id INTEGER NOT NULL,
  content_hash TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','saved','stale','superseded','failed')),
  error TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(batch_id, custom_id));
CREATE INDEX IF NOT EXISTS explain_batch_items_object ON explain_batch_items(kind, object_id, content_hash);
`

func ensureExplanations(db *sql.DB) error {
	_, err := db.Exec(explanationSchema)
	return err
}

// Explanation returns the stored explanation of an object, if there is one.
func (s *Store) Explanation(kind string, id int64) (explain.Explanation, bool, error) {
	if s.reader != nil {
		return s.reader.Explanation(kind, id)
	}
	var e explain.Explanation
	err := s.db.QueryRow(`SELECT kind, object_id, project, text, language, source, model, generated_at, content_hash
		FROM explanations WHERE kind = ? AND object_id = ?`, kind, id).
		Scan(&e.Kind, &e.ObjectID, &e.Project, &e.Text, &e.Language, &e.Source, &e.Model, &e.GeneratedAt, &e.ContentHash)
	if errors.Is(err, sql.ErrNoRows) {
		return explain.Explanation{}, false, nil
	}
	return e, err == nil, err
}

// SaveExplanation stores or replaces the explanation of an object.
func (s *Store) SaveExplanation(e explain.Explanation) error {
	if !explain.ValidKind(e.Kind) || e.ObjectID <= 0 {
		return fmt.Errorf("unknown object %s %d", e.Kind, e.ObjectID)
	}
	if e.Source != explain.SourceAgent && e.Source != explain.SourceGenerated {
		return fmt.Errorf("explanation source must be agent or generated")
	}
	if strings.TrimSpace(e.Text) == "" || e.ContentHash == "" {
		return fmt.Errorf("explanation needs a text and the hash of what it explains")
	}
	if e.GeneratedAt == "" {
		e.GeneratedAt = now()
	}
	if s.writer != nil {
		return queueWrite(s, []any{e}, func(d *Store, p []any) error { return d.SaveExplanation(p[0].(explain.Explanation)) })
	}
	_, err := s.db.Exec(`INSERT INTO explanations(kind, object_id, project, text, language, source, model, generated_at, content_hash)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(kind, object_id) DO UPDATE SET project=excluded.project, text=excluded.text, language=excluded.language,
		  source=excluded.source, model=excluded.model, generated_at=excluded.generated_at, content_hash=excluded.content_hash`,
		e.Kind, e.ObjectID, e.Project, e.Text, e.Language, e.Source, e.Model, e.GeneratedAt, e.ContentHash)
	return err
}

// SaveAgentExplanation stores what the agent wrote for an object it just
// created. The hash is taken from the object as it is stored, so the text counts
// as current until the object changes.
func (s *Store) SaveAgentExplanation(kind string, id int64, text string) error {
	text, err := explain.Clean(text)
	if err != nil {
		return err
	}
	// Only the creator of a new object sets its explanation. A replayed
	// idempotent call returns an object somebody else made; it must not replace
	// what is stored for it.
	if _, found, err := s.Explanation(kind, id); err != nil {
		return err
	} else if found {
		return nil
	}
	src, err := s.ExplainSource(kind, id)
	if err != nil {
		return err
	}
	return s.SaveExplanation(explain.Explanation{
		Kind: kind, ObjectID: id, Project: src.Project, Text: text, Source: explain.SourceAgent,
		GeneratedAt: now(), ContentHash: src.Hash,
	})
}

// ExplanationCounts says how many explanations exist by source.
func (s *Store) ExplanationCounts() (agent, generated int, err error) {
	if s.reader != nil {
		return s.reader.ExplanationCounts()
	}
	rows, err := s.db.Query(`SELECT source, COUNT(*) FROM explanations GROUP BY source`)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var src string
		var n int
		if err := rows.Scan(&src, &n); err != nil {
			return 0, 0, err
		}
		if src == explain.SourceAgent {
			agent = n
		} else {
			generated = n
		}
	}
	return agent, generated, rows.Err()
}

// ---------------------------------------------------------------------- Source

// ExplainSource reads what an explanation of the object is made from: the
// object's own text, plus project data that belongs to the same project and is
// readable by its members (titles of the requests it names, code paths). Never
// a session transcript. Project is empty for objects that belong to no
// project; nothing is generated for those, but the hash still tells whether an
// agent-written explanation matches the current text.
func (s *Store) ExplainSource(kind string, id int64) (explain.Source, error) {
	if s.reader != nil {
		return s.reader.ExplainSource(kind, id)
	}
	var src explain.Source
	var err error
	switch kind {
	case explain.KindKnowledge:
		src, err = s.knowledgeSource(id)
	case explain.KindRequest:
		src, err = s.requestSource(id)
	case explain.KindPain:
		src, err = s.painSource(id)
	case explain.KindTask:
		src, err = s.taskSource(id)
	case explain.KindQuestion:
		src, err = s.questionSource(id)
	default:
		return explain.Source{}, explain.ErrNotExplainable
	}
	if errors.Is(err, sql.ErrNoRows) {
		return explain.Source{}, explain.ErrNotExplainable
	}
	src.Kind, src.ID = kind, id
	return src, err
}

var explainOriginSession = regexp.MustCompile(`^session:(\d+)`)

// originHeld says whether an object derived from a session ("session:N") still
// belongs to the owner of that session: the session is private and access
// control is on. Same rule as ProjectAccess.heldOrigin, without a viewer,
// because an explanation is shown to every reader of the object and must not
// contain what only the session owner may see.
func (s *Store) originHeld(ref string) bool {
	if ref == "" || !s.accessCfg().enforce.Load() {
		return false
	}
	m := explainOriginSession.FindStringSubmatch(ref)
	if m == nil {
		return false
	}
	id, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return false
	}
	sess, err := s.SessionByID(id)
	if err != nil {
		return false
	}
	return !sess.Shared && sess.Visibility != VisGuests
}

// requestHeld: distilled request nobody has taken over yet (see heldCol).
func (s *Store) requestHeld(origin, person, sessionRef string) bool {
	return origin == "distilled" && person == "" && s.originHeld(sessionRef)
}

// knowledgeHeld: distilled knowledge still in quarantine (see knowledgeHeldRef).
func (s *Store) knowledgeHeld(origin, confidence, sessionRef string) bool {
	return origin == "distilled" && confidence == "quarantined" && s.originHeld(sessionRef)
}

// readableRequestTitle returns the title of a request of the project that every
// reader of the project may see. Held and machine-bound requests are left out: the explanation is
// stored once and shown to all readers of the explained object.
func (s *Store) readableRequestTitle(id int64, project string) (string, bool) {
	var title, origin, person, ref string
	if s.db.QueryRow(`SELECT title, origin, person, session_ref FROM requests WHERE id = ? AND project = ? AND machine = ''`, id, project).
		Scan(&title, &origin, &person, &ref) != nil {
		return "", false
	}
	if s.requestHeld(origin, person, ref) {
		return "", false
	}
	return title, true
}

// referenceContext names, in context lines, what a text points at. Only
// requests of the same project that are not held are resolved, so a number
// cannot be used to read another project's or a hidden request's title.
func (s *Store) referenceContext(project, text string) []string {
	refs := explain.References(text)
	var out []string
	if project != "" {
		for _, n := range refs.Requests {
			if title, ok := s.readableRequestTitle(n, project); ok {
				out = append(out, fmt.Sprintf("Request REQ-%d: %s", n, title))
			}
		}
	}
	for _, p := range refs.Paths {
		out = append(out, "Code: "+p)
	}
	for _, pr := range refs.PRs {
		out = append(out, fmt.Sprintf("Pull request #%d", pr.Number))
	}
	return out
}

func (s *Store) knowledgeSource(id int64) (explain.Source, error) {
	var typ, title, body, project, machine, origin, confidence, sessionRef string
	if err := s.db.QueryRow(`SELECT type, title, body, project, machine, origin, confidence, session_ref FROM knowledge WHERE id = ?`, id).
		Scan(&typ, &title, &body, &project, &machine, &origin, &confidence, &sessionRef); err != nil {
		return explain.Source{}, err
	}
	hash := explain.HashOf(explain.KindKnowledge, project, typ, title, body)
	// Machine-axis entries are readable by the machine's owner only, held ones by
	// the owner of the private session they came from: no project text for the
	// model, no shared explanation.
	if machine != "" || s.knowledgeHeld(origin, confidence, sessionRef) {
		return explain.Source{Label: typ, Title: title, Body: body, Hash: hash}, nil
	}
	return explain.Source{Project: project, Label: typ, Title: title, Body: body,
		Context: s.referenceContext(project, title+"\n"+body), Hash: hash}, nil
}

func (s *Store) requestSource(id int64) (explain.Source, error) {
	var typ, title, desc, project, machine, origin, person, sessionRef string
	if err := s.db.QueryRow(`SELECT type, title, description, project, machine, origin, person, session_ref FROM requests WHERE id = ?`, id).
		Scan(&typ, &title, &desc, &project, &machine, &origin, &person, &sessionRef); err != nil {
		return explain.Source{}, err
	}
	hash := explain.HashOf(explain.KindRequest, project, typ, title, desc)
	if machine != "" || s.requestHeld(origin, person, sessionRef) {
		return explain.Source{Label: typ, Title: title, Body: desc, Hash: hash}, nil
	}
	var ctx []string
	rows, err := s.db.Query(`SELECT description FROM request_criteria WHERE request_id = ? ORDER BY number LIMIT 8`, id)
	if err == nil {
		for rows.Next() {
			var c string
			if rows.Scan(&c) == nil {
				ctx = append(ctx, "Acceptance criterion: "+c)
			}
		}
		rows.Close()
	}
	ctx = append(ctx, s.referenceContext(project, desc)...)
	return explain.Source{Project: project, Label: typ, Title: title, Body: desc, Context: ctx, Hash: hash}, nil
}

func (s *Store) painSource(id int64) (explain.Source, error) {
	var title, body, project, state, remaining string
	if err := s.db.QueryRow(`SELECT title, body, project, state, remaining FROM pains WHERE id = ?`, id).Scan(&title, &body, &project, &state, &remaining); err != nil {
		return explain.Source{}, err
	}
	ctx := []string{"State: " + state}
	if remaining != "" {
		ctx = append(ctx, "Still unsolved: "+remaining)
	}
	rows, err := s.db.Query(`SELECT r.id, r.title, r.origin, r.person, r.session_ref FROM entity_links l JOIN requests r ON r.id = l.dst_id
		WHERE l.src_kind = 'pain' AND l.src_id = ? AND l.dst_kind = 'request' AND r.project = ? AND r.machine = '' LIMIT 10`, id, project)
	if err == nil {
		shown := 0
		for rows.Next() {
			var n int64
			var t, origin, person, ref string
			if rows.Scan(&n, &t, &origin, &person, &ref) == nil && shown < 3 && !s.requestHeld(origin, person, ref) {
				ctx = append(ctx, fmt.Sprintf("Motivates request REQ-%d: %s", n, t))
				shown++
			}
		}
		rows.Close()
	}
	ctx = append(ctx, s.referenceContext(project, title+"\n"+body)...)
	return explain.Source{Project: project, Label: "pain", Title: title, Body: body, Context: ctx,
		Hash: explain.HashOf(explain.KindPain, project, title, body, state, remaining)}, nil
}

func (s *Store) taskSource(id int64) (explain.Source, error) {
	var kind, title, desc, location, hint, project, state string
	var requestID int64
	if err := s.db.QueryRow(`SELECT kind, title, description, location, check_hint, project, request_id, state FROM human_tasks WHERE id = ?`, id).
		Scan(&kind, &title, &desc, &location, &hint, &project, &requestID, &state); err != nil {
		return explain.Source{}, err
	}
	body := desc
	if location != "" {
		body += "\nWhere it should end up: " + location
	}
	if hint != "" {
		body += "\nHow it is checked: " + hint
	}
	var ctx []string
	if reqTitle, ok := s.readableRequestTitle(requestID, project); ok {
		ctx = append(ctx, fmt.Sprintf("Hangs on request REQ-%d: %s", requestID, reqTitle))
	}
	return explain.Source{Project: project, Label: "task for a person (" + kind + ")", Title: title, Body: strings.TrimSpace(body), Context: ctx,
		Hash: explain.HashOf(explain.KindTask, project, state, kind, title, desc, location, hint)}, nil
}

func (s *Store) questionSource(id int64) (explain.Source, error) {
	var destKind, destID, intent, body string
	if err := s.db.QueryRow(`SELECT destination_kind, destination_id, intent, body FROM coord_messages WHERE id = ?`, id).
		Scan(&destKind, &destID, &intent, &body); err != nil {
		return explain.Source{}, err
	}
	// Only project rooms are project data. Direct and group rooms are private
	// conversations and machine rooms belong to no project.
	project := ""
	switch destKind {
	case DestinationRoom:
		if strings.HasPrefix(destID, "project:") {
			project = strings.TrimPrefix(destID, "project:")
		}
	case DestinationDiscussion:
		_ = s.db.QueryRow(`SELECT project FROM threads WHERE id = ?`, destID).Scan(&project)
	}
	return explain.Source{Project: project, Label: "question to the people of the project", Title: "", Body: body,
		Context: s.referenceContext(project, body), Hash: explain.HashOf(explain.KindQuestion, project, intent, body)}, nil
}

// --------------------------------------------------------------------- Queue

// ExplainCandidates lists objects that belong to a project, in the order they
// should be explained: what waits for review first, then open requests, active
// pains, open tasks, open questions and finally the rest of the knowledge.
// Whether an explanation exists is not looked at here.
func (s *Store) ExplainCandidates(scan int) ([]explain.Candidate, error) {
	if s.reader != nil {
		return s.reader.ExplainCandidates(scan)
	}
	type part struct {
		kind, query string
		held        func(origin, a, b string) bool
	}
	heldKnowledge := func(origin, confidence, ref string) bool { return s.knowledgeHeld(origin, confidence, ref) }
	heldRequest := func(origin, person, ref string) bool { return s.requestHeld(origin, person, ref) }
	parts := []part{
		{explain.KindKnowledge, `SELECT id, origin, confidence, session_ref FROM knowledge WHERE project != '' AND machine = '' AND ((status = 'active' AND confidence IN ('quarantined','staged')) OR status = 'stale') ORDER BY created_at DESC, id DESC`, heldKnowledge},
		{explain.KindRequest, `SELECT id, origin, person, session_ref FROM requests WHERE project != '' AND machine = '' AND state = 'open' ORDER BY created_at DESC, id DESC`, heldRequest},
		{explain.KindPain, `SELECT id, '', '', '' FROM pains WHERE project != '' AND state IN ('active','mitigated') ORDER BY created_at DESC, id DESC`, nil},
		{explain.KindTask, `SELECT id, '', '', '' FROM human_tasks WHERE state IN ('open','done') ORDER BY created_at DESC, id DESC`, nil},
		{explain.KindQuestion, `SELECT DISTINCT m.id, '', '', '' FROM coord_attention a JOIN coord_messages m ON m.id = a.message_id
			WHERE a.reason = 'question' AND a.state = 'open' ORDER BY m.id DESC`, nil},
		{explain.KindKnowledge, `SELECT id, origin, confidence, session_ref FROM knowledge WHERE project != '' AND machine = '' AND status = 'active' AND confidence IN ('trusted','verified') ORDER BY created_at DESC, id DESC`, heldKnowledge},
	}
	var out []explain.Candidate
	for _, p := range parts {
		// Held and machine-private objects are dropped after the query, so a page
		// can come back short; read on until the part is exhausted or enough is
		// found. The rows are closed before the held check, which reads again.
		for offset := 0; len(out) < scan; offset += scan {
			type row struct {
				id      int64
				a, b, c string
			}
			rows, err := s.db.Query(p.query+` LIMIT ? OFFSET ?`, scan, offset)
			if err != nil {
				return nil, err
			}
			var page []row
			for rows.Next() {
				var r row
				if err := rows.Scan(&r.id, &r.a, &r.b, &r.c); err != nil {
					rows.Close()
					return nil, err
				}
				page = append(page, r)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
			for _, r := range page {
				if p.held != nil && p.held(r.a, r.b, r.c) {
					continue
				}
				if len(out) < scan {
					out = append(out, explain.Candidate{Kind: p.kind, ID: r.id})
				}
			}
			if len(page) < scan {
				break
			}
		}
		if len(out) >= scan {
			break
		}
	}
	return out, nil
}

var _ explain.Store = (*Store)(nil)
