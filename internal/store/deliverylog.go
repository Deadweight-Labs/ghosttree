package store

import (
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/deliverylog"
)

// The measurement base for "did the context we deliver help" (REQ-636, P0 of
// the concept "Schlankes Profil und Kontext, der hilft"). Nothing here changes
// what an agent is given; it only records and counts.
//
// context_deliveries stays what it was: the memory that makes an entry arrive
// once per session, aged out after a week. The log below is the long-lived
// counterpart. One row is one part of one delivery as the hook put it in front
// of the model, with its size in characters, so the rows of a session add up to
// what the session budget counted. It holds numbers, repository paths, sizes
// and times, never text of a prompt and never a word from one: the line a hint
// is built from is logged as a hint of some size.
//
// A row is kept deliveryLogRetention and then folded into the monthly sums of
// context_delivery_rollup, which keep how often and how much of each entry was
// delivered but drop the path and the session. Folding happens where rows are
// written, so no maintenance job has to exist for the table to stay bounded.
//
// used, judged_at and held_out are not written yet. They are the columns the
// later packages of the concept fill (the judgement from the transcript, the
// hold-back sample), laid down now so those do not need a migration.
const deliveryLogSchema = `
CREATE TABLE IF NOT EXISTS context_delivery_log(
  id INTEGER PRIMARY KEY,
  account_id INTEGER NOT NULL DEFAULT 0,
  session_key TEXT NOT NULL,
  call TEXT NOT NULL DEFAULT '',
  ord INTEGER NOT NULL DEFAULT 0,
  project TEXT NOT NULL,
  harness TEXT NOT NULL DEFAULT '',
  profile TEXT NOT NULL DEFAULT 'full',
  subagent INTEGER NOT NULL DEFAULT 0 CHECK(subagent IN (0,1)),
  via TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('knowledge','ghost','runbook','ledger','feature','hint','frame')),
  object_id INTEGER NOT NULL DEFAULT 0,
  path TEXT NOT NULL DEFAULT '',
  chars INTEGER NOT NULL DEFAULT 0,
  held_out INTEGER NOT NULL DEFAULT 0 CHECK(held_out IN (0,1)),
  delivered_at TEXT NOT NULL,
  used INTEGER NOT NULL DEFAULT -1 CHECK(used IN (-1,0,1,2)),
  judged_at TEXT NOT NULL DEFAULT '');
CREATE UNIQUE INDEX IF NOT EXISTS context_delivery_log_call ON context_delivery_log(account_id, session_key, call, ord) WHERE call != '';
CREATE INDEX IF NOT EXISTS context_delivery_log_session ON context_delivery_log(account_id, session_key);
CREATE INDEX IF NOT EXISTS context_delivery_log_object ON context_delivery_log(kind, object_id);
CREATE INDEX IF NOT EXISTS context_delivery_log_age ON context_delivery_log(delivered_at);
CREATE TABLE IF NOT EXISTS context_delivery_rollup(
  month TEXT NOT NULL,
  project TEXT NOT NULL,
  via TEXT NOT NULL,
  kind TEXT NOT NULL,
  object_id INTEGER NOT NULL DEFAULT 0,
  deliveries INTEGER NOT NULL DEFAULT 0,
  chars INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(month, project, via, kind, object_id)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS session_signals(
  project TEXT NOT NULL,
  session_ref TEXT NOT NULL,
  account_id INTEGER NOT NULL DEFAULT 0,
  harness TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  value TEXT NOT NULL,
  first_at TEXT NOT NULL,
  PRIMARY KEY(project, session_ref, account_id, harness, name)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS session_signals_age ON session_signals(first_at);
`

func ensureDeliveryLog(db *sql.DB) error {
	_, err := db.Exec(deliveryLogSchema)
	return err
}

