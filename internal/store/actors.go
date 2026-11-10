package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	actordomain "github.com/Deadweight-Labs/ghosttree/internal/actor"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
)

// actorSchema (REQ-620): who wrote an entry, one row per (kind, entry), and the
// models a session answered with, one row per stretch. Both tables are new and
// small; nothing existing is rewritten. entry_actors is keyed by the kind
// names of internal/actor, so search, statistics and the web read every kind
// the same way. A creation stamp stays as first written; a closing stamp
// (criterion met, request done) and a reaction are replaced when they happen
// again (actordomain.Replaces).
//
// session_ref keeps the session's external id when the session was not yet
// uploaded at the time of writing (the collector lags behind the agent); the
// reader resolves it later, and only to a session of the same account.
//
// session_models carries no foreign key on purpose: it is derived from the
// transcript and must never block a change to sessions.
const actorSchema = `
CREATE TABLE IF NOT EXISTS entry_actors(
  kind TEXT NOT NULL,
  entry_id INTEGER NOT NULL,
  actor TEXT NOT NULL CHECK(actor IN ('human','agent')),
  account_id INTEGER NOT NULL DEFAULT 0,
  harness TEXT NOT NULL DEFAULT '',
  machine TEXT NOT NULL DEFAULT '',
  session_id INTEGER,
  session_ref TEXT NOT NULL DEFAULT '',
  at TEXT NOT NULL,
  backfilled INTEGER NOT NULL DEFAULT 0 CHECK(backfilled IN (0,1)),
  PRIMARY KEY(kind, entry_id)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS entry_actors_pending ON entry_actors(session_ref) WHERE session_ref<>'';
CREATE INDEX IF NOT EXISTS entry_actors_session ON entry_actors(session_id, kind, at) WHERE session_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS session_models(
  session_id INTEGER NOT NULL,
  first_at TEXT NOT NULL,
  model TEXT NOT NULL,
  last_at TEXT NOT NULL,
  PRIMARY KEY(session_id, first_at, model)) WITHOUT ROWID;
`

func ensureActors(db *sql.DB) error {
	if _, err := db.Exec(actorSchema); err != nil {
		return err
	}
	// via: wie ein Mensch handelte, web oder terminal (Selbstauskunft); additiv.
	if err := ensureColumn(db, "entry_actors", "via", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	return rekeyVoteActors(db)
}

// rekeyVoteActors gives vote stamps written by the first version (#191, keyed
// by the rowid of decision_votes) the stable key VoteEntryID. A stamp counts as
// old when a vote has its entry_id as rowid and a different hash. Idempotent
// and cheap (only vote stamps, a handful); it runs at start-up. A stamp whose
// new key a live stamp already holds is dropped, the live one is newer.
func rekeyVoteActors(db *sql.DB) error {
	const old = `kind='vote' AND EXISTS (SELECT 1 FROM decision_votes v WHERE v.rowid=entry_actors.entry_id
		AND ghosttree_vote_entry(v.decision_id, v.voter_key)<>entry_actors.entry_id)`
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE OR IGNORE entry_actors SET entry_id=(SELECT ghosttree_vote_entry(v.decision_id, v.voter_key)
		FROM decision_votes v WHERE v.rowid=entry_actors.entry_id) WHERE ` + old); err != nil {
		return fmt.Errorf("rekey vote stamps: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM entry_actors WHERE ` + old); err != nil {
		return fmt.Errorf("rekey vote stamps: %w", err)
	}
	return tx.Commit()
}

// EntryActor is the stored actor of one entry with what the reader derives:
// the model active at the time of writing (from session_models) and the
// account name.
type EntryActor struct {
	actordomain.Actor
	At         string `json:"at"`
	Backfilled bool   `json:"backfilled,omitempty"`
	// Model is the raw model identifier; empty when the session is unknown or
	// its transcript named none.
	Model string `json:"model,omitempty"`
}

// RecordActor stores who wrote an entry. The first stamp of a creation kind
// stays (a retry or a second handler cannot rewrite it), a closing kind
// (actordomain.Replaces) takes the latest. An actor without kind is not stored: the
// entry stays unknown rather than guessed.
func (s *Store) RecordActor(kind string, id int64, a actordomain.Actor) error {
	if s.writer != nil {
		return queueWrite(s, []any{kind, id, a}, func(d *Store, p []any) error {
			return d.RecordActor(p[0].(string), p[1].(int64), p[2].(actordomain.Actor))
		})
	}
	return recordActorTx(s.db, kind, id, a, now(), false)
}

