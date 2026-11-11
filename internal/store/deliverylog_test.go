package store

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/deliverylog"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const logProject = "github.com/dw/p"

func logParts(chars ...int) []deliverylog.Part {
	var out []deliverylog.Part
	for i, c := range chars {
		out = append(out, deliverylog.Part{Via: deliverylog.ViaBootstrap, Kind: deliverylog.KindKnowledge, ID: int64(i + 1), Chars: c})
	}
	return out
}

func logCount(t *testing.T, s *Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// REQ-636: the same report sent twice (the answer got lost) is stored once, a
// part keeps its route, kind, object and size, and a path stays only on ghosts.
func TestRecordContextLogStoresPartsOnceAndKeepsTheirShape(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b := ContextLogBatch{Project: logProject, SessionRef: "sess-1", Harness: "claude", Events: []DeliveryEvent{
		{Call: "aaaa1111", Parts: []deliverylog.Part{
			{Via: deliverylog.ViaBootstrap, Kind: deliverylog.KindKnowledge, ID: 7, Chars: 120},
			{Via: deliverylog.ViaBootstrap, Kind: deliverylog.KindFrame, Chars: 30},
		}},
		{Call: "bbbb2222", Subagent: true, Parts: []deliverylog.Part{
			{Via: deliverylog.ViaPath, Kind: deliverylog.KindGhost, Path: "internal/store/store.go", Chars: 400},
		}},
	}}
	for range 2 {
		if err := s.RecordContextLog(b, 5); err != nil {
			t.Fatal(err)
		}
	}
	if n := logCount(t, s, `SELECT COUNT(*) FROM context_delivery_log`); n != 3 {
		t.Fatalf("rows = %d, want 3 (a repeated report is stored once)", n)
	}
	if n := logCount(t, s, `SELECT SUM(chars) FROM context_delivery_log WHERE session_key='sess-1' AND subagent=0`); n != 150 {
		t.Errorf("session chars = %d", n)
	}
	var path string
	var sub int
	if err := s.db.QueryRow(`SELECT path, subagent FROM context_delivery_log WHERE kind='ghost'`).Scan(&path, &sub); err != nil || path != "internal/store/store.go" || sub != 1 {
		t.Errorf("ghost row: %q %d %v", path, sub, err)
	}
	if n := logCount(t, s, `SELECT COUNT(*) FROM context_delivery_log WHERE kind != 'ghost' AND path != ''`); n != 0 {
		t.Error("a path was stored on something that is not a path description")
	}
}

func TestRecordContextLogRefusesWhatItShouldNotStore(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	ok := DeliveryEvent{Call: "aaaa", Parts: logParts(5)}
	for name, b := range map[string]ContextLogBatch{
		"no project":     {SessionRef: "s", Events: []DeliveryEvent{ok}},
		"no session":     {Project: logProject, Events: []DeliveryEvent{ok}},
		"nothing":        {Project: logProject, SessionRef: "s"},
		"no call":        {Project: logProject, SessionRef: "s", Events: []DeliveryEvent{{Parts: logParts(5)}}},
		"no parts":       {Project: logProject, SessionRef: "s", Events: []DeliveryEvent{{Call: "a"}}},
		"bad part":       {Project: logProject, SessionRef: "s", Events: []DeliveryEvent{{Call: "a", Parts: []deliverylog.Part{{Via: "x", Kind: "knowledge"}}}}},
		"bad time":       {Project: logProject, SessionRef: "s", Events: []DeliveryEvent{{Call: "a", At: "yesterday", Parts: logParts(5)}}},
		"unknown signal": {Project: logProject, SessionRef: "s", Signals: []SessionSignal{{Name: "HOME", Value: "/home/x"}}},
	} {
		if err := s.RecordContextLog(b, 1); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if n := logCount(t, s, `SELECT COUNT(*) FROM context_delivery_log`); n != 0 {
		t.Errorf("rows after refusals: %d", n)
	}
}

// What one caller can make the table store is bounded per session; a hook's
// clock cannot put rows in the future or far in the past.
func TestRecordContextLogBoundsASessionAndDistrustsTheClock(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	for i := range 60 {
		var events []DeliveryEvent
		for j := range 100 {
			events = append(events, DeliveryEvent{Call: fmt.Sprintf("c%dx%d", i, j), Parts: logParts(1, 1)})
		}
		if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: "flood", Events: events[:100]}, 1); err != nil {
			t.Fatal(err)
		}
	}
	if n := logCount(t, s, `SELECT COUNT(*) FROM context_delivery_log WHERE session_key='flood'`); n != maxDeliveryRowsPerSess {
		t.Errorf("a session stored %d rows, cap %d", n, maxDeliveryRowsPerSess)
	}
	future := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	past := time.Now().UTC().Add(-90 * 24 * time.Hour).Format(time.RFC3339)
	recent := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: "clock", Events: []DeliveryEvent{
		{Call: "f", At: future, Parts: logParts(1)}, {Call: "p", At: past, Parts: logParts(1)}, {Call: "r", At: recent, Parts: logParts(1)},
	}}, 1); err != nil {
		t.Fatal(err)
	}
	limit := time.Now().UTC().Add(time.Minute).Format(time.RFC3339)
	if n := logCount(t, s, `SELECT COUNT(*) FROM context_delivery_log WHERE session_key='clock' AND delivered_at > ?`, limit); n != 0 {
		t.Error("a row sits in the future")
	}
	if n := logCount(t, s, `SELECT COUNT(*) FROM context_delivery_log WHERE session_key='clock' AND delivered_at = ?`, recent); n != 1 {
		t.Error("a plausible time must be kept")
	}
}