const (
	// deliveryLogRetention is how long a row is kept in full. A session lives
	// days, the questions the log answers (is an entry used, does a route pay
	// for its characters) need weeks, and a row older than half a year has
	// been counted into its month by then.
	deliveryLogRetention = 180 * 24 * time.Hour
	// sessionSignalRetention is longer on purpose: a signal says nothing until
	// it is held against the session's length, and sessions are kept longer.
	sessionSignalRetention = 365 * 24 * time.Hour
	// maxFoldPerWrite bounds the work one write spends on folding old rows, so a
	// backlog (a restore, a clock jump) is worked off over several writes and
	// never stalls one.
	maxFoldPerWrite = 2000

	maxContextLogEvents     = 200
	maxDeliveryRowsPerSess  = 5000
	maxSessionSignalsPerReq = 8
	maxSignalValueRunes     = 64
	maxCallRunes            = 32
	// deliveryClockSkew is how far into the future a hook's clock may be before
	// its time is replaced by the server's.
	deliveryClockSkew = 5 * time.Minute
	// deliveryBackdate is how long a hook may hold a delivery back (a server
	// that was unreachable) before the server's time is used instead.
	deliveryBackdate = 30 * 24 * time.Hour
)

// SessionSignalNames are the environment variables a hook reports about its
// session. The concept names exactly these three; a name outside the list is
// refused so that this table cannot become a place environments are copied to.
var SessionSignalNames = []string{"CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION_ATTENDED", "CLAUDE_CODE_CHILD_SESSION"}

var (
	profileName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,15}$`)
	callName    = regexp.MustCompile(`^[a-zA-Z0-9]{1,` + fmt.Sprint(maxCallRunes) + `}$`)
)

// DeliveryEvent is one hook call that put context in front of the model: the
// parts it consisted of, adding up to what the budget counted. Call is the
// hook's own nonce for the event: a report sent twice (the answer got lost)
// is stored once. Subagent says the hook ran for a subagent of the session
// (its payload named an agent), which is how P0 finds out whether subagents
// share the account of the session they were started by.
type DeliveryEvent struct {
	At       string             `json:"at,omitempty"`
	Call     string             `json:"call"`
	Profile  string             `json:"profile,omitempty"`
	Subagent bool               `json:"subagent,omitempty"`
	Parts    []deliverylog.Part `json:"parts"`
}

// SessionSignal is the value of one environment variable of a session. An empty
// value says the variable was not set, which is information too.
type SessionSignal struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ContextLogBatch is what a hook reports for one session.
type ContextLogBatch struct {
	Project    string          `json:"project"`
	SessionRef string          `json:"session_ref"`
	Harness    string          `json:"harness"`
	Events     []DeliveryEvent `json:"events,omitempty"`
	Signals    []SessionSignal `json:"signals,omitempty"`
}

// Validate checks a batch before it is stored.
func (b ContextLogBatch) Validate() error {
	if strings.TrimSpace(b.Project) == "" || !ValidExternalID(b.SessionRef) {
		return fmt.Errorf("project and session_ref are required")
	}
	if len([]rune(b.Harness)) > maxToolNameRunes {
		return fmt.Errorf("harness too long")
	}
	if len(b.Events) == 0 && len(b.Signals) == 0 {
		return fmt.Errorf("nothing to report")
	}
	if len(b.Events) > maxContextLogEvents {
		return fmt.Errorf("at most %d deliveries per report", maxContextLogEvents)
	}
	for _, e := range b.Events {
		if !callName.MatchString(e.Call) {
			return fmt.Errorf("a delivery needs a call id of letters and digits")
		}
		if e.Profile != "" && !profileName.MatchString(e.Profile) {
			return fmt.Errorf("profile must be a short name")
		}
		if e.At != "" {
			if _, err := time.Parse(time.RFC3339, e.At); err != nil {
				return fmt.Errorf("at must be RFC 3339")
			}
		}
		if len(e.Parts) == 0 || len(e.Parts) > deliverylog.MaxPartsPerDelivery+1 {
			return fmt.Errorf("a delivery has between 1 and %d parts", deliverylog.MaxPartsPerDelivery+1)
		}
		for _, p := range e.Parts {
			if _, ok := p.Clean(); !ok {
				return fmt.Errorf("a delivery part is not valid")
			}
		}
	}
	if len(b.Signals) > maxSessionSignalsPerReq {
		return fmt.Errorf("at most %d signals per report", maxSessionSignalsPerReq)
	}
	for _, s := range b.Signals {
		if err := s.Valid(); err != nil {
			return err
		}
	}
	return nil
}

// Valid says whether the signal may be stored: a listed name and a short
// printable value.
func (s SessionSignal) Valid() error {
	if !knownSignal(s.Name) {
		return fmt.Errorf("unknown signal %q", s.Name)
	}
	if utf8.RuneCountInString(s.Value) > maxSignalValueRunes || !utf8.ValidString(s.Value) {
		return fmt.Errorf("signal value too long")
	}
	for _, r := range s.Value {
		if unicode.IsControl(r) {
			return fmt.Errorf("signal value must be printable")
		}
	}
	return nil
}

func knownSignal(name string) bool {
	for _, n := range SessionSignalNames {
		if n == name {
			return true
		}
	}
	return false
}

// RecordContextLog stores a batch. The caller checked the right to write for the
// batch's project. A delivery the log has already seen (same account, session
// and call) is ignored, and a session stops being logged at
// maxDeliveryRowsPerSess rows: both bound what one authenticated caller can
// make the table store.
func (s *Store) RecordContextLog(b ContextLogBatch, account int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{b, account}, func(d *Store, p []any) error {
			return d.RecordContextLog(p[0].(ContextLogBatch), p[1].(int64))
		})
	}
	if err := b.Validate(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	t := time.Now().UTC()
	at := t.Format(time.RFC3339)
	if len(b.Events) > 0 {
		var have int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM context_delivery_log WHERE account_id=? AND session_key=?`,
			account, b.SessionRef).Scan(&have); err != nil {
			return err
		}
		ins, err := tx.Prepare(`INSERT OR IGNORE INTO context_delivery_log(account_id, session_key, call, ord, project, harness, profile, subagent, via, kind, object_id, path, chars, delivered_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer ins.Close()
		for _, e := range b.Events {
			when := deliveredTime(e.At, t)
			sub := 0
			if e.Subagent {
				sub = 1
			}
			profile := e.Profile
			if profile == "" {
				profile = "full"
			}
			for i, p := range e.Parts {
				if have >= maxDeliveryRowsPerSess {
					break
				}
				p, _ = p.Clean()
				res, err := ins.Exec(account, b.SessionRef, e.Call, i, b.Project, b.Harness, profile, sub, p.Via, p.Kind, p.ID, p.Path, p.Chars, when)
				if err != nil {
					return err
				}
				if n, err := res.RowsAffected(); err == nil && n == 1 {
					have++
				}
			}
		}
		if err := foldDeliveryLog(tx, t.Add(-deliveryLogRetention).Format(time.RFC3339)); err != nil {
			return err
		}
	}
	if len(b.Signals) > 0 {
		for _, sg := range b.Signals {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO session_signals(project, session_ref, account_id, harness, name, value, first_at)
				VALUES(?,?,?,?,?,?,?)`, b.Project, b.SessionRef, account, b.Harness, sg.Name, sg.Value, at); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`DELETE FROM session_signals WHERE first_at < ?`,
			t.Add(-sessionSignalRetention).Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// deliveredTime takes the hook's time when it is plausible. The time matters