func recordActorTx(tx txExec, kind string, id int64, a actordomain.Actor, at string, backfilled bool) error {
	if id <= 0 || !actordomain.ValidKind(kind) || (a.Kind != actordomain.Human && a.Kind != actordomain.Agent) {
		return nil
	}
	// Der Nachtrag füllt nur Lücken. Ein Live-Stempel ersetzt einen
	// nachgetragenen (er weiß es besser), sonst bleibt der erste; eine
	// Abschluss-Art nimmt immer den letzten.
	verb, conflict := "INSERT OR IGNORE", ""
	if !backfilled {
		verb = "INSERT"
		conflict = ` ON CONFLICT(kind,entry_id) DO UPDATE SET actor=excluded.actor, account_id=excluded.account_id, harness=excluded.harness,
			machine=excluded.machine, session_id=excluded.session_id, session_ref=excluded.session_ref, at=excluded.at, backfilled=0, via=excluded.via`
		if !actordomain.Replaces(kind) {
			conflict += ` WHERE entry_actors.backfilled=1`
		}
	}
	var session any
	if a.SessionID > 0 {
		session = a.SessionID
	}
	bf := 0
	if backfilled {
		bf = 1
	}
	ref := ""
	if a.SessionID <= 0 && a.Kind == actordomain.Agent {
		ref = strings.TrimSpace(a.SessionRef)
	}
	via := ""
	if a.Kind == actordomain.Human && (a.Via == actordomain.ViaWeb || a.Via == actordomain.ViaTerminal) {
		via = a.Via
	}
	_, err := tx.Exec(verb+` INTO entry_actors(kind,entry_id,actor,account_id,harness,machine,session_id,session_ref,at,backfilled,via) VALUES(?,?,?,?,?,?,?,?,?,?,?)`+conflict,
		kind, id, a.Kind, a.AccountID, strings.TrimSpace(a.Harness), strings.TrimSpace(a.Machine), session, ref, at, bf, via)
	return err
}

