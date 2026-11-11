package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
