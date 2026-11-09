package store

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	actordomain "github.com/Deadweight-Labs/ghosttree/internal/actor"
	"modernc.org/sqlite"
)

// ActorBackfillOptions steer the backfill of actors (REQ-620).
type ActorBackfillOptions struct {
	// DryRun counts and writes nothing.
	DryRun bool
	// Batch is the number of entries per transaction (default 500): a long run
	// on a large database never holds the write lock for long.
	Batch int
	// Models also derives the model history of every session an actor points
	// to and that has none yet, from the stored transcript lines.
	Models bool
	// Kinds limits the run to these kinds; empty means all.
	Kinds []string
}

// ActorBackfillKind is the result for one kind.
type ActorBackfillKind struct {
	Kind string `json:"kind"`
	// Candidates are entries without an actor.
	Candidates int `json:"candidates"`
	// Human and Agent were filled (would be, in a dry run); WithSession are the
	// agents among them whose session is proven.
	Human       int `json:"human"`
	Agent       int `json:"agent"`
	WithSession int `json:"with_session"`
	// Unknown stayed without actor: no proof.
	Unknown int `json:"unknown"`
	// Recent were left alone: younger than a few minutes, the live stamp may
	// still be on its way.
	Recent int `json:"recent"`
}

// ActorBackfillReport is the whole run.
type ActorBackfillReport struct {
	DryRun bool                `json:"dry_run"`
	Kinds  []ActorBackfillKind `json:"kinds"`
	// Sessions is the number of sessions whose model history was derived (would
	// be), Segments the stretches found in them.
	Sessions int `json:"sessions"`
	Segments int `json:"segments"`
}

// actorCandidate is one entry without actor and everything that may prove it.
type actorCandidate struct {
	id      int64
	at      string
	account int64
	// hint is what the entry itself says: agent (requests.origin,
	// knowledge.origin), human or agent where the server set it by route (coord
	// author_kind, decision by_agent), "skip" for entries no session wrote
	// (distilled), empty when it says nothing. origin "human" on requests and
	// knowledge is no proof: a token could write it before REQ-620.
	hint    string
	harness string
	// sessionRow is a numeric session id, sessionRef an external id or
	// "session:N", originRef the session of the origin context.
	sessionRow int64
	sessionRef string
	originRef  string
	via        string
}

// actorBackfillSource reads candidates of one kind. The query returns, in this
// order: id, time, writer account, hint, harness, numeric session, session
// reference, origin session, origin way, for entries after the given id that
// have no actor yet, ordered by id.
type actorBackfillSource struct {
	kind  string
	query string
	// tool proves a session for pains and ideas without one: a session of the
	// writer that called this tool (MCP name or CLI command) within two minutes
	// of the entry, by the times of its transcript lines (sessionCalledTool).
	tool *toolProof
}

// toolProof names how a session creates an entry of a kind: tool names, CLI
// commands (the start of a shell command), and the prefix of the identifier
// the answer carries (PAIN-12).
type toolProof struct {
	tools    []string
	commands []string
	prefix   string
}

const personAccountExpr = `(SELECT CASE WHEN COUNT(*)=1 THEN MIN(p.id) ELSE 0 END FROM persons p WHERE trim(%s)<>'' AND lower(trim(p.name))=lower(trim(%s)))`

func personAccount(col string) string { return fmt.Sprintf(personAccountExpr, col, col) }