// (the judgement reads the transcript from the moment of delivery), but a hook
// with a wrong clock must not put rows into the future, where they would never
// age out, or so far back that they are folded away at once.
func deliveredTime(claimed string, now time.Time) string {
	if claimed != "" {
		if t, err := time.Parse(time.RFC3339, claimed); err == nil &&
			!t.After(now.Add(deliveryClockSkew)) && t.After(now.Add(-deliveryBackdate)) {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return now.Format(time.RFC3339)
}

// foldDeliveryLog moves rows older than cutoff into the monthly sums, at most
// maxFoldPerWrite of them, oldest first. The sums keep how often and how much
// was delivered per month, project, route, kind and object; the path and the
// session are dropped on purpose.
func foldDeliveryLog(tx *sql.Tx, cutoff string) error {
	rows, err := tx.Query(`SELECT id, substr(delivered_at,1,7), project, via, kind, object_id, chars
		FROM context_delivery_log WHERE delivered_at < ? ORDER BY delivered_at LIMIT ?`, cutoff, maxFoldPerWrite)
	if err != nil {
		return err
	}
	type key struct {
		month, project, via, kind string
		object                    int64
	}
	type sum struct{ n, chars int64 }
	sums := map[key]*sum{}
	var ids []int64
	for rows.Next() {
		var id, object, chars int64
		var k key
		if err := rows.Scan(&id, &k.month, &k.project, &k.via, &k.kind, &object, &chars); err != nil {
			rows.Close()
			return err
		}
		k.object = object
		if sums[k] == nil {
			sums[k] = &sum{}
		}
		sums[k].n++
		sums[k].chars += chars
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return err
	}
	up, err := tx.Prepare(`INSERT INTO context_delivery_rollup(month, project, via, kind, object_id, deliveries, chars)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(month, project, via, kind, object_id) DO UPDATE SET deliveries=deliveries+excluded.deliveries, chars=chars+excluded.chars`)
	if err != nil {
		return err
	}
	defer up.Close()
	for k, v := range sums {
		if _, err := up.Exec(k.month, k.project, k.via, k.kind, k.object, v.n, v.chars); err != nil {
			return err
		}
	}
	del, err := tx.Prepare(`DELETE FROM context_delivery_log WHERE id=?`)
	if err != nil {
		return err
	}
	defer del.Close()
	for _, id := range ids {
		if _, err := del.Exec(id); err != nil {
			return err
		}
	}
	return nil
}

// DeliveryLine is one route and kind in the report: how many parts, how many
// characters, and in how many sessions.
type DeliveryLine struct {
	Via, Kind    string
	Parts, Chars int
	Sessions     int
}

// DeliveryReport counts what was delivered since a day.
type DeliveryReport struct {
	Since string
	// Sessions are the sessions with at least one logged delivery in the window.
	Sessions int
	Chars    int
	// SubagentChars are the characters delivered by hooks that ran for a
	// subagent, and SubagentSessions the sessions that had any.
	SubagentChars, SubagentSessions int
	Lines                           []DeliveryLine
	// Folded are the monthly sums from before the retention, for months that
	// start in the window; they carry no session counts.
	Folded []DeliveryLine
}

// DeliveryReport reads the log of these projects since the given day
// (YYYY-MM-DD or RFC 3339). The caller decides which projects the viewer may
// see. The report counts and sums; it does not show paths or object numbers.
func (s *Store) DeliveryReport(projects []string, since string) (DeliveryReport, error) {
	if s.reader != nil {
		return s.reader.DeliveryReport(projects, since)
	}
	rep := DeliveryReport{Since: since}
	if len(projects) == 0 {
		return rep, nil
	}
	in, args := inList(projects)
	q := append(append([]any{}, args...), since)
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT account_id||'/'||session_key), COALESCE(SUM(chars),0)
		FROM context_delivery_log WHERE project IN (`+in+`) AND delivered_at >= ?`, q...).Scan(&rep.Sessions, &rep.Chars); err != nil {
		return rep, err
	}
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(chars),0), COUNT(DISTINCT account_id||'/'||session_key)
		FROM context_delivery_log WHERE project IN (`+in+`) AND delivered_at >= ? AND subagent=1`, q...).Scan(&rep.SubagentChars, &rep.SubagentSessions); err != nil {
		return rep, err
	}
	rows, err := s.db.Query(`SELECT via, kind, COUNT(*), SUM(chars), COUNT(DISTINCT account_id||'/'||session_key)
		FROM context_delivery_log WHERE project IN (`+in+`) AND delivered_at >= ? GROUP BY via, kind`, q...)
	if err != nil {
		return rep, err
	}
	for rows.Next() {
		var l DeliveryLine
		if err := rows.Scan(&l.Via, &l.Kind, &l.Parts, &l.Chars, &l.Sessions); err != nil {
			rows.Close()
			return rep, err
		}
		rep.Lines = append(rep.Lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return rep, err
	}
	sortDeliveryLines(rep.Lines)
	month := since
	if len(month) > 7 {
		month = month[:7]
	}
	fq := append(append([]any{}, args...), month)
	frows, err := s.db.Query(`SELECT via, kind, SUM(deliveries), SUM(chars) FROM context_delivery_rollup
		WHERE project IN (`+in+`) AND month >= ? GROUP BY via, kind`, fq...)
	if err != nil {
		return rep, err
	}
	defer frows.Close()
	for frows.Next() {
		var l DeliveryLine
		if err := frows.Scan(&l.Via, &l.Kind, &l.Parts, &l.Chars); err != nil {
			return rep, err
		}
		rep.Folded = append(rep.Folded, l)
	}
	sortDeliveryLines(rep.Folded)
	return rep, frows.Err()
}

func sortDeliveryLines(ls []DeliveryLine) {
	sort.Slice(ls, func(i, j int) bool {
		if ls[i].Chars != ls[j].Chars {
			return ls[i].Chars > ls[j].Chars
		}
		return ls[i].Via+ls[i].Kind < ls[j].Via+ls[j].Kind
	})
}

func inList(items []string) (string, []any) {
	args := make([]any, len(items))
	for i, p := range items {
		args[i] = p
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(items)), ","), args
}

