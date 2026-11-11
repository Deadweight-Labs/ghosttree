package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/deliverylog"
	"github.com/Deadweight-Labs/ghosttree/internal/hookbudget"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// logFixture is a real server and store with a repository whose project has
// knowledge and a described path, and a hook environment that points at it.
type logFixture struct {
	st   *store.Store
	srv  *httptest.Server
	repo string
	cfg  config.Config
}

func newLogFixture(t *testing.T) *logFixture {
	t.Helper()
	repo := newRepo(t)
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, err := st.AddPerson("test")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	withConfig(t, srv.URL)
	cfg := config.Config{ServerURL: srv.URL, Token: token, Machine: "testbox"}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	// Two pitfalls go into every briefing; a decision waits until a word of a
	// prompt asks for it, which is how the relevance route gets something to log.
	for _, e := range []struct{ kind, title string }{
		{"pitfall", "first pitfall about something"}, {"pitfall", "second pitfall about something"},
		{"decision", "quillfeather zerbricht beim Neustart"},
	} {
		if _, err := st.InsertKnowledge(store.Knowledge{Type: e.kind, Title: e.title, Body: "body of " + e.title,
			Scope: scope.Axes{Project: "github.com/test/repo"}, Person: "test", Confidence: "trusted"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"internal/store/store.go", "internal/store/knowledge.go"} {
		if _, err := st.PutGhostFile(store.GhostFile{Project: "github.com/test/repo", Path: p, Description: strings.Repeat("what this file does. ", 12)}); err != nil {
			t.Fatal(err)
		}
	}
	return &logFixture{st: st, srv: srv, repo: repo, cfg: cfg}
}

func (f *logFixture) hook(t *testing.T, event string, payload map[string]any) string {
	t.Helper()
	payload["cwd"] = f.repo
	raw, _ := json.Marshal(payload)
	var out bytes.Buffer
	if code := cmdHookWith(bytes.NewReader(raw), []string{event, "--harness", "claude"}, &out); code != 0 {
		t.Fatalf("%s exited %d", event, code)
	}
	if event == "turn-end" || event == "post-tool-use" || event == "subagent-start" {
		var o struct {
			HookSpecificOutput struct {
				AdditionalContext string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		_ = json.Unmarshal(out.Bytes(), &o)
		return o.HookSpecificOutput.AdditionalContext
	}
	_, text := decodeHook(t, &out)
	return text
}

func (f *logFixture) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.st.DB().QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// REQ-636, the acceptance of P0: after a test session every delivery stands in
// the log with route, kind and characters, and what the log holds for the
// session is exactly what the session budget counted.
func TestDeliveryLogMatchesTheBudgetAccountOfASession(t *testing.T) {
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "sdk-cli")
	t.Setenv("CLAUDE_CODE_SESSION_ATTENDED", "")
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
	f := newLogFixture(t)
	const session = "sess-log-1"

	start := f.hook(t, "session-start", map[string]any{"session_id": session})
	prompt := f.hook(t, "user-prompt-submit", map[string]any{"session_id": session, "prompt": "what about quillfeather today"})
	read := f.hook(t, "pre-tool-use", map[string]any{"session_id": session, "tool_name": "Read",
		"tool_input": map[string]string{"file_path": "internal/store/store.go"}})
	read2 := f.hook(t, "pre-tool-use", map[string]any{"session_id": session, "tool_name": "Edit",
		"tool_input": map[string]string{"file_path": "internal/store/knowledge.go"}})
	for name, text := range map[string]string{"bootstrap": start, "relevance": prompt, "path": read, "path 2": read2} {
		if strings.TrimSpace(text) == "" {
			t.Fatalf("the %s hook delivered nothing; the test would prove nothing", name)
		}
	}
	// Nothing reaches the server while the hooks run.
	if n := f.count(t, `SELECT COUNT(*) FROM context_delivery_log`); n != 0 {
		t.Fatalf("%d rows before the turn ended", n)
	}
	f.hook(t, "turn-end", map[string]any{"session_id": session, "hook_event_name": "Stop"})

	receipts, err := hookbudget.Recent()
	if err != nil || len(receipts) != 1 {
		t.Fatalf("receipts %v %v", receipts, err)
	}
	logged := f.count(t, `SELECT COALESCE(SUM(chars),0) FROM context_delivery_log WHERE session_key=? AND subagent=0`, session)
	if logged != receipts[0].EmittedChars || logged == 0 {
		t.Errorf("the log holds %d characters, the budget account %d", logged, receipts[0].EmittedChars)
	}
	// Each hook call is its own delivery; its parts add up to what it emitted.
	want := map[string]int{"bootstrap": deliverylog.Runes(start), "relevance": deliverylog.Runes(prompt)}
	for via, chars := range want {
		var sum int
		if err := f.st.DB().QueryRow(`SELECT COALESCE(SUM(chars),0) FROM context_delivery_log WHERE session_key=? AND via=?`, session, via).Scan(&sum); err != nil {
			t.Fatal(err)
		}
		if sum != chars {
			t.Errorf("%s: log %d, delivered %d", via, sum, chars)
		}
	}
	if n := f.count(t, `SELECT COUNT(*) FROM context_delivery_log WHERE via='bootstrap' AND kind='knowledge' AND object_id != 0`); n != 2 {
		t.Errorf("bootstrap entries in the log: %d, want 2", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM context_delivery_log WHERE via='relevance' AND kind='knowledge' AND object_id != 0`); n != 1 {
		t.Errorf("relevance entries in the log: %d, want 1", n)
	}
	paths := map[string]int{}
	rows, err := f.st.DB().Query(`SELECT path, chars FROM context_delivery_log WHERE kind='ghost'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var c int
		rows.Scan(&p, &c)
		paths[p] += c
	}
	for _, p := range []string{"internal/store/store.go", "internal/store/knowledge.go"} {
		if paths[p] < 200 {
			t.Errorf("path description %s counted %d characters", p, paths[p])
		}
	}
	if f.count(t, `SELECT COUNT(*) FROM context_delivery_log WHERE session_key=? AND project != 'github.com/test/repo'`, session) != 0 {
		t.Error("rows under another project")
	}
	// The log carries numbers, paths and sizes, never the prompt.
	var dump strings.Builder
	all, err := f.st.DB().Query(`SELECT via||kind||path||call||harness||profile FROM context_delivery_log`)
	if err != nil {
		t.Fatal(err)
	}
	defer all.Close()
	for all.Next() {
		var s string
		all.Scan(&s)
		dump.WriteString(s)
	}
	if strings.Contains(dump.String(), "quillfeather") || strings.Contains(dump.String(), "today") {
		t.Error("a word of the prompt is in the log")
	}

	// The signals of the session: names and values, once.
	got := map[string]string{}
	srows, err := f.st.DB().Query(`SELECT name, value FROM session_signals WHERE session_ref=?`, session)
	if err != nil {
		t.Fatal(err)
	}
	defer srows.Close()
	for srows.Next() {
		var n, v string
		srows.Scan(&n, &v)
		got[n] = v
	}
	if len(got) != 3 || got["CLAUDE_CODE_ENTRYPOINT"] != "sdk-cli" || got["CLAUDE_CODE_CHILD_SESSION"] != "1" || got["CLAUDE_CODE_SESSION_ATTENDED"] != "" {
		t.Errorf("signals %v", got)
	}
}

// A report the server could not take stays in the session's log and is sent
// with the next one; an unreachable server changes nothing about what the hooks
// say.
func TestDeliveryLogSurvivesAnUnreachableServer(t *testing.T) {
	f := newLogFixture(t)
	const session = "sess-log-2"
	first := f.hook(t, "session-start", map[string]any{"session_id": session})

	live := f.cfg
	dead := live
	dead.ServerURL = "http://127.0.0.1:1"
	if err := config.Save(dead); err != nil {
		t.Fatal(err)
	}
	f.hook(t, "turn-end", map[string]any{"session_id": session, "hook_event_name": "Stop"})
	if n := f.count(t, `SELECT COUNT(*) FROM context_delivery_log`); n != 0 {
		t.Fatalf("rows although the server was unreachable: %d", n)
	}
	dir, err := sessionLogDir(session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "deliveries")); err != nil {
		t.Fatalf("the report did not stay for the next attempt: %v", err)
	}
	if err := config.Save(live); err != nil {
		t.Fatal(err)
	}
	f.hook(t, "turn-end", map[string]any{"session_id": session, "hook_event_name": "Stop"})
	if got := f.count(t, `SELECT COALESCE(SUM(chars),0) FROM context_delivery_log WHERE session_key=?`, session); got != deliverylog.Runes(first) {
		t.Errorf("after the retry the log holds %d of %d characters", got, deliverylog.Runes(first))
	}
	// And once more: nothing is sent twice.
	f.hook(t, "turn-end", map[string]any{"session_id": session, "hook_event_name": "Stop"})
	if got := f.count(t, `SELECT COUNT(*) FROM context_delivery_log WHERE session_key=?`, session); got == 0 {
		t.Error("rows vanished")
	}
}

// The briefing a subagent starts with passes no budget; it is logged under the
// parent session as a subagent's delivery, with the size it has.
func TestSubagentBriefingIsLoggedUnderTheParentSession(t *testing.T) {
	f := newLogFixture(t)
	const session = "sess-log-3"
	briefing := f.hook(t, "subagent-start", map[string]any{"session_id": session, "agent_id": "agent-1", "agent_type": "general-purpose"})
	if strings.TrimSpace(briefing) == "" {
		t.Fatal("no briefing")
	}
	f.hook(t, "turn-end", map[string]any{"session_id": session, "hook_event_name": "Stop"})
	var chars, sub int
	if err := f.st.DB().QueryRow(`SELECT COALESCE(SUM(chars),0), COALESCE(MIN(subagent),0) FROM context_delivery_log WHERE session_key=? AND via='subagent'`, session).Scan(&chars, &sub); err != nil {
		t.Fatal(err)
	}
	if chars != deliverylog.Runes(briefing) || sub != 1 {
		t.Errorf("logged %d characters (subagent=%d), the briefing has %d", chars, sub, deliverylog.Runes(briefing))
	}
}

// Without a project the server takes nothing, so the hook writes nothing down.
func TestNothingIsLoggedOutsideARepository(t *testing.T) {
	f := newLogFixture(t)
	f.repo = t.TempDir()
	f.hook(t, "session-start", map[string]any{"session_id": "sess-log-4"})
	dir, err := sessionLogDir("sess-log-4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "deliveries")); err == nil {
		t.Error("a spool exists for a session without a project")
	}
}

// spoolBatches makes n reports (one per project) the way the spool holds them.
func spoolBatches(n int) []sentBatch {
	var out []sentBatch
	for i := range n {
		project := fmt.Sprintf("github.com/test/p%d", i)
		line := []byte(fmt.Sprintf(`{"project":%q,"event":{"call":"c%d","parts":[{"via":"bootstrap","kind":"frame","chars":1}]}}`, project, i))
		out = append(out, sentBatch{
			batch: store.ContextLogBatch{Project: project, SessionRef: "s", Harness: "claude",
				Events: []store.DeliveryEvent{{Call: fmt.Sprintf("c%d", i), Parts: logPartsOf(1)}}},
			lines: [][]byte{line},
		})
	}
	return out
}

func logPartsOf(chars int) []deliverylog.Part {
	return []deliverylog.Part{{Via: deliverylog.ViaBootstrap, Kind: deliverylog.KindFrame, Chars: chars}}
}

// A server that does not answer costs a hook one deadline, not one per report:
// the first report that gets no answer ends the sending, and it and every report
// after it go back into the spool.
func TestFlushContextLogToAHangingServerCostsOneDeadline(t *testing.T) {
	release := make(chan struct{})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	wait := 300 * time.Millisecond
	c := client.NewWithTimeout(config.Config{ServerURL: srv.URL, Token: "t"}, wait)
	batches := spoolBatches(5)
	start := time.Now()
	back := sendContextLogBatches(c.RecordContextLogContext, batches, wait)
	took := time.Since(start)
	if took > 2*wait {
		t.Errorf("flush took %v against a hanging server, want about one deadline (%v), not one per report", took, wait)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("%d reports were sent to a server that did not answer the first, want 1", n)
	}
	if len(back) != len(batches) {
		t.Fatalf("%d lines went back, want all %d (the first and those not sent)", len(back), len(batches))
	}
	for i, l := range back {
		if !bytes.Equal(l, batches[i].lines[0]) {
			t.Errorf("line %d changed on its way back", i)
		}
	}
}

// The deadline is shared: reports that are answered slowly use it up together,
// and what is left unsent goes back.
func TestFlushContextLogShareOneDeadlineAcrossReports(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(120 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	wait := 300 * time.Millisecond
	c := client.NewWithTimeout(config.Config{ServerURL: srv.URL, Token: "t"}, wait)
	batches := spoolBatches(10)
	start := time.Now()
	back := sendContextLogBatches(c.RecordContextLogContext, batches, wait)
	if took := time.Since(start); took > wait+200*time.Millisecond {
		t.Errorf("flush took %v, the deadline of all reports together is %v", took, wait)
	}
	sent := int(hits.Load())
	if sent < 1 || sent >= len(batches) {
		t.Fatalf("%d of %d reports were sent in one deadline", sent, len(batches))
	}
	if len(back) < len(batches)-sent || len(back) > len(batches)-sent+1 {
		t.Errorf("%d lines went back after %d reports were sent of %d", len(back), sent, len(batches))
	}
}

// A refusal for good drops the report and goes on; a server that cannot be
// reached, fails (5xx) or does not know the token (401) keeps it, and so does
// everything behind it.
func TestFlushContextLogKeepsWhatTheServerMayTakeLater(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		kept   int
		hits   int32
	}{
		{"ok", http.StatusNoContent, 0, 3},
		{"bad request is dropped", http.StatusBadRequest, 0, 3},
		{"forbidden is dropped", http.StatusForbidden, 0, 3},
		{"unauthorized is kept", http.StatusUnauthorized, 3, 1},
		{"server error is kept", http.StatusInternalServerError, 3, 1},
		{"bad gateway is kept", http.StatusBadGateway, 3, 1},
		{"too many requests is kept", http.StatusTooManyRequests, 3, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if c.status >= 400 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(c.status)
					_, _ = w.Write([]byte(`{"code":"x","message":"no"}`))
					return
				}
				w.WriteHeader(c.status)
			}))
			defer srv.Close()
			cl := client.NewWithTimeout(config.Config{ServerURL: srv.URL, Token: "t"}, time.Second)
			back := sendContextLogBatches(cl.RecordContextLogContext, spoolBatches(3), time.Second)
			if len(back) != c.kept || hits.Load() != c.hits {
				t.Errorf("status %d: %d lines kept (want %d), %d reports sent (want %d)", c.status, len(back), c.kept, hits.Load(), c.hits)
			}
		})
	}
}

