package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// toolCountSchema (REQ-436): how often a session called ghosttree's tools, one
// row per session, tool, action, harness and whether a subagent made the call.
// The table is new; nothing existing changes. session_ref is the harness
// session id as the hooks and ctx mcp know it, the same value as
// sessions.external_id once the transcript is uploaded.
const toolCountSchema = `
CREATE TABLE IF NOT EXISTS tool_call_counts(
  project TEXT NOT NULL,
  session_ref TEXT NOT NULL,
  harness TEXT NOT NULL DEFAULT '',
  tool TEXT NOT NULL,
  action TEXT NOT NULL DEFAULT '',
  subagent INTEGER NOT NULL DEFAULT 0 CHECK(subagent IN (0,1)),
  calls INTEGER NOT NULL DEFAULT 0,
  account_id INTEGER NOT NULL DEFAULT 0,
  first_at TEXT NOT NULL,
  last_at TEXT NOT NULL,
  PRIMARY KEY(project, session_ref, harness, tool, action, subagent)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS tool_call_counts_session ON tool_call_counts(session_ref);
`

func ensureToolCounts(db *sql.DB) error {
	_, err := db.Exec(toolCountSchema)
	return err
}

// ToolUse is a count of calls of one tool and action in one session.
type ToolUse struct {
	Tool     string `json:"tool"`
	Action   string `json:"action,omitempty"`
	Subagent bool   `json:"subagent,omitempty"`
	Calls    int    `json:"calls"`
}

// ToolUseBatch is what a hook or ctx mcp reports for one session.
type ToolUseBatch struct {
	Project    string    `json:"project"`
	SessionRef string    `json:"session_ref"`
	Harness    string    `json:"harness"`
	Uses       []ToolUse `json:"uses"`
}

// Limits of one report: a hook sends what one session gathered since its last
// report, which is far below these.
const (
	maxToolUsesPerBatch = 200
	maxToolCallsPerUse  = 10_000
	maxToolNameRunes    = 64
)

// Validate checks a batch before it is stored.
func (b ToolUseBatch) Validate() error {
	if strings.TrimSpace(b.Project) == "" || !ValidExternalID(b.SessionRef) {
		return fmt.Errorf("project and session_ref are required")
	}
	if len([]rune(b.Harness)) > maxToolNameRunes {
		return fmt.Errorf("harness too long")
	}
	if len(b.Uses) == 0 || len(b.Uses) > maxToolUsesPerBatch {
		return fmt.Errorf("between 1 and %d uses per report", maxToolUsesPerBatch)
	}
	for _, u := range b.Uses {
		if strings.TrimSpace(u.Tool) == "" || len([]rune(u.Tool)) > maxToolNameRunes || len([]rune(u.Action)) > maxToolNameRunes {
			return fmt.Errorf("tool and action must be short names")
		}
		if u.Calls < 1 || u.Calls > maxToolCallsPerUse {
			return fmt.Errorf("calls must be between 1 and %d", maxToolCallsPerUse)
		}
	}
	return nil
}