// shortSessionPrompts is the number of prompts up to which a session counts as
// short: PAIN-73 found three quarters of all sessions at three or fewer.
const shortSessionPrompts = 3

// signalSettleTime is how long after its last activity a session counts as
// over. A session still running has few prompts because it is young, not
// because it is short.
const signalSettleTime = time.Hour

// SignalLine is one value of one signal: in how many sessions it was seen, and
// of those, how many are known to have had at most shortSessionPrompts prompts.
type SignalLine struct {
	Name, Value string
	Sessions    int
	// Measured are the sessions whose transcript is stored and which are over.
	Measured int
	// Short are the measured sessions with at most three prompts.
	Short int
}

// SignalReport counts the sessions per signal value.
type SignalReport struct {
	Since string
	Lines []SignalLine
}

// SignalReport reads the signals of these projects since a day, held against
// the number of prompts of the stored transcript (counted when the report is
// made, so it is the length the session ended with, not the one it had at
// start).
func (s *Store) SignalReport(projects []string, since string) (SignalReport, error) {
	if s.reader != nil {
		return s.reader.SignalReport(projects, since)
	}
	rep := SignalReport{Since: since}
	if len(projects) == 0 {
		return rep, nil
	}
	in, args := inList(projects)
	settled := time.Now().UTC().Add(-signalSettleTime).Format(time.RFC3339)
	q := append(append([]any{}, args...), since)
	rows, err := s.db.Query(`SELECT g.name, g.value, COUNT(*),
		SUM(CASE WHEN se.id IS NOT NULL AND se.last_seen_at < ? THEN 1 ELSE 0 END),
		SUM(CASE WHEN se.id IS NOT NULL AND se.last_seen_at < ? AND
			(SELECT COUNT(*) FROM (SELECT 1 FROM chunk_index ci WHERE ci.session_id=se.id AND ci.is_prompt=1 LIMIT ?)) <= ? THEN 1 ELSE 0 END)
		FROM session_signals g
		LEFT JOIN sessions se ON se.external_id=g.session_ref AND se.project=g.project AND (se.account_id=0 OR se.account_id=g.account_id)
		WHERE g.project IN (`+in+`) AND g.first_at >= ?
		GROUP BY g.name, g.value`,
		append([]any{settled, settled, shortSessionPrompts + 1, shortSessionPrompts}, q...)...)
	if err != nil {
		return rep, err
	}
	defer rows.Close()
	for rows.Next() {
		var l SignalLine
		if err := rows.Scan(&l.Name, &l.Value, &l.Sessions, &l.Measured, &l.Short); err != nil {
			return rep, err
		}
		rep.Lines = append(rep.Lines, l)
	}
	if err := rows.Err(); err != nil {
		return rep, err
	}
	sort.Slice(rep.Lines, func(i, j int) bool {
		a, b := rep.Lines[i], rep.Lines[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Sessions > b.Sessions
	})
	return rep, nil
}

// ToolSummary is one tool in the tool report: the calls of all its actions,
// harnesses and subagents, and the number of different sessions that made one.
type ToolSummary struct {
	Tool     string        `json:"tool"`
	Action   string        `json:"action,omitempty"`
	Sessions int           `json:"sessions"`
	Calls    int           `json:"calls"`
	Actions  []ToolSummary `json:"actions,omitempty"`
}

// ToolReport is tool_call_counts since a day, per tool. It answers the question
// the gate of DEC-8 asks of production: in how many sessions was a tool called
// at all.
type ToolReport struct {
	Since string `json:"since"`
	// Sessions are the sessions of the projects active since then whose
	// transcript is stored, Counted the sessions with at least one counted
	// call. A session that called no ghosttree tool is in the first and not in
	// the second, which is what a share of "all sessions" needs.
	Sessions int           `json:"sessions"`
	Counted  int           `json:"counted"`
	Tools    []ToolSummary `json:"tools"`
}

// ToolReportSince reads tool_call_counts of these projects for sessions that
// were active since the day (YYYY-MM-DD or RFC 3339).
func (s *Store) ToolReportSince(projects []string, since string) (ToolReport, error) {
	if s.reader != nil {
		return s.reader.ToolReportSince(projects, since)
	}
	rep := ToolReport{Since: since, Tools: []ToolSummary{}}
	if len(projects) == 0 {
		return rep, nil
	}
	in, args := inList(projects)
	q := append(append([]any{}, args...), since)
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT project||'/'||session_ref) FROM tool_call_counts
		WHERE project IN (`+in+`) AND last_at >= ?`, q...).Scan(&rep.Counted); err != nil {
		return rep, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE project IN (`+in+`) AND last_seen_at >= ?`, q...).Scan(&rep.Sessions); err != nil {
		return rep, err
	}
	rows, err := s.db.Query(`SELECT tool, action, SUM(calls), COUNT(DISTINCT project||'/'||session_ref)
		FROM tool_call_counts WHERE project IN (`+in+`) AND last_at >= ? GROUP BY tool, action`, q...)
	if err != nil {
		return rep, err
	}
	defer rows.Close()
	byTool := map[string]*ToolSummary{}
	for rows.Next() {
		var a ToolSummary
		if err := rows.Scan(&a.Tool, &a.Action, &a.Calls, &a.Sessions); err != nil {
			return rep, err
		}
		t := byTool[a.Tool]
		if t == nil {
			t = &ToolSummary{Tool: a.Tool}
			byTool[a.Tool] = t
		}
		t.Calls += a.Calls
		t.Actions = append(t.Actions, a)
	}
	if err := rows.Err(); err != nil {
		return rep, err
	}
	// The sessions of a tool are counted over all its actions at once; adding
	// the actions' sessions up would count a session twice.
	trows, err := s.db.Query(`SELECT tool, COUNT(DISTINCT project||'/'||session_ref)
		FROM tool_call_counts WHERE project IN (`+in+`) AND last_at >= ? GROUP BY tool`, q...)
	if err != nil {
		return rep, err
	}
	defer trows.Close()
	for trows.Next() {
		var tool string
		var n int
		if err := trows.Scan(&tool, &n); err != nil {
			return rep, err
		}
		if t := byTool[tool]; t != nil {
			t.Sessions = n
		}
	}
	if err := trows.Err(); err != nil {
		return rep, err
	}
	for _, t := range byTool {
		sort.Slice(t.Actions, func(i, j int) bool {
			if t.Actions[i].Calls != t.Actions[j].Calls {
				return t.Actions[i].Calls > t.Actions[j].Calls
			}
			return t.Actions[i].Action < t.Actions[j].Action
		})
		rep.Tools = append(rep.Tools, *t)
	}
	sort.Slice(rep.Tools, func(i, j int) bool {
		a, b := rep.Tools[i], rep.Tools[j]
		if a.Sessions != b.Sessions {
			return a.Sessions > b.Sessions
		}
		if a.Calls != b.Calls {
			return a.Calls > b.Calls
		}
		return a.Tool < b.Tool
	})
	return rep, nil
}