// The whole flush, as the Stop hook runs it, against a server that hangs: it
// ends within about a deadline and the lines stay for the next report.
func TestStopHookIsNotBlockedByAHangingServer(t *testing.T) {
	f := newLogFixture(t)
	const session = "sess-hang"
	f.hook(t, "session-start", map[string]any{"session_id": session})
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer hang.Close()
	defer close(release)
	if err := config.Save(config.Config{ServerURL: hang.URL, Token: f.cfg.Token, Machine: "testbox"}); err != nil {
		t.Fatal(err)
	}
	old := contextLogReportWait
	contextLogReportWait = 300 * time.Millisecond
	defer func() { contextLogReportWait = old }()
	start := time.Now()
	raw, _ := json.Marshal(map[string]any{"session_id": session, "cwd": f.repo})
	flushContextLog(raw, "claude", true)
	if took := time.Since(start); took > time.Second {
		t.Errorf("the flush took %v against a hanging server", took)
	}
	dir, _ := sessionLogDir(session)
	if info, err := os.Stat(filepath.Join(dir, "deliveries")); err != nil || info.Size() == 0 {
		t.Errorf("the lines did not stay in the spool: %v", err)
	}
}

// A 401 keeps the report; once the token is right it is delivered.
func TestUnauthorizedReportIsKeptAndSentLater(t *testing.T) {
	f := newLogFixture(t)
	const session = "sess-401"
	first := f.hook(t, "session-start", map[string]any{"session_id": session})
	bad := f.cfg
	bad.Token = "not-a-token"
	if err := config.Save(bad); err != nil {
		t.Fatal(err)
	}
	f.hook(t, "turn-end", map[string]any{"session_id": session, "hook_event_name": "Stop"})
	if n := f.count(t, `SELECT COUNT(*) FROM context_delivery_log`); n != 0 {
		t.Fatalf("rows stored with a bad token: %d", n)
	}
	dir, _ := sessionLogDir(session)
	if info, err := os.Stat(filepath.Join(dir, "deliveries")); err != nil || info.Size() == 0 {
		t.Fatalf("a 401 dropped the report: %v", err)
	}
	if err := config.Save(f.cfg); err != nil {
		t.Fatal(err)
	}
	f.hook(t, "turn-end", map[string]any{"session_id": session, "hook_event_name": "Stop"})
	if got := f.count(t, `SELECT COALESCE(SUM(chars),0) FROM context_delivery_log WHERE session_key=?`, session); got != deliverylog.Runes(first) {
		t.Errorf("after the token was right the log holds %d of %d characters", got, deliverylog.Runes(first))
	}
}