// The log stays bounded: rows older than the retention are folded into monthly
// sums that keep how often and how much, and no path and no session. The work
// per write is limited.
func TestContextLogFoldsRowsOlderThanTheRetention(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	old := time.Now().UTC().Add(-deliveryLogRetention - 40*24*time.Hour).Format(time.RFC3339)
	for i := range 5 {
		if _, err := s.db.Exec(`INSERT INTO context_delivery_log(account_id, session_key, call, ord, project, via, kind, object_id, path, chars, delivered_at)
			VALUES(1,?,?,0,?,?,?,?,?,?,?)`, fmt.Sprintf("old-%d", i), fmt.Sprintf("o%d", i), logProject,
			deliverylog.ViaPath, deliverylog.KindGhost, 0, "secret/dir/file.go", 100+i, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO context_delivery_log(account_id, session_key, call, ord, project, via, kind, object_id, chars, delivered_at)
		VALUES(1,'old-k','k',0,?,?,?,9,50,?)`, logProject, deliverylog.ViaBootstrap, deliverylog.KindKnowledge, old); err != nil {
		t.Fatal(err)
	}
	// An ordinary write folds them.
	if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: "now", Events: []DeliveryEvent{{Call: "n", Parts: logParts(10)}}}, 1); err != nil {
		t.Fatal(err)
	}
	if n := logCount(t, s, `SELECT COUNT(*) FROM context_delivery_log`); n != 1 {
		t.Fatalf("raw rows after folding: %d, want only the new one", n)
	}
	var deliveries, chars int
	if err := s.db.QueryRow(`SELECT deliveries, chars FROM context_delivery_rollup WHERE kind='ghost'`).Scan(&deliveries, &chars); err != nil || deliveries != 5 || chars != 510 {
		t.Errorf("ghost rollup: %d deliveries, %d chars, %v", deliveries, chars, err)
	}
	var obj int
	if err := s.db.QueryRow(`SELECT object_id FROM context_delivery_rollup WHERE kind='knowledge'`).Scan(&obj); err != nil || obj != 9 {
		t.Errorf("a knowledge entry keeps its number in the rollup: %d %v", obj, err)
	}
	cols, err := s.db.Query(`SELECT name FROM pragma_table_info('context_delivery_rollup')`)
	if err != nil {
		t.Fatal(err)
	}
	defer cols.Close()
	for cols.Next() {
		var name string
		cols.Scan(&name)
		if name == "path" || name == "session_key" {
			t.Errorf("the rollup keeps %q", name)
		}
	}
	// The report names a folded month only when the whole month lies in its
	// window: a window that starts inside the month cannot tell which part of
	// the sum falls in it, so it leaves the month out and says where it starts.
	foldedParts := func(since string) (int, string) {
		t.Helper()
		rep, err := s.DeliveryReport([]string{logProject}, since)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, l := range rep.Folded {
			n += l.Parts
		}
		return n, rep.FoldedFrom
	}
	month := old[:7]
	if n, from := foldedParts(month + "-01"); n != 6 || from != month {
		t.Errorf("window from the first of the month: %d folded parts from %q, want 6 from %q", n, from, month)
	}
	if n, from := foldedParts(month + "-01T00:00:00Z"); n != 6 || from != month {
		t.Errorf("window from midnight of the first: %d from %q", n, from)
	}
	if n, from := foldedParts(month + "-01T06:00:00Z"); n != 0 || from == month {
		t.Errorf("window that starts after midnight of the first: %d folded parts from %q, want the month left out", n, from)
	}
	oldDay, _ := time.Parse(time.RFC3339, old)
	if oldDay.Day() > 1 {
		if n, from := foldedParts(old[:10]); n != 0 || from == month {
			t.Errorf("window that starts inside the month: %d folded parts from %q, want the month left out", n, from)
		}
	}
}

func TestFirstWholeMonth(t *testing.T) {
	for since, want := range map[string]string{
		"2026-03-01":           "2026-03",
		"2026-03-01T00:00:00Z": "2026-03",
		"2026-03-01T00:00:01Z": "2026-04",
		"2026-03-02":           "2026-04",
		"2026-12-15":           "2027-01",
		"2026-12-31T23:00:00Z": "2027-01",
	} {
		if got := firstWholeMonth(since); got != want {
			t.Errorf("firstWholeMonth(%q) = %q, want %q", since, got, want)
		}
	}
}

func TestFoldingIsBoundedPerWrite(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	old := time.Now().UTC().Add(-deliveryLogRetention - 24*time.Hour).Format(time.RFC3339)
	tx, _ := s.db.Begin()
	for i := range maxFoldPerWrite + 50 {
		tx.Exec(`INSERT INTO context_delivery_log(account_id, session_key, call, ord, project, via, kind, chars, delivered_at) VALUES(1,?,?,0,?,?,?,1,?)`,
			fmt.Sprintf("s%d", i), fmt.Sprintf("c%d", i), logProject, deliverylog.ViaBootstrap, deliverylog.KindFrame, old)
	}
	tx.Commit()
	if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: "now", Events: []DeliveryEvent{{Call: "n", Parts: logParts(1)}}}, 1); err != nil {
		t.Fatal(err)
	}
	if n := logCount(t, s, `SELECT COUNT(*) FROM context_delivery_log WHERE delivered_at = ?`, old); n != 50 {
		t.Errorf("one write left %d old rows, want 50 (the backlog is worked off over several writes)", n)
	}
}

func TestDeliveryReportSumsPerRouteAndKindForTheGivenProjectsOnly(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	rec := func(project, session string, sub bool, parts ...deliverylog.Part) {
		t.Helper()
		if err := s.RecordContextLog(ContextLogBatch{Project: project, SessionRef: session, Events: []DeliveryEvent{
			{Call: session + fmt.Sprint(len(parts)) + fmt.Sprint(sub), Subagent: sub, Parts: parts}}}, 1); err != nil {
			t.Fatal(err)
		}
	}
	k := func(c int) deliverylog.Part {
		return deliverylog.Part{Via: deliverylog.ViaBootstrap, Kind: deliverylog.KindKnowledge, ID: 1, Chars: c}
	}
	g := func(c int) deliverylog.Part {
		return deliverylog.Part{Via: deliverylog.ViaPath, Kind: deliverylog.KindGhost, Path: "a.go", Chars: c}
	}
	rec(logProject, "s1", false, k(100), g(300))
	rec(logProject, "s2", false, k(50))
	rec(logProject, "s3", true, g(70))
	rec("github.com/dw/other", "s4", false, k(9999))
	rep, err := s.DeliveryReport([]string{logProject}, "2020-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Sessions != 3 || rep.Chars != 520 || rep.SubagentChars != 70 || rep.SubagentSessions != 1 {
		t.Errorf("totals: %+v", rep)
	}
	byKey := map[string]DeliveryLine{}
	for _, l := range rep.Lines {
		byKey[l.Via+"/"+l.Kind] = l
	}
	if l := byKey["path/ghost"]; l.Parts != 2 || l.Chars != 370 || l.Sessions != 2 {
		t.Errorf("path/ghost: %+v", l)
	}
	if l := byKey["bootstrap/knowledge"]; l.Parts != 2 || l.Chars != 150 || l.Sessions != 2 {
		t.Errorf("bootstrap/knowledge: %+v", l)
	}
	if empty, err := s.DeliveryReport(nil, "2020-01-01"); err != nil || empty.Chars != 0 {
		t.Errorf("no projects, no data: %+v %v", empty, err)
	}
}

// P0 must tell which signal separates the short sessions: per value, how many
// finished sessions with a stored transcript had at most three prompts. A
// running session and one without a transcript are not counted as short.
func TestSignalReportHoldsSignalsAgainstSessionLength(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	hourAgo := time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339)
	session := func(ref string, prompts int, lastSeen string) {
		id, err := s.UpsertSession(Session{Harness: "claude", ExternalID: ref, Scope: scope.Axes{Project: logProject}})
		if err != nil {
			t.Fatal(err)
		}
		for i := range prompts {
			if _, err := s.db.Exec(`INSERT INTO chunk_index(chunk_id, session_id, seq, role, is_prompt) VALUES(?,?,?,?,1)`,
				id*1000+int64(i), id, i, "user"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.db.Exec(`UPDATE sessions SET last_seen_at=? WHERE id=?`, lastSeen, id); err != nil {
			t.Fatal(err)
		}
	}
	signal := func(ref, entry string) {
		t.Helper()
		if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: ref, Harness: "claude", Signals: []SessionSignal{
			{Name: "CLAUDE_CODE_ENTRYPOINT", Value: entry}, {Name: "CLAUDE_CODE_CHILD_SESSION", Value: ""}}}, 1); err != nil {
			t.Fatal(err)
		}
	}
	session("a", 1, hourAgo)
	session("b", 3, hourAgo)
	session("c", 4, hourAgo)
	session("d", 40, hourAgo)
	session("e", 1, time.Now().UTC().Format(time.RFC3339))     // still running
	for _, r := range []string{"a", "b", "c", "d", "e", "f"} { // f: no transcript
		entry := "cli"
		if r == "a" || r == "b" || r == "e" || r == "f" {
			entry = "sdk-cli"
		}
		signal(r, entry)
	}
	rep, err := s.SignalReport([]string{logProject}, "2020-01-01")
	if err != nil {
		t.Fatal(err)
	}
	get := func(name, value string) SignalLine {
		for _, l := range rep.Lines {
			if l.Name == name && l.Value == value {
				return l
			}
		}
		t.Fatalf("no line %s=%q in %+v", name, value, rep.Lines)
		return SignalLine{}
	}
	if l := get("CLAUDE_CODE_ENTRYPOINT", "sdk-cli"); l.Sessions != 4 || l.Measured != 2 || l.Short != 2 {
		t.Errorf("sdk-cli: %+v (a and b are short, e is running, f has no transcript)", l)
	}
	if l := get("CLAUDE_CODE_ENTRYPOINT", "cli"); l.Sessions != 2 || l.Measured != 2 || l.Short != 0 {
		t.Errorf("cli: %+v", l)
	}
	if l := get("CLAUDE_CODE_CHILD_SESSION", ""); l.Sessions != 6 {
		t.Errorf("an unset variable is a value too: %+v", l)
	}
	// The first report of a session wins: a later one cannot rewrite it.
	signal("a", "cli")
	if l := get2(t, s, "CLAUDE_CODE_ENTRYPOINT", "cli"); l.Sessions != 2 {
		t.Errorf("a later report changed a recorded signal: %+v", l)
	}
}

func get2(t *testing.T, s *Store, name, value string) SignalLine {
	t.Helper()
	rep, err := s.SignalReport([]string{logProject}, "2020-01-01")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range rep.Lines {
		if l.Name == name && l.Value == value {
			return l
		}
	}
	return SignalLine{}
}

// The gate of the tool-loading decision: per tool, in how many sessions it was
// called at all, over a window, with sessions that called nothing in the
// denominator.
func TestToolReportSinceCountsSessionsPerToolOverAllActions(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	for _, ref := range []string{"s1", "s2", "s3", "s4"} {
		if _, err := s.UpsertSession(Session{Harness: "claude", ExternalID: ref, Scope: scope.Axes{Project: logProject}}); err != nil {
			t.Fatal(err)
		}
	}
	use := func(ref, tool, action string, n int, sub bool) {
		t.Helper()
		if err := s.RecordToolUses(ToolUseBatch{Project: logProject, SessionRef: ref, Harness: "claude",
			Uses: []ToolUse{{Tool: tool, Action: action, Calls: n, Subagent: sub}}}, 1); err != nil {
			t.Fatal(err)
		}
	}
	use("s1", "package", "get", 2, false)
	use("s1", "package", "create", 1, false)
	use("s1", "package", "get", 3, true)
	use("s2", "package", "get", 1, false)
	use("s2", "runbook", "report", 4, false)
	use("s3", "request", "get", 5, false)
	rep, err := s.ToolReportSince([]string{logProject}, "2020-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Sessions != 4 || rep.Counted != 3 {
		t.Errorf("sessions %d counted %d, want 4 and 3 (s4 called nothing)", rep.Sessions, rep.Counted)
	}
	byTool := map[string]ToolSummary{}
	for _, tl := range rep.Tools {
		byTool[tl.Tool] = tl
	}
	if p := byTool["package"]; p.Sessions != 2 || p.Calls != 7 || len(p.Actions) != 2 {
		t.Errorf("package: %+v (a session that used two actions counts once)", p)
	}
	if r := byTool["runbook"]; r.Sessions != 1 || r.Calls != 4 {
		t.Errorf("runbook: %+v", r)
	}
	if rep.Tools[0].Tool != "package" {
		t.Errorf("most sessions first: %+v", rep.Tools)
	}
	future, err := s.ToolReportSince([]string{logProject}, "2999-01-01")
	if err != nil || len(future.Tools) != 0 || future.Counted != 0 {
		t.Errorf("a window in the future is empty: %+v %v", future, err)
	}
}

// A signal value is a short token or it is "(invalid)": a value is never a
// reason to lose the session's other signals, and never text that is listed.
func TestSignalValuesAreShortTokensOrInvalid(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	for i, c := range []struct{ value, want string }{
		{"sdk-cli", "sdk-cli"}, {"", ""}, {"1", "1"}, {"a_b.c-D9", "a_b.c-D9"},
		{strings.Repeat("x", 32), strings.Repeat("x", 32)},
		{strings.Repeat("x", 33), SignalInvalid}, {"a b", SignalInvalid}, {"a\x00b", SignalInvalid},
		{"/home/robin/secret", SignalInvalid}, {"süß", SignalInvalid}, {"a\nb", SignalInvalid}, {"\xff", SignalInvalid},
		{SignalInvalid, SignalInvalid},
	} {
		ref := fmt.Sprintf("sig-%d", i)
		err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: ref, Harness: "claude", Signals: []SessionSignal{
			{Name: "CLAUDE_CODE_ENTRYPOINT", Value: c.value}, {Name: "CLAUDE_CODE_CHILD_SESSION", Value: "1"}}}, 1)
		if err != nil {
			t.Fatalf("%q: a value must not refuse the report: %v", c.value, err)
		}
		var got string
		if err := s.db.QueryRow(`SELECT value FROM session_signals WHERE session_ref=? AND name='CLAUDE_CODE_ENTRYPOINT'`, ref).Scan(&got); err != nil || got != c.want {
			t.Errorf("value %q stored as %q (%v), want %q", c.value, got, err, c.want)
		}
		if n := logCount(t, s, `SELECT COUNT(*) FROM session_signals WHERE session_ref=? AND name='CLAUDE_CODE_CHILD_SESSION' AND value='1'`, ref); n != 1 {
			t.Errorf("%q: the other signal of the report was lost", c.value)
		}
	}
	if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: "s", Signals: []SessionSignal{{Name: "HOME", Value: "x"}}}, 1); err == nil {
		t.Error("an unlisted name was accepted")
	}
}

// What one account stores for one project in one day is bounded, whatever
// session_ref and object numbers it picks; another account or project has its
// own allowance.
func TestRecordContextLogBoundsAnAccountsDay(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	const other = "github.com/dw/other"
	write := func(project string, account int64, n int, tag string) {
		t.Helper()
		var events []DeliveryEvent
		for i := range n {
			events = append(events, DeliveryEvent{Call: fmt.Sprintf("%s%d", tag, i), Parts: logParts(1)})
		}
		for len(events) > 0 {
			k := min(len(events), maxContextLogEvents)
			if err := s.RecordContextLog(ContextLogBatch{Project: project, SessionRef: fmt.Sprintf("%s-%d", tag, len(events)), Events: events[:k]}, account); err != nil {
				t.Fatal(err)
			}
			events = events[k:]
		}
	}
	// A new session each batch, so that the per-session limit does not apply.
	write(logProject, 1, maxDeliveryRowsPerDay+500, "a")
	if n := logCount(t, s, `SELECT COUNT(*) FROM context_delivery_log WHERE account_id=1 AND project=?`, logProject); n != maxDeliveryRowsPerDay {
		t.Errorf("a day stored %d delivery rows, cap %d", n, maxDeliveryRowsPerDay)
	}
	write(logProject, 2, 10, "b")
	write(other, 1, 10, "c")
	if n := logCount(t, s, `SELECT COUNT(*) FROM context_delivery_log WHERE account_id=2 OR project=?`, other); n != 20 {
		t.Errorf("another account or project got %d rows, want its own 20", n)
	}
	// A new day is a new allowance: only the day's count is kept, and old days go.
	if _, err := s.db.Exec(`UPDATE context_log_quota SET day='2020-01-01'`); err != nil {
		t.Fatal(err)
	}
	write(logProject, 1, 10, "d")
	if n := logCount(t, s, `SELECT COUNT(*) FROM context_log_quota WHERE day='2020-01-01'`); n != 0 {
		t.Errorf("%d quota rows of old days stayed", n)
	}
	if n := logCount(t, s, `SELECT COUNT(*) FROM context_delivery_log WHERE account_id=1 AND project=?`, logProject); n != maxDeliveryRowsPerDay+10 {
		t.Errorf("after a day change the account stored %d rows in all", n)
	}

	// Signals: three rows per session, the same bound per day.
	for i := 0; i < maxSignalRowsPerDay/2+50; i++ {
		if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: fmt.Sprintf("sg%d", i), Harness: "claude",
			Signals: []SessionSignal{{Name: "CLAUDE_CODE_ENTRYPOINT", Value: "cli"}, {Name: "CLAUDE_CODE_CHILD_SESSION", Value: "1"}}}, 3); err != nil {
			t.Fatal(err)
		}
	}
	if n := logCount(t, s, `SELECT COUNT(*) FROM session_signals WHERE account_id=3`); n != maxSignalRowsPerDay {
		t.Errorf("a day stored %d signal rows, cap %d", n, maxSignalRowsPerDay)
	}
	if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: "sg-other", Harness: "claude",
		Signals: []SessionSignal{{Name: "CLAUDE_CODE_ENTRYPOINT", Value: "cli"}}}, 4); err != nil {
		t.Fatal(err)
	}
	if n := logCount(t, s, `SELECT COUNT(*) FROM session_signals WHERE account_id=4`); n != 1 {
		t.Errorf("another account's signal rows: %d", n)
	}
}

// The monthly sums do not grow with the object numbers a caller picks: beyond
// maxRollupObjectsPerMonth objects in a month of a project, further objects are
// counted under object 0 of their route and kind, and nothing is lost from the
// totals.
func TestRollupObjectsPerMonthAreBounded(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	old := time.Now().UTC().Add(-deliveryLogRetention - 40*24*time.Hour).Format(time.RFC3339)
	total := maxRollupObjectsPerMonth + 150
	tx, _ := s.db.Begin()
	for i := 1; i <= total; i++ {
		if _, err := tx.Exec(`INSERT INTO context_delivery_log(account_id, session_key, call, ord, project, via, kind, object_id, chars, delivered_at)
			VALUES(1,?,?,0,?,?,?,?,2,?)`, fmt.Sprintf("s%d", i), fmt.Sprintf("c%d", i), logProject, deliverylog.ViaRelevance, deliverylog.KindKnowledge, i, old); err != nil {
			t.Fatal(err)
		}
	}
	tx.Commit()
	for i := range 3 { // the backlog is folded 2,000 rows per write
		if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: fmt.Sprintf("now%d", i), Events: []DeliveryEvent{{Call: "n", Parts: logParts(1)}}}, 1); err != nil {
			t.Fatal(err)
		}
	}
	if n := logCount(t, s, `SELECT COUNT(*) FROM context_delivery_log WHERE delivered_at=?`, old); n != 0 {
		t.Fatalf("%d old rows left unfolded", n)
	}
	if n := logCount(t, s, `SELECT COUNT(*) FROM context_delivery_rollup WHERE project=?`, logProject); n != maxRollupObjectsPerMonth+1 {
		t.Errorf("rollup rows = %d, want %d objects and one for the rest", n, maxRollupObjectsPerMonth)
	}
	var deliveries, chars int
	if err := s.db.QueryRow(`SELECT SUM(deliveries), SUM(chars) FROM context_delivery_rollup`).Scan(&deliveries, &chars); err != nil || deliveries != total || chars != 2*total {
		t.Errorf("rollup totals %d deliveries, %d chars (%v), want %d and %d", deliveries, chars, err, total, 2*total)
	}
	if n := logCount(t, s, `SELECT deliveries FROM context_delivery_rollup WHERE object_id=0`); n != 150 {
		t.Errorf("the rest holds %d deliveries, want 150", n)
	}
}

// The tables carry a project column, so a rename of the project takes them
// along without being told (renameProjectTx reads the schema).
func TestDeliveryTablesFollowAProjectRename(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	tables, err := projectTextTables(s.db)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, n := range tables {
		have[n] = true
	}
	for _, n := range []string{"context_delivery_log", "context_delivery_rollup", "session_signals", "context_log_quota"} {
		if !have[n] {
			t.Errorf("%s is not renamed with its project", n)
		}
	}
}