// RecordContextLog stores a report for a project the caller may write knowledge
// in: like the tool counts, the sums appear in the project's statistics, so a
// stranger or a guest must not add to them.
func (a *ProjectAccess) RecordContextLog(b ContextLogBatch) error {
	if err := b.Validate(); err != nil {
		return err
	}
	if err := a.Check(b.Project, ResKnowledge, ActCreate, Object{Own: true}); err != nil {
		return err
	}
	return a.st.RecordContextLog(b, a.account)
}

// DeliveryReport, SignalReport and ToolReportSince read over the projects this
// viewer may see in full, the same selection as the tool statistics. They
// return sums only: no path, object number or session leaves the store here.
func (a *ProjectAccess) DeliveryReport(project, since string) (DeliveryReport, error) {
	projects, err := a.statsProjects(project)
	if err != nil {
		return DeliveryReport{}, err
	}
	return a.st.DeliveryReport(projects, since)
}

func (a *ProjectAccess) SignalReport(project, since string) (SignalReport, error) {
	projects, err := a.statsProjects(project)
	if err != nil {
		return SignalReport{}, err
	}
	return a.st.SignalReport(projects, since)
}

func (a *ProjectAccess) ToolReportSince(project, since string) (ToolReport, error) {
	projects, err := a.statsProjects(project)
	if err != nil {
		return ToolReport{}, err
	}
	return a.st.ToolReportSince(projects, since)
}