// Values of the variables that are not short tokens are reported as "(invalid)".
func TestSessionSignalsAreShortTokensOrInvalid(t *testing.T) {
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "/home/someone/very/private/path")
	t.Setenv("CLAUDE_CODE_SESSION_ATTENDED", strings.Repeat("a", 33))
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "sdk_cli-1.2")
	f := newLogFixture(t)
	f.hook(t, "session-start", map[string]any{"session_id": "sess-sig"})
	f.hook(t, "turn-end", map[string]any{"session_id": "sess-sig", "hook_event_name": "Stop"})
	got := map[string]string{}
	rows, err := f.st.DB().Query(`SELECT name, value FROM session_signals WHERE session_ref='sess-sig'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n, v string
		rows.Scan(&n, &v)
		got[n] = v
	}
	if got["CLAUDE_CODE_ENTRYPOINT"] != "(invalid)" || got["CLAUDE_CODE_SESSION_ATTENDED"] != "(invalid)" || got["CLAUDE_CODE_CHILD_SESSION"] != "sdk_cli-1.2" {
		t.Errorf("signals %v", got)
	}
}

// The marker that says the signals are done is written after the lines are in
// the spool: a spool that cannot take them leaves it out, so the next hook tries.
func TestSignalMarkerIsWrittenAfterTheLines(t *testing.T) {
	f := newLogFixture(t)
	dir, err := sessionLogDir("sess-marker")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A spool that is already full takes no line.
	if err := os.WriteFile(filepath.Join(dir, "deliveries"), bytes.Repeat([]byte("x"), contextLogMaxSpool), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = f
	noteSessionSignals("sess-marker", "claude", "github.com/test/repo")
	if _, err := os.Stat(filepath.Join(dir, "signals")); err == nil {
		t.Fatal("the marker was written although no line could be appended")
	}
	if err := os.Remove(filepath.Join(dir, "deliveries")); err != nil {
		t.Fatal(err)
	}
	noteSessionSignals("sess-marker", "claude", "github.com/test/repo")
	if _, err := os.Stat(filepath.Join(dir, "signals")); err != nil {
		t.Fatalf("no marker after the lines were appended: %v", err)
	}
	spool, _ := os.ReadFile(filepath.Join(dir, "deliveries"))
	if n := bytes.Count(spool, []byte(`"signal"`)); n != len(contextLogSignals) {
		t.Errorf("%d signal lines in the spool, want %d", n, len(contextLogSignals))
	}
	noteSessionSignals("sess-marker", "claude", "github.com/test/repo")
	again, _ := os.ReadFile(filepath.Join(dir, "deliveries"))
	if len(again) != len(spool) {
		t.Error("the signals were written a second time")
	}
}

// The Stop hook of Codex removes the logs of old sessions like Claude's does.
func TestCodexStopHookPrunesOldSessionLogs(t *testing.T) {
	f := newLogFixture(t)
	oldDir, err := sessionLogDir("sess-codex-old")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	longAgo := time.Now().Add(-sessionLogMaxAge - time.Hour)
	if err := os.Chtimes(oldDir, longAgo, longAgo); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"session_id": "sess-codex-new", "cwd": f.repo, "hook_event_name": "Stop"})
	var out bytes.Buffer
	if code := cmdHookWith(bytes.NewReader(raw), []string{"turn-end", "--harness", "codex"}, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Stat(oldDir); err == nil {
		t.Error("the log of a session older than a week is still there after a Codex Stop")
	}
}