var actorBackfillSources = []actorBackfillSource{
	{kind: actordomain.Request, query: `SELECT r.id, r.created_at, COALESCE((SELECT a.account_id FROM request_authors a WHERE a.request_id=r.id),0),
		CASE r.origin WHEN 'agent' THEN 'agent' WHEN 'distilled' THEN 'skip' ELSE '' END, '', 0, r.session_ref,
		COALESCE((SELECT o.session_ref FROM request_activity t JOIN origin_contexts o ON o.id=t.origin_id WHERE t.request_id=r.id AND t.kind='request.created' ORDER BY t.id LIMIT 1),''),
		COALESCE((SELECT o.via FROM request_activity t JOIN origin_contexts o ON o.id=t.origin_id WHERE t.request_id=r.id AND t.kind='request.created' ORDER BY t.id LIMIT 1),'')
		FROM requests r WHERE r.id>? AND NOT EXISTS (SELECT 1 FROM entry_actors e WHERE e.kind='request' AND e.entry_id=r.id) ORDER BY r.id LIMIT ?`},
	{kind: actordomain.Knowledge, query: `SELECT k.id, k.created_at, COALESCE((SELECT a.account_id FROM knowledge_authors a WHERE a.knowledge_id=k.id),0),
		CASE k.origin WHEN 'agent' THEN 'agent' WHEN 'distilled' THEN 'skip' ELSE '' END, k.harness, 0, k.session_ref,
		COALESCE(o.session_ref,''), COALESCE(o.via,'')
		FROM knowledge k LEFT JOIN origin_contexts o ON o.id=k.origin_id
		WHERE k.id>? AND NOT EXISTS (SELECT 1 FROM entry_actors e WHERE e.kind='knowledge' AND e.entry_id=k.id) ORDER BY k.id LIMIT ?`},
	{kind: actordomain.Pain, tool: &toolProof{tools: []string{"pain_record"}, commands: []string{"ctx pain add"}, prefix: "PAIN-"}, query: `SELECT p.id, p.created_at, COALESCE((SELECT a.account_id FROM pain_authors a WHERE a.pain_id=p.id),0),
		'', '', COALESCE(ev.session_id,0), p.session_ref, COALESCE(o.session_ref,''), COALESCE(o.via,'')
		FROM pains p LEFT JOIN pain_events ev ON ev.id=(SELECT MIN(x.id) FROM pain_events x WHERE x.pain_id=p.id AND x.kind='created')
		LEFT JOIN origin_contexts o ON o.id=ev.origin_id
		WHERE p.id>? AND NOT EXISTS (SELECT 1 FROM entry_actors e WHERE e.kind='pain' AND e.entry_id=p.id) ORDER BY p.id LIMIT ?`},
	{kind: actordomain.Idea, tool: &toolProof{tools: []string{"idea_record"}, commands: []string{"ctx idea add"}, prefix: "IDEA-"}, query: `SELECT i.id, i.created_at, COALESCE((SELECT a.account_id FROM idea_authors a WHERE a.idea_id=i.id),0),
		'', '', 0, i.session_ref, COALESCE(o.session_ref,''), COALESCE(o.via,'')
		FROM ideas i LEFT JOIN origin_contexts o ON o.id=i.origin_id
		WHERE i.id>? AND NOT EXISTS (SELECT 1 FROM entry_actors e WHERE e.kind='idea' AND e.entry_id=i.id) ORDER BY i.id LIMIT ?`},
	{kind: actordomain.Criterion, query: `SELECT c.id, COALESCE(ev.created_at, c.updated_at), ` + personAccount("COALESCE(ev.person,'')") + `,
		'', '', 0, '', COALESCE(o.session_ref,''), COALESCE(o.via,'')
		FROM request_criteria c LEFT JOIN request_evidence ev ON ev.id=(SELECT MAX(x.id) FROM request_evidence x WHERE x.criterion_id=c.id)
		LEFT JOIN origin_contexts o ON o.id=ev.origin_id
		WHERE c.state='met' AND c.id>? AND NOT EXISTS (SELECT 1 FROM entry_actors e WHERE e.kind='criterion' AND e.entry_id=c.id) ORDER BY c.id LIMIT ?`},
	{kind: actordomain.RequestDone, query: `SELECT r.id, COALESCE(t.created_at, r.updated_at), ` + personAccount("COALESCE(t.person,'')") + `,
		'', '', COALESCE(t.session_id,0), '', COALESCE(o.session_ref,''), COALESCE(o.via,'')
		FROM requests r LEFT JOIN request_activity t ON t.id=(SELECT MAX(x.id) FROM request_activity x WHERE x.request_id=r.id AND x.kind='request.done')
		LEFT JOIN origin_contexts o ON o.id=t.origin_id
		WHERE r.state='done' AND r.id>? AND NOT EXISTS (SELECT 1 FROM entry_actors e WHERE e.kind='request_done' AND e.entry_id=r.id) ORDER BY r.id LIMIT ?`},
	{kind: actordomain.Task, query: `SELECT t.id, t.created_at, t.created_by_account, '', '', t.created_session_id, '', '', ''
		FROM human_tasks t WHERE t.id>? AND NOT EXISTS (SELECT 1 FROM entry_actors e WHERE e.kind='task' AND e.entry_id=t.id) ORDER BY t.id LIMIT ?`},
	{kind: actordomain.Comment, query: `SELECT c.id, c.created_at, c.account_id, '', '', c.session_id, '', COALESCE(o.session_ref,''), COALESCE(o.via,'')
		FROM comments c LEFT JOIN comment_versions v ON v.comment_id=c.id AND v.revision=1 LEFT JOIN origin_contexts o ON o.id=v.origin_id
		WHERE c.id>? AND NOT EXISTS (SELECT 1 FROM entry_actors e WHERE e.kind='comment' AND e.entry_id=c.id) ORDER BY c.id LIMIT ?`},
	{kind: actordomain.Feature, query: `SELECT f.id, f.created_at, f.author_id, '', '', 0, f.session_ref, '', ''
		FROM features f WHERE f.id>? AND NOT EXISTS (SELECT 1 FROM entry_actors e WHERE e.kind='feature' AND e.entry_id=f.id) ORDER BY f.id LIMIT ?`},
	// Room messages say themselves who wrote them (author_kind, set by the
	// server by route); the harness is the prefix of the sender's identity.
	{kind: actordomain.Message, query: `SELECT m.id, m.created_at,
		CASE WHEN m.author_principal_id LIKE 'person:%' THEN CAST(substr(m.author_principal_id,8) AS INTEGER) ELSE 0 END,
		CASE m.author_kind WHEN 'human' THEN 'human' WHEN 'agent' THEN 'agent' ELSE 'skip' END,
		CASE WHEN m.author_kind='agent' THEN substr(m.sender_external_id,1,instr(m.sender_external_id||':',':')-1) ELSE '' END,
		0, '', COALESCE(o.session_ref,''), COALESCE(o.via,'')
		FROM coord_messages m LEFT JOIN origin_contexts o ON o.id=m.origin_id
		WHERE m.id>? AND NOT EXISTS (SELECT 1 FROM entry_actors e WHERE e.kind='message' AND e.entry_id=m.id) ORDER BY m.id LIMIT ?`},
	// Stimmen: die Id des Stempels ist VoteEntryID (Hash aus Entscheidung und
	// voter_key), die SQL-Funktion ghosttree_vote_entry rechnet dasselbe. Das
	// Konto steht im voter_key (person:N), nicht im Anzeigenamen.
	{kind: actordomain.Vote, query: `SELECT v.id, v.updated_at, v.account, v.hint, '', 0, '', '', '' FROM (
		SELECT ghosttree_vote_entry(decision_id, voter_key) AS id, updated_at,
			CASE WHEN voter_key LIKE 'person:%' THEN CAST(substr(voter_key, 8) AS INTEGER) ELSE 0 END AS account,
			CASE by_agent WHEN 1 THEN 'agent' ELSE 'human' END AS hint FROM decision_votes) v
		WHERE v.id>? AND NOT EXISTS (SELECT 1 FROM entry_actors e WHERE e.kind='vote' AND e.entry_id=v.id) ORDER BY v.id LIMIT ?`},
}