// RecordToolUses adds the calls of a batch to the session's counts. The caller
// checked the right to write for the batch's project.
func (s *Store) RecordToolUses(b ToolUseBatch, account int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{b, account}, func(d *Store, p []any) error {
			return d.RecordToolUses(p[0].(ToolUseBatch), p[1].(int64))
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
	at := now()
	for _, u := range b.Uses {
		sub := 0
		if u.Subagent {
			sub = 1
		}
		if _, err := tx.Exec(`INSERT INTO tool_call_counts(project, session_ref, harness, tool, action, subagent, calls, account_id, first_at, last_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(project, session_ref, harness, tool, action, subagent) DO UPDATE SET calls=calls+excluded.calls, last_at=excluded.last_at`,
			b.Project, b.SessionRef, b.Harness, u.Tool, u.Action, sub, u.Calls, account, at, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ToolUseLine is one line of the statistics: calls of a tool and action from
// one harness, by subagents or not, and in how many sessions.
type ToolUseLine struct {
	Tool, Action, Harness string
	Subagent              bool
	Calls, Sessions       int
}

// ToolUseHarness says, for one harness, how many sessions were uploaded since
// counting began and how many of them called a ghosttree tool.
type ToolUseHarness struct {
	Harness             string
	Sessions, WithCalls int
}

// ToolUseReport is the statistics over some projects.
type ToolUseReport struct {
	// Since is when the window starts: the first report in these projects or
	// the start of the window, whichever is later. Empty when nothing was
	// counted yet.
	Since   string
	Lines   []ToolUseLine
	Harness []ToolUseHarness
	// Sessions are the uploaded sessions active since Since, Silent those of them
	// that called no ghosttree tool.
	Sessions, Silent int
}

// ToolUseStats reads the statistics of these projects over the last days.
// The caller decides which projects the viewer may see.
func (s *Store) ToolUseStats(projects []string, days int) (ToolUseReport, error) {
	if s.reader != nil {
		return s.reader.ToolUseStats(projects, days)
	}
	var rep ToolUseReport
	if len(projects) == 0 {
		return rep, nil
	}
	if days <= 0 {
		days = 30
	}
	from := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
	in := strings.TrimSuffix(strings.Repeat("?,", len(projects)), ",")
	args := make([]any, 0, len(projects)+1)
	for _, p := range projects {
		args = append(args, p)
	}
	var first sql.NullString
	if err := s.db.QueryRow(`SELECT MIN(first_at) FROM tool_call_counts WHERE project IN (`+in+`)`, args...).Scan(&first); err != nil {
		return rep, err
	}
	if !first.Valid || first.String == "" {
		return rep, nil
	}
	start := first.String
	if from > start {
		start = from
	}
	rep.Since = start
	q := append(append([]any{}, args...), start)
	rows, err := s.db.Query(`SELECT tool, action, harness, subagent, SUM(calls), COUNT(DISTINCT session_ref)
		FROM tool_call_counts WHERE project IN (`+in+`) AND last_at >= ?
		GROUP BY tool, action, harness, subagent`, q...)
	if err != nil {
		return rep, err
	}
	for rows.Next() {
		var l ToolUseLine
		var sub int
		if err := rows.Scan(&l.Tool, &l.Action, &l.Harness, &sub, &l.Calls, &l.Sessions); err != nil {
			rows.Close()
			return rep, err
		}
		l.Subagent = sub == 1
		rep.Lines = append(rep.Lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return rep, err
	}
	sort.Slice(rep.Lines, func(i, j int) bool {
		a, b := rep.Lines[i], rep.Lines[j]
		if a.Calls != b.Calls {
			return a.Calls > b.Calls
		}
		return a.Tool+" "+a.Action+" "+a.Harness < b.Tool+" "+b.Action+" "+b.Harness
	})
	hrows, err := s.db.Query(`SELECT se.harness, COUNT(*),
		SUM(CASE WHEN EXISTS (SELECT 1 FROM tool_call_counts u WHERE u.session_ref=se.external_id) THEN 1 ELSE 0 END)
		FROM sessions se WHERE se.project IN (`+in+`) AND se.last_seen_at >= ?
		GROUP BY se.harness ORDER BY se.harness`, q...)
	if err != nil {
		return rep, err
	}
	defer hrows.Close()
	for hrows.Next() {
		var h ToolUseHarness
		if err := hrows.Scan(&h.Harness, &h.Sessions, &h.WithCalls); err != nil {
			return rep, err
		}
		rep.Harness = append(rep.Harness, h)
		rep.Sessions += h.Sessions
		rep.Silent += h.Sessions - h.WithCalls
	}
	return rep, hrows.Err()
}

// RecordToolUses stores a report for a project the caller may upload sessions
// to.
func (a *ProjectAccess) RecordToolUses(b ToolUseBatch) error {
	if err := b.Validate(); err != nil {
		return err
	}
	if err := a.Check(b.Project, ResSessionMeta, ActCreate, Object{Own: true}); err != nil {
		return err
	}
	return a.st.RecordToolUses(b, a.account)
}

// ToolUseStats is the statistics over the projects this viewer may see in
// full, like ActorStats.
func (a *ProjectAccess) ToolUseStats(project string, days int) (ToolUseReport, error) {
	projects, err := a.statsProjects(project)
	if err != nil {
		return ToolUseReport{}, err
	}
	return a.st.ToolUseStats(projects, days)
}