// ActorsOf reads the actors of entries of one kind, by entry id. Entries
// without a stamp are missing from the result (unknown).
func (s *Store) ActorsOf(kind string, ids []int64) (map[int64]EntryActor, error) {
	if s.reader != nil {
		return s.reader.ActorsOf(kind, ids)
	}
	out := map[int64]EntryActor{}
	sessions := map[int64]bool{}
	err := chunked(dedupe(ids), func(part []int64) error {
		rows, err := s.db.Query(`SELECT e.entry_id, e.actor, e.account_id, COALESCE(p.name,''), e.harness, e.machine, COALESCE(e.session_id,0), e.session_ref, e.at, e.backfilled, e.via
			FROM entry_actors e LEFT JOIN persons p ON p.id=e.account_id
			WHERE e.kind=? AND e.entry_id IN (`+placeholders(len(part))+`)`, append([]any{kind}, factIDArgs(part)...)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var ea EntryActor
			var bf int
			if err := rows.Scan(&id, &ea.Kind, &ea.AccountID, &ea.Person, &ea.Harness, &ea.Machine, &ea.SessionID, &ea.SessionRef, &ea.At, &bf, &ea.Via); err != nil {
				return err
			}
			ea.Backfilled = bf == 1
			out[id] = ea
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if err := s.resolveActorSessions(out); err != nil {
		return nil, err
	}
	for _, ea := range out {
		if ea.SessionID > 0 {
			sessions[ea.SessionID] = true
		}
	}
	segs, err := s.sessionModelsOf(keys(sessions))
	if err != nil {
		return nil, err
	}
	for id, ea := range out {
		if ea.SessionID > 0 {
			ea.Model = actordomain.ModelAt(segs[ea.SessionID], ea.At)
			out[id] = ea
		}
	}
	return out, nil
}

func keys(m map[int64]bool) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ActorOf is ActorsOf for one entry; ok is false when it is unknown.
func (s *Store) ActorOf(kind string, id int64) (EntryActor, bool, error) {
	if s.reader != nil {
		return s.reader.ActorOf(kind, id)
	}
	m, err := s.ActorsOf(kind, []int64{id})
	if err != nil {
		return EntryActor{}, false, err
	}
	ea, ok := m[id]
	return ea, ok, nil
}

// SessionModels is the model history of a session in order: which model
// answered from when to when. Empty when the transcript named none.
func (s *Store) SessionModels(sessionID int64) ([]actordomain.Segment, error) {
	if s.reader != nil {
		return s.reader.SessionModels(sessionID)
	}
	m, err := s.sessionModelsOf([]int64{sessionID})
	return m[sessionID], err
}

func (s *Store) sessionModelsOf(ids []int64) (map[int64][]actordomain.Segment, error) {
	out := map[int64][]actordomain.Segment{}
	err := chunked(ids, func(part []int64) error {
		rows, err := s.db.Query(`SELECT session_id, model, first_at, last_at FROM session_models
			WHERE session_id IN (`+placeholders(len(part))+`) ORDER BY session_id, first_at, model`, factIDArgs(part)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var seg actordomain.Segment
			if err := rows.Scan(&id, &seg.Model, &seg.FirstAt, &seg.LastAt); err != nil {
				return err
			}
			out[id] = append(out[id], seg)
		}
		return rows.Err()
	})
	return out, err
}

// foldSessionModelsTx adds model sightings of one session to session_models,
// in transcript order. Only the last stretch can be extended; a sighting older
// than it (a late upload of earlier lines) opens a stretch of its own, which
// ModelAt orders by time.
func foldSessionModelsTx(tx txExec, sessionID int64, obs []actordomain.Observation) error {
	if len(obs) == 0 {
		return nil
	}
	var last actordomain.Segment
	err := tx.QueryRow(`SELECT model, first_at, last_at FROM session_models WHERE session_id=? ORDER BY first_at DESC, model DESC LIMIT 1`, sessionID).
		Scan(&last.Model, &last.FirstAt, &last.LastAt)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	var segs []actordomain.Segment
	if err == nil {
		segs = []actordomain.Segment{last}
	}
	var fresh []actordomain.Observation
	for _, o := range obs {
		if len(segs) > 0 && o.At < segs[0].FirstAt {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO session_models(session_id,first_at,model,last_at) VALUES(?,?,?,?)`, sessionID, o.At, o.Model, o.At); err != nil {
				return err
			}
			continue
		}
		fresh = append(fresh, o)
	}
	folded := actordomain.Fold(segs, fresh)
	for i, seg := range folded {
		if i == 0 && len(segs) > 0 {
			if seg.LastAt != last.LastAt {
				if _, err := tx.Exec(`UPDATE session_models SET last_at=? WHERE session_id=? AND first_at=? AND model=?`, seg.LastAt, sessionID, seg.FirstAt, seg.Model); err != nil {
					return err
				}
			}
			continue
		}
		if _, err := tx.Exec(`INSERT INTO session_models(session_id,first_at,model,last_at) VALUES(?,?,?,?)
			ON CONFLICT(session_id,first_at,model) DO UPDATE SET last_at=MAX(last_at, excluded.last_at)`, sessionID, seg.FirstAt, seg.Model, seg.LastAt); err != nil {
			return err
		}
	}
	return nil
}

// recordChunkModelsTx reads the model from freshly stored transcript lines
// and folds it into session_models. Only new lines count, so a repeated upload
// does not move anything.
func recordChunkModelsTx(tx txExec, rows []chunkRow) error {
	bySession := map[int64][]actordomain.Observation{}
	var order []int64
	for _, r := range rows {
		o, ok := actordomain.ObserveModel(r.raw)
		if !ok {
			continue
		}
		if _, seen := bySession[r.sessionID]; !seen {
			order = append(order, r.sessionID)
		}
		bySession[r.sessionID] = append(bySession[r.sessionID], o)
	}
	for _, id := range order {
		if err := foldSessionModelsTx(tx, id, bySession[id]); err != nil {
			return fmt.Errorf("session models: %w", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------- statistics

// ActorStat is one line of the statistics: a harness and a model, or the
// people (Actor human), with what they did in the projects counted.
type ActorStat struct {
	Actor string `json:"actor"`
	// Via separates people: web (verified by the session) and terminal
	// (self-reported by the CLI); empty for agents and older stamps.
	Via     string `json:"via,omitempty"`
	Harness string `json:"harness,omitempty"`
	Family  string `json:"family,omitempty"`
	Model   string `json:"model,omitempty"`
	// ModelLabel is the model as a person reads it ("Opus 5.5").
	ModelLabel string `json:"model_label,omitempty"`
	// Entries created (requests, pains, ideas, knowledge, tasks, features).
	Entries int `json:"entries"`
	// CriteriaMet are criteria that are met now and were closed by this line.
	CriteriaMet int `json:"criteria_met"`
	// RequestsDone are requests that are done now and were closed by this line;
	// FeaturesDone the ones of type feature among them.
	RequestsDone int `json:"requests_done"`
	FeaturesDone int `json:"features_done"`
}

// actorStatSources say for each counted kind where its project is, what the
// read filter of the lists needs, and when it still counts (a criterion only
// while met, a request only while done, knowledge only while active or stale
// and not quarantined). Columns: entry, request behind it, project, request
// type, person, held session of a distilled request (for a task those of its
// request), for knowledge machine, confidence, origin and session_ref, and for
// a task its assignee and creator.
var actorStatSources = []struct {
	kind, sql string
}{
	{actordomain.Request, `SELECT e.entry_id, t.id, t.project, t.type, t.person, ` + heldColOf("t") + `, '', '', '', '', 0, 0 FROM entry_actors e JOIN requests t ON t.id=e.entry_id WHERE e.kind='request'`},
	{actordomain.Pain, `SELECT e.entry_id, 0, t.project, '', '', '', '', '', '', '', 0, 0 FROM entry_actors e JOIN pains t ON t.id=e.entry_id WHERE e.kind='pain'`},
	{actordomain.Idea, `SELECT e.entry_id, 0, t.project, '', '', '', '', '', '', '', 0, 0 FROM entry_actors e JOIN ideas t ON t.id=e.entry_id WHERE e.kind='idea'`},
	{actordomain.Knowledge, `SELECT e.entry_id, 0, t.project, '', t.person, '', t.machine, t.confidence, t.origin, t.session_ref, 0, 0 FROM entry_actors e JOIN knowledge t ON t.id=e.entry_id
		WHERE e.kind='knowledge' AND t.project<>'' AND t.status IN ('active','stale') AND t.confidence<>'quarantined'`},
	{actordomain.Task, `SELECT e.entry_id, t.request_id, t.project, '', r.person, ` + heldColOf("r") + `, '', '', '', '', t.assignee_account, t.created_by_account
		FROM entry_actors e JOIN human_tasks t ON t.id=e.entry_id JOIN requests r ON r.id=t.request_id WHERE e.kind='task'`},
	{actordomain.Feature, `SELECT e.entry_id, 0, t.project, '', '', '', '', '', '', '', 0, 0 FROM entry_actors e JOIN features t ON t.id=e.entry_id WHERE e.kind='feature'`},
	{actordomain.Criterion, `SELECT e.entry_id, r.id, r.project, r.type, r.person, ` + heldColOf("r") + `, '', '', '', '', 0, 0 FROM entry_actors e JOIN request_criteria c ON c.id=e.entry_id JOIN requests r ON r.id=c.request_id WHERE e.kind='criterion' AND c.state='met'`},
	{actordomain.RequestDone, `SELECT e.entry_id, t.id, t.project, t.type, t.person, ` + heldColOf("t") + `, '', '', '', '', 0, 0 FROM entry_actors e JOIN requests t ON t.id=e.entry_id WHERE e.kind='request_done' AND t.state='done'`},
}

func heldColOf(alias string) string {
	return `CASE WHEN ` + alias + `.origin='distilled' AND ` + alias + `.person='' THEN ` + alias + `.session_ref ELSE '' END`
}

// ActorStatHit is one counted stamp with what the read filter needs.
type ActorStatHit struct {
	Kind    string
	EntryID int64
	// RequestID is the request behind a request, criterion or task.
	RequestID int64
	Project   string
	// Type is the request type (features are requests of type feature).
	Type string
	// Ref carries person and held session of a request, Knowledge the fields
	// CanSeeKnowledge reads.
	Ref       ObjectRef
	Knowledge Knowledge
	// Task carries what canSeeTaskVia reads.
	Task  humantask.Task
	Actor EntryActor
}

// ActorStatHits reads the stamps the statistics count in these projects, with
// the model resolved. It does not decide what a viewer may see; that is
// ProjectAccess.ActorStats.
func (s *Store) ActorStatHits(projects []string) ([]ActorStatHit, error) {
	if s.reader != nil {
		return s.reader.ActorStatHits(projects)
	}
	allowed := map[string]bool{}
	for _, p := range projects {
		allowed[p] = true
	}
	var hits []ActorStatHit
	sessions := map[int64]bool{}
	for _, src := range actorStatSources {
		byID := map[int64]ActorStatHit{}
		rows, err := s.db.Query(src.sql)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", src.kind, err)
		}
		for rows.Next() {
			h := ActorStatHit{Kind: src.kind}
			k := &h.Knowledge
			if err := rows.Scan(&h.EntryID, &h.RequestID, &h.Project, &h.Type, &h.Ref.Person, &h.Ref.HeldBy,
				&k.Scope.Machine, &k.Confidence, &k.Origin, &k.SessionRef, &h.Task.AssigneeAccount, &h.Task.CreatedByAccount); err != nil {
				rows.Close()
				return nil, err
			}
			if !allowed[h.Project] {
				continue
			}
			h.Ref.ID, h.Ref.Project = h.RequestID, h.Project
			k.ID, k.Scope.Project, k.Person = h.EntryID, h.Project, h.Ref.Person
			h.Task.ID, h.Task.Project, h.Task.RequestID = h.EntryID, h.Project, h.RequestID
			byID[h.EntryID] = h
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		list := make([]int64, 0, len(byID))
		for id := range byID {
			list = append(list, id)
		}
		actors, err := s.rawActorsOf(src.kind, list)
		if err != nil {
			return nil, err
		}
		for id, ea := range actors {
			h := byID[id]
			h.Actor = ea
			hits = append(hits, h)
			if ea.SessionID > 0 {
				sessions[ea.SessionID] = true
			}
		}
	}
	segs, err := s.sessionModelsOf(keys(sessions))
	if err != nil {
		return nil, err
	}
	for i := range hits {
		if id := hits[i].Actor.SessionID; id > 0 {
			hits[i].Actor.Model = actordomain.ModelAt(segs[id], hits[i].Actor.At)
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Kind != hits[j].Kind {
			return hits[i].Kind < hits[j].Kind
		}
		return hits[i].EntryID < hits[j].EntryID
	})
	return hits, nil
}

// aggregateActorStats folds hits into lines per actor, harness and model.
func aggregateActorStats(hits []ActorStatHit) []ActorStat {
	lines := map[string]*ActorStat{}
	for _, h := range hits {
		model, harness, via := "", "", ""
		if h.Actor.Kind == actordomain.Agent {
			harness = actordomain.HarnessKey(h.Actor.Harness)
			model = h.Actor.Model
		} else {
			via = h.Actor.Via
		}
		key := h.Actor.Kind + "\x00" + harness + "\x00" + model + "\x00" + via
		st := lines[key]
		if st == nil {
			st = &ActorStat{Actor: h.Actor.Kind, Via: via, Harness: harness, Model: model, ModelLabel: actordomain.ModelLabel(model)}
			if h.Actor.Kind == actordomain.Agent {
				st.Family = actordomain.Family(harness)
			}
			lines[key] = st
		}
		switch h.Kind {
		case actordomain.Criterion:
			st.CriteriaMet++
		case actordomain.RequestDone:
			st.RequestsDone++
			if h.Type == "feature" {
				st.FeaturesDone++
			}
		default:
			st.Entries++
		}
	}
	out := make([]ActorStat, 0, len(lines))
	for _, st := range lines {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Actor == actordomain.Agent) != (b.Actor == actordomain.Agent) {
			return a.Actor == actordomain.Agent
		}
		ta, tb := a.Entries+a.CriteriaMet+a.RequestsDone, b.Entries+b.CriteriaMet+b.RequestsDone
		if ta != tb {
			return ta > tb
		}
		if a.Harness != b.Harness {
			return a.Harness < b.Harness
		}
		return a.Model < b.Model
	})
	return out
}

// rawActorsOf reads stamps without resolving names and models.
func (s *Store) rawActorsOf(kind string, ids []int64) (map[int64]EntryActor, error) {
	out := map[int64]EntryActor{}
	err := chunked(ids, func(part []int64) error {
		rows, err := s.db.Query(`SELECT entry_id, actor, account_id, harness, COALESCE(session_id,0), session_ref, at, via FROM entry_actors
			WHERE kind=? AND entry_id IN (`+placeholders(len(part))+`)`, append([]any{kind}, factIDArgs(part)...)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var ea EntryActor
			if err := rows.Scan(&id, &ea.Kind, &ea.AccountID, &ea.Harness, &ea.SessionID, &ea.SessionRef, &ea.At, &ea.Via); err != nil {
				return err
			}
			out[id] = ea
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, s.resolveActorSessions(out)
}

// resolveActorSessions fills session, harness and machine of agent stamps that
// only know the external id of their session: the session of the same account
// with that id, once it is uploaded. A session of another account is never
// taken; the stamp then stays without session. One query for all pending ids;
// the upload of the session writes the result back (bindPendingActorsTx).
func (s *Store) resolveActorSessions(m map[int64]EntryActor) error {
	type key struct {
		ref     string
		account int64
	}
	var refs []any
	seen := map[string]bool{}
	for _, ea := range m {
		if ea.SessionID <= 0 && ea.SessionRef != "" && ea.AccountID > 0 && !seen[ea.SessionRef] {
			seen[ea.SessionRef] = true
			refs = append(refs, ea.SessionRef)
		}
	}
	if len(refs) == 0 {
		return nil
	}
	owner := instanceOwnerID(s.db)
	found := map[key]Session{}
	for len(refs) > 0 {
		part := refs
		if len(part) > 500 {
			part = part[:500]
		}
		refs = refs[len(part):]
		rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions WHERE external_id IN (`+placeholders(len(part))+`) ORDER BY last_seen_at`, part...)
		if err != nil {
			return err
		}
		list, err := scanSessions(rows)
		if err != nil {
			return err
		}
		for _, sess := range list {
			// Die zuletzt gesehene Zeile gewinnt, wie bei OwnSessionByExternal.
			found[key{sess.ExternalID, effectiveOwner(sess.AccountID, owner)}] = sess
		}
	}
	for id, ea := range m {
		if ea.SessionID > 0 || ea.SessionRef == "" {
			continue
		}
		sess, ok := found[key{ea.SessionRef, ea.AccountID}]
		if !ok {
			continue
		}
		ea.SessionID = sess.ID
		if ea.Harness == "" {
			ea.Harness = sess.Harness
		}
		if ea.Machine == "" {
			ea.Machine = sess.Scope.Machine
		}
		m[id] = ea
	}
	return nil
}

// bindPendingActorsTx gives the stamps that waited for this session (written
// before its first upload) their session, harness and machine. Only stamps of
// the session's own account.
func bindPendingActorsTx(tx txExec, sessionID, account int64, external, harness, machine string) error {
	_, err := tx.Exec(`UPDATE entry_actors SET session_id=?, session_ref='',
		harness=CASE WHEN harness='' THEN ? ELSE harness END, machine=CASE WHEN machine='' THEN ? ELSE machine END
		WHERE session_ref=? AND session_ref<>'' AND session_id IS NULL AND account_id=?`, sessionID, harness, machine, external, account)
	return err
}

// OwnSessionByExternal is the session of the account with this external id,
// the most recently seen one if several harnesses share the id. Legacy rows
// without account belong to the instance owner.
func (s *Store) OwnSessionByExternal(external string, account int64) (Session, bool) {
	if s.reader != nil {
		return s.reader.OwnSessionByExternal(external, account)
	}
	return s.ownSessionByExternal(external, account)
}

func (s *Store) ownSessionByExternal(external string, account int64) (Session, bool) {
	if external == "" || account <= 0 {
		return Session{}, false
	}
	rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions WHERE external_id=?
		AND (CASE WHEN account_id=0 THEN ? ELSE account_id END)=? ORDER BY last_seen_at DESC LIMIT 1`,
		external, instanceOwnerID(s.db), account)
	if err != nil {
		return Session{}, false
	}
	found, err := scanSessions(rows)
	if err != nil || len(found) == 0 {
		return Session{}, false
	}
	return found[0], true
}

// ReactionRowID is the row of the caller's reaction on an object, 0 if there
// is none. The handler stamps its actor after setting it.
func (s *Store) ReactionRowID(kind string, id int64, content string, account int64) int64 {
	if s.reader != nil {
		return s.reader.ReactionRowID(kind, id, content, account)
	}
	key, ok := ParseReaction(content)
	if !ok {
		return 0
	}
	var row int64
	if err := s.db.QueryRow(`SELECT id FROM reactions WHERE object_kind=? AND object_id=? AND content=? AND account_id=?`, kind, id, key, account).Scan(&row); err != nil {
		return 0
	}
	return row
}

// ActorStats is the statistics over the projects this viewer may see in full:
// one project when named (it must be readable), else every project with a role
// or in one of the viewer's organisations. Guests of a project do not count it.
// Every entry passes the same read filter as in the lists, and a model counts
// only from a session whose metadata the viewer may see (ActorView), else as
// unknown.
func (a *ProjectAccess) ActorStats(project string) ([]ActorStat, []string, error) {
	projects, err := a.statsProjects(project)
	if err != nil {
		return nil, nil, err
	}
	hits, err := a.st.ActorStatHits(projects)
	if err != nil {
		return nil, projects, err
	}
	var sessions []int64
	for _, h := range hits {
		sessions = append(sessions, h.Actor.SessionID)
	}
	a.loadActorSessions(sessions)
	kept := hits[:0]
	tasks := map[int64]bool{}
	for _, h := range hits {
		if !a.actorStatVisible(h, tasks) {
			continue
		}
		// Ein Modell aus einer Session, deren Metadaten der Betrachter nicht
		// sehen darf, zählt als unbekannt.
		if h.Actor.Model != "" && !a.actorModelVisible(h.Actor.SessionID) {
			h.Actor.Model = ""
		}
		kept = append(kept, h)
	}
	return aggregateActorStats(kept), projects, nil
}

// statsProjects are the projects a statistics page counts: the named one (it
// must be readable), else every project with a role or in one of the viewer's
// organisations, without those whose machines the viewer may not see.
func (a *ProjectAccess) statsProjects(project string) ([]string, error) {
	a.Filtered()
	var candidates []string
	if project != "" {
		if err := a.Check(project, ResProject, ActRead, Object{}); err != nil {
			return nil, err
		}
		candidates = []string{project}
	} else {
		seen := map[string]bool{}
		for _, p := range a.Projects() {
			seen[p] = true
		}
		if list, err := a.st.ListProjects(a.principal.ID, 0); err == nil {
			for _, p := range a.VisibleProjects(list) {
				seen[p.Remote] = true
			}
		}
		for p := range seen {
			candidates = append(candidates, p)
		}
		sort.Strings(candidates)
	}
	var projects []string
	for _, p := range candidates {
		if !a.HidesMachines(p) {
			projects = append(projects, p)
		}
	}
	return projects, nil
}

// actorStatVisible is the read filter of the lists for one counted entry:
// requests not held back from a foreign private session, knowledge the viewer
// may read, a task like in the task lists (its request must be readable unless
// it is assigned to the viewer), the rest by its resource in the project.
// tasks caches the answers per request.
func (a *ProjectAccess) actorStatVisible(h ActorStatHit, tasks map[int64]bool) bool {
	switch h.Kind {
	case actordomain.Request, actordomain.Criterion, actordomain.RequestDone:
		obj, visible := a.RequestObject(h.Ref)
		return visible && a.Allow(h.Project, ResRequest, ActRead, obj)
	case actordomain.Knowledge:
		return a.CanSeeKnowledge(h.Knowledge)
	case actordomain.Pain:
		return a.Allow(h.Project, ResPain, ActRead, Object{})
	case actordomain.Idea:
		return a.Allow(h.Project, ResIdea, ActRead, Object{})
	case actordomain.Task:
		return a.canSeeTaskVia(h.Task, tasks)
	case actordomain.Feature:
		return a.Allow(h.Project, ResFeature, ActRead, Object{})
	}
	return false
}