// BackfillActors stamps the actor of entries that have none, where it can be
// proven: a web origin is a person; an entry that names its writer as a person
// or an agent (requests and knowledge by origin, room messages, votes) is that;
// a session that belongs only to the writer gives an agent with harness,
// session and machine; for pains and ideas the one session of the writer that
// was active at the time counts. Everything else stays unknown. Distilled
// entries stay unknown: their session is the source, not the writer.
//
// Runs in batches of opts.Batch entries, each in its own transaction, so it can
// run beside a server. Idempotent: only entries without actor are read. Never
// at start-up, only as a command (ctx db backfill-actors).
func (s *Store) BackfillActors(opts ActorBackfillOptions) (ActorBackfillReport, error) {
	if s.writer != nil {
		return queueValue(s, []any{opts}, func(d *Store, p []any) (ActorBackfillReport, error) {
			return d.BackfillActors(p[0].(ActorBackfillOptions))
		})
	}
	if opts.Batch <= 0 {
		opts.Batch = 500
	}
	rep := ActorBackfillReport{DryRun: opts.DryRun}
	sessions := map[int64]bool{}
	owner := instanceOwnerID(s.db)
	for _, src := range actorBackfillSources {
		if len(opts.Kinds) > 0 && !hasString(opts.Kinds, src.kind) {
			continue
		}
		k := ActorBackfillKind{Kind: src.kind}
		if err := s.backfillActorSource(src, opts, owner, &k, sessions); err != nil {
			return rep, fmt.Errorf("%s: %w", src.kind, err)
		}
		rep.Kinds = append(rep.Kinds, k)
	}
	if opts.Models {
		if !opts.DryRun {
			// Sessions stamped before this run count as well.
			rows, err := s.db.Query(`SELECT DISTINCT session_id FROM entry_actors WHERE session_id IS NOT NULL`)
			if err != nil {
				return rep, err
			}
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return rep, err
				}
				sessions[id] = true
			}
			rows.Close()
		}
		n, segs, err := s.backfillSessionModels(keys(sessions), opts.DryRun)
		rep.Sessions, rep.Segments = n, segs
		if err != nil {
			return rep, fmt.Errorf("models: %w", err)
		}
	}
	return rep, nil
}

func (s *Store) backfillActorSource(src actorBackfillSource, opts ActorBackfillOptions, owner int64, k *ActorBackfillKind, sessions map[int64]bool) error {
	var after int64
	for {
		cands, err := s.actorCandidates(src.query, after, opts.Batch)
		if err != nil {
			return err
		}
		if len(cands) == 0 {
			return nil
		}
		after = cands[len(cands)-1].id
		k.Candidates += len(cands)
		recent := time.Now().UTC().Add(-actorBackfillQuiet).Format(time.RFC3339)
		type stamp struct {
			id  int64
			at  string
			act actordomain.Actor
		}
		var stamps []stamp
		for _, c := range cands {
			if at, ok := actordomain.NormalizeTime(c.at); !ok || at > recent {
				k.Recent++
				continue
			}
			act, ok := s.proveActor(c, src.tool, owner)
			if !ok {
				k.Unknown++
				continue
			}
			if act.Kind == actordomain.Human {
				k.Human++
			} else {
				k.Agent++
				if act.SessionID > 0 {
					k.WithSession++
					sessions[act.SessionID] = true
				}
			}
			stamps = append(stamps, stamp{c.id, c.at, act})
		}
		if opts.DryRun || len(stamps) == 0 {
			continue
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		for _, st := range stamps {
			at, ok := actordomain.NormalizeTime(st.at)
			if !ok {
				at = st.at
			}
			if err := recordActorTx(tx, src.kind, st.id, st.act, at, true); err != nil {
				tx.Rollback()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
}

func (s *Store) actorCandidates(query string, after int64, limit int) ([]actorCandidate, error) {
	rows, err := s.db.Query(query, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []actorCandidate
	for rows.Next() {
		var c actorCandidate
		if err := rows.Scan(&c.id, &c.at, &c.account, &c.hint, &c.harness, &c.sessionRow, &c.sessionRef, &c.originRef, &c.via); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// proveActor applies the rules of BackfillActors to one candidate, strongest
// proof first: a web origin is a person; a session that belongs only to the
// writer; a person or agent the server recorded by route; for pains and ideas
// a session of the writer that called the tool at that time; an agent by the
// entry's own origin. Everything else is unknown.
func (s *Store) proveActor(c actorCandidate, tool *toolProof, owner int64) (actordomain.Actor, bool) {
	act := actordomain.Actor{AccountID: c.account}
	if c.hint == "skip" {
		return act, false
	}
	if c.via == "web" {
		act.Kind = actordomain.Human
		return act, true
	}
	if sess, ok := s.provenSession(c, owner); ok {
		act.Kind, act.SessionID, act.Harness, act.Machine = actordomain.Agent, sess.ID, sess.Harness, sess.Scope.Machine
		return act, true
	}
	if c.hint == actordomain.Human {
		act.Kind = actordomain.Human
		return act, true
	}
	if tool != nil && c.account > 0 {
		if sess, ok := s.sessionByToolCall(c, tool, owner); ok {
			act.Kind, act.SessionID, act.Harness, act.Machine = actordomain.Agent, sess.ID, sess.Harness, sess.Scope.Machine
			return act, true
		}
	}
	if c.hint == actordomain.Agent {
		act.Kind, act.Harness = actordomain.Agent, strings.TrimSpace(c.harness)
		return act, true
	}
	return act, false
}

// provenSession is the session a candidate names, when it belongs only to the
// writer (the rule of the origin backfill, originSession).
func (s *Store) provenSession(c actorCandidate, owner int64) (Session, bool) {
	type named struct {
		ref   string
		byRow bool
	}
	var refs []named
	if c.sessionRow > 0 {
		refs = append(refs, named{strconv.FormatInt(c.sessionRow, 10), true})
	}
	for _, r := range []string{c.sessionRef, c.originRef} {
		if strings.TrimSpace(r) != "" {
			refs = append(refs, named{r, false})
		}
	}
	for _, r := range refs {
		ext, ok := s.originSession(r.ref, r.byRow, c.account, owner)
		if !ok {
			continue
		}
		if sess, ok := s.ownSessionByExternal(ext, c.account); ok {
			return sess, true
		}
	}
	return Session{}, false
}

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("ghosttree_vote_entry", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		id, _ := args[0].(int64)
		key, _ := args[1].(string)
		return actordomain.VoteEntryID(id, key), nil
	})
}

// actorBackfillQuiet: entries younger than this are left to the live stamp.
const actorBackfillQuiet = 5 * time.Minute

// toolWindow is how far a tool call may lie from the entry it made.
const toolWindow = 2 * time.Minute

// sessionByToolCall proves the session of a pain or idea from the transcript:
// among the writer's sessions that may have run then, the one whose lines
// within two minutes of the entry call the tool. When several do, the one whose
// lines name the new identifier (PAIN-12) wins; otherwise nothing is proven.
// The window is found by the times of the lines (binary search over seq), so
// no session is read in full.
func (s *Store) sessionByToolCall(c actorCandidate, tool *toolProof, owner int64) (Session, bool) {
	at, ok := actordomain.NormalizeTime(c.at)
	if !ok {
		return Session{}, false
	}
	t, _ := time.Parse(time.RFC3339, at)
	from, to := t.Add(-toolWindow).Format(time.RFC3339), t.Add(toolWindow).Format(time.RFC3339)
	// last_seen_at ist die Zeit des letzten Uploads und liegt nie vor der
	// letzten Zeile: als Vorauswahl genügt sie, der Beweis kommt aus den Zeilen.
	rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions
		WHERE (CASE WHEN account_id=0 THEN ? ELSE account_id END)=? AND started_at<=? AND last_seen_at>=?`,
		owner, c.account, to, from)
	if err != nil {
		return Session{}, false
	}
	cands, err := scanSessions(rows)
	if err != nil {
		return Session{}, false
	}
	ref := tool.prefix + strconv.FormatInt(c.id, 10)
	var called, named []Session
	for _, sess := range cands {
		call, name := s.sessionCalledTool(sess.ID, from, to, tool, ref)
		if call {
			called = append(called, sess)
			if name {
				named = append(named, sess)
			}
		}
	}
	switch {
	case len(called) == 1:
		return called[0], true
	case len(named) == 1:
		return named[0], true
	}
	return Session{}, false
}

// sessionCalledTool reads the lines of a session between from and to (by
// their own times) and says whether one is a real call of the tool
// (actordomain.CallsTool: a tool_use or function_call, or a shell command that
// starts with the CLI command; a result or a prompt that only mentions it does
// not count) and whether a tool result names ref as a whole identifier. Lines
// without a time are not placed in the window and do not count. At most
// toolScanLimit lines are read.
func (s *Store) sessionCalledTool(sessionID int64, from, to string, tool *toolProof, ref string) (called, named bool) {
	seq, ok := s.seqAtTime(sessionID, from)
	if !ok {
		return false, false
	}
	read := 0
	for read < toolScanLimit {
		rows, err := s.db.Query(`SELECT seq, raw FROM session_chunks WHERE session_id=? AND seq>=? ORDER BY seq LIMIT 200`, sessionID, seq)
		if err != nil {
			return called, named
		}
		n, past := 0, false
		for rows.Next() {
			var raw string
			if err := rows.Scan(&seq, &raw); err != nil {
				break
			}
			n++
			read++
			at, ok := chunkTime(raw)
			if !ok || at < from {
				continue
			}
			if at > to {
				past = true
				break
			}
			if actordomain.CallsTool(raw, tool.tools, tool.commands) {
				called = true
			}
			if actordomain.IsToolResult(raw) && namesRef(raw, ref) {
				named = true
			}
		}
		rows.Close()
		seq++
		if past || n == 0 {
			return called, named
		}
	}
	return called, named
}

const toolScanLimit = 2000

// namesRef says whether raw contains ref not followed by a further digit
// (PAIN-12 but not PAIN-123).
func namesRef(raw, ref string) bool {
	for i := 0; ; {
		j := strings.Index(raw[i:], ref)
		if j < 0 {
			return false
		}
		end := i + j + len(ref)
		if end >= len(raw) || raw[end] < '0' || raw[end] > '9' {
			return true
		}
		i = end
	}
}

type chunkTimeLine struct {
	Timestamp string `json:"timestamp"`
}

// chunkTime is the time of a transcript line, RFC3339 in UTC.
func chunkTime(raw string) (string, bool) {
	if !strings.Contains(raw, `"timestamp"`) {
		return "", false
	}
	var l chunkTimeLine
	if json.Unmarshal([]byte(raw), &l) != nil {
		return "", false
	}
	return actordomain.NormalizeTime(l.Timestamp)
}

// timeFrom is the time of the first line at or after seq that has one,
// looking at timeLookahead lines only.
func (s *Store) timeFrom(sessionID int64, seq int) (string, bool) {
	return timeFromQ(s.db, sessionID, seq)
}

// timeLookahead: so many lines without a time in a row are rare (a long
// tool output split into lines has none).
const timeLookahead = 200

func timeFromQ(q queryer, sessionID int64, seq int) (string, bool) {
	rows, err := q.Query(`SELECT raw FROM session_chunks WHERE session_id=? AND seq>=? ORDER BY seq LIMIT ?`, sessionID, seq, timeLookahead)
	if err != nil {
		return "", false
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) != nil {
			return "", false
		}
		if at, ok := chunkTime(raw); ok {
			return at, true
		}
	}
	return "", false
}

// seqAtTime finds by binary search over seq the first line of a session at or
// after the time at (lines are in time order). A stretch without any time is
// taken as before at, never as after it: the search then moves on and, at
// worst, finds nothing, which leaves the entry unknown instead of reading the
// wrong stretch. ok is false for a session without lines or whose lines all
// lie before at.
func (s *Store) seqAtTime(sessionID int64, at string) (int, bool) {
	var maxSeq sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(seq) FROM session_chunks WHERE session_id=?`, sessionID).Scan(&maxSeq); err != nil || !maxSeq.Valid {
		return 0, false
	}
	lo, hi := 0, int(maxSeq.Int64)+1
	for lo < hi {
		mid := (lo + hi) / 2
		t, ok := s.timeFrom(sessionID, mid)
		if ok && t >= at {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo, lo <= int(maxSeq.Int64)
}

// backfillSessionModels derives the model history of sessions from their
// stored lines: assistant lines (Claude Code) and turn contexts (Codex, stored
// without role). A session whose history already begins near its first line
// is left alone; one that ran when this version was deployed has only the
// later part from the live upload and is read again in full, its rows
// replaced. Reading and replacing happen in one transaction per session, so
// an upload in between cannot be lost: it waits for the write lock and then
// folds its lines onto the new rows.
func (s *Store) backfillSessionModels(ids []int64, dryRun bool) (sessions, segments int, err error) {
	for _, id := range ids {
		n, err := s.backfillSessionModelsOf(id, dryRun)
		if err != nil {
			return sessions, segments, err
		}
		if n > 0 {
			sessions++
			segments += n
		}
	}
	return sessions, segments, nil
}

func (s *Store) backfillSessionModelsOf(id int64, dryRun bool) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if !dryRun {
		// Die Schreibsperre vor dem Lesen nehmen: ein Upload zwischen Lesen und
		// Ersetzen ginge sonst verloren.
		if _, err := tx.Exec(`DELETE FROM session_models WHERE 0`); err != nil {
			return 0, err
		}
	}
	var first sql.NullString
	if err := tx.QueryRow(`SELECT MIN(first_at) FROM session_models WHERE session_id=?`, id).Scan(&first); err != nil {
		return 0, err
	}
	if first.Valid && modelsCoverStart(tx, id, first.String) {
		return 0, nil
	}
	obs, err := sessionObservations(tx, id)
	if err != nil {
		return 0, err
	}
	segs := actordomain.Fold(nil, obs)
	if len(segs) == 0 || dryRun {
		return len(segs), nil
	}
	if _, err := tx.Exec(`DELETE FROM session_models WHERE session_id=?`, id); err != nil {
		return 0, err
	}
	for _, seg := range segs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO session_models(session_id,first_at,model,last_at) VALUES(?,?,?,?)`, id, seg.FirstAt, seg.Model, seg.LastAt); err != nil {
			return 0, err
		}
	}
	return len(segs), tx.Commit()
}

// modelsStartSlack is how long after the first line of a session its first
// answer may come for the stored history to count as complete.
const modelsStartSlack = 10 * time.Minute

// modelsCoverStart says whether the stored history of a session begins near
// its first transcript line.
func modelsCoverStart(q queryer, id int64, firstModel string) bool {
	start, ok := timeFromQ(q, id, 0)
	if !ok {
		return true
	}
	t, err1 := time.Parse(time.RFC3339, start)
	m, err2 := time.Parse(time.RFC3339, firstModel)
	if err1 != nil || err2 != nil {
		return true
	}
	return m.Sub(t) <= modelsStartSlack
}

func sessionObservations(q queryer, id int64) ([]actordomain.Observation, error) {
	var out []actordomain.Observation
	after := -1
	for {
		rows, err := q.Query(`SELECT seq, raw FROM session_chunks WHERE session_id=? AND seq>?
			AND (role='assistant' OR (role='' AND instr(raw,'"turn_context"')>0)) ORDER BY seq LIMIT 1000`, id, after)
		if err != nil {
			return nil, err
		}
		n := 0
		for rows.Next() {
			var raw string
			if err := rows.Scan(&after, &raw); err != nil {
				rows.Close()
				return nil, err
			}
			n++
			if o, ok := actordomain.ObserveModel(raw); ok {
				out = append(out, o)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return out, nil
		}
	}
}
