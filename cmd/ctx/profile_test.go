package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/profile"
	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProfileLabelSaysWhereTheProfileComesFrom(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", "full (default; GHOSTTREE_PROFILE=lean for short helper runs)"},
		{"lean", "lean (GHOSTTREE_PROFILE)"},
		{"FULL", "full (GHOSTTREE_PROFILE)"},
		{"tiny", `full (GHOSTTREE_PROFILE="tiny" is no profile: lean or full; full applies)`},
	} {
		if got := profileLabel(c.in); got != c.want {
			t.Errorf("profileLabel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStatusNamesTheProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	for value, want := range map[string]string{"": "profile     full (default", "lean": "profile     lean (GHOSTTREE_PROFILE)"} {
		t.Setenv(profile.EnvVar, value)
		var out bytes.Buffer
		cmdStatus(nil, &out)
		if !strings.Contains(out.String(), want) {
			t.Errorf("with %s=%q, ctx status prints:\n%s\nwant a line %q", profile.EnvVar, value, out.String(), want)
		}
	}
}

// leanHookServer is a server on a throwaway store with the knowledge of a
// working project, and the configuration that points ctx at it.
func leanHookServer(t *testing.T) *store.Store {
	t.Helper()
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
	if err := config.Save(config.Config{ServerURL: srv.URL, Token: token, Machine: "testbox"}); err != nil {
		t.Fatal(err)
	}
	return st
}

// The session-start hook asks for the briefing of the profile in the
// environment: a lean session gets the short one, with the binding instructions
// in full, and a session without the variable gets the one it always got.
func TestSessionStartHookFollowsTheProfile(t *testing.T) {
	repo := newRepo(t)
	st := leanHookServer(t)
	proj := scope.Axes{Project: "github.com/test/repo"}
	if _, err := st.InsertKnowledge(store.Knowledge{Type: "instruction", Title: "keep it small", Body: "Do not add a dependency without asking.", Scope: proj}); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		if _, err := st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: fmt.Sprintf("pitfall %d", i),
			Body: strings.Repeat("The build cache hides a stale generated file. ", 6), Scope: proj, Confidence: "trusted"}); err != nil {
			t.Fatal(err)
		}
	}
	briefing := func(profileValue string) string {
		t.Setenv(profile.EnvVar, profileValue)
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		payload, _ := json.Marshal(map[string]any{"session_id": "profile-session-" + profileValue, "cwd": repo})
		var out bytes.Buffer
		if code := cmdHookWith(bytes.NewReader(payload), []string{"session-start"}, &out); code != 0 {
			t.Fatalf("hook exited %d", code)
		}
		_, text := decodeHook(t, &out)
		return text
	}
	full, lean := briefing(""), briefing("lean")
	if n := utf8.RuneCountInString(lean); n > profile.LeanStartChars {
		t.Errorf("the lean briefing has %d characters, at most %d:\n%s", n, profile.LeanStartChars, lean)
	}
	if !strings.Contains(lean, "Do not add a dependency without asking.") {
		t.Errorf("the lean briefing lacks the binding instruction:\n%s", lean)
	}
	if utf8.RuneCountInString(full) <= 2*utf8.RuneCountInString(lean) || strings.Count(full, "pitfall ") < 10 {
		t.Errorf("without the variable the briefing is not the full one (%d characters, lean %d)", utf8.RuneCountInString(full), utf8.RuneCountInString(lean))
	}
	t.Logf("session start: full %d characters, lean %d characters", utf8.RuneCountInString(full), utf8.RuneCountInString(lean))
}

// The hooks of a lean session may add 6,000 characters in all, and the notice
// that closes the last answer says so.
func TestHooksOfALeanSessionStopAtTheSmallerBudget(t *testing.T) {
	repo := newRepo(t)
	st := leanHookServer(t)
	t.Setenv(profile.EnvVar, "lean")
	description := strings.Repeat("a", 390)
	total, notices := 0, 0
	for i := range 40 {
		path := fmt.Sprintf("path%d.go", i)
		if err := os.WriteFile(filepath.Join(repo, path), []byte("package test\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := st.PutGhostFile(store.GhostFile{Project: "github.com/test/repo", Path: path, Description: description}); err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(map[string]any{"session_id": "lean-budget-session", "cwd": repo,
			"tool_name": "Read", "tool_input": map[string]string{"file_path": path}})
		var out bytes.Buffer
		if code := cmdHookWith(bytes.NewReader(payload), []string{"pre-tool-use"}, &out); code != 0 {
			t.Fatal(code)
		}
		_, text := decodeHook(t, &out)
		total += utf8.RuneCountInString(text)
		notices += strings.Count(text, "Session hook context budget reached (6000 characters)")
	}
	// The last answer is cut after its last whole line: at most the budget, and
	// less than one line short of it.
	if total > profile.LeanHookBudget || total < profile.LeanHookBudget-500 || notices != 1 {
		t.Fatalf("a lean session got %d characters from its hooks and %d notices, want at most %d (and no more than 500 short) and 1", total, notices, profile.LeanHookBudget)
	}
}

// TestMCPChild is the child process: it runs as `ctx mcp` over the pipes the
// parent gave it. Without the variable it does nothing.
func TestMCPChild(t *testing.T) {
	if os.Getenv("GHOSTTREE_TEST_MCP_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	os.Exit(cmdMCP(nil, os.Stderr))
}

// The real `ctx mcp` process, started with GHOSTTREE_PROFILE=lean against a
// server on a throwaway store: it lists the four tools of the core tier with
// alwaysLoad and the others without, names the others in its instructions, and
// an agent does the core tasks with it, including through a tool it does not
// load up front.
func TestMCPProcessUnderLeanDoesTheCoreTasks(t *testing.T) {
	repo := newRepo(t)
	leanHookServer(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := func(profileValue string) *mcp.ClientSession {
		cmd := exec.Command(exe, "-test.run=^TestMCPChild$")
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GHOSTTREE_TEST_MCP_CHILD=1", profile.EnvVar+"="+profileValue,
			"CLAUDE_CODE_SESSION_ID=", "CODEX_SESSION_ID=", "CODEX_THREAD_ID=", "OPENCODE_SESSION_ID=")
		client := mcp.NewClient(&mcp.Implementation{Name: "probe", Version: "test"}, nil)
		session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { session.Close() })
		return session
	}
	preloaded := func(session *mcp.ClientSession) []string {
		res, err := session.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Tools) != 12 {
			t.Errorf("%d tools listed, want all 12", len(res.Tools))
		}
		var up []string
		for _, tool := range res.Tools {
			if v, _ := tool.Meta["anthropic/alwaysLoad"].(bool); v {
				up = append(up, tool.Name)
			}
		}
		return up
	}
	call := func(session *mcp.ClientSession, name string, args map[string]any) string {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var sb strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				sb.WriteString(tc.Text)
			}
		}
		if res.IsError {
			t.Fatalf("%s failed: %s", name, sb.String())
		}
		return sb.String()
	}

	lean := child("lean")
	if got, want := strings.Join(preloaded(lean), " "), "context_search context_get context_remember request"; got != want {
		t.Errorf("lean preloads %q, want %q", got, want)
	}
	if ins := lean.InitializeResult().Instructions; !strings.Contains(ins, "idea") || !strings.Contains(ins, "runbook") {
		t.Errorf("the instructions of a lean session do not name the tools left out: %q", ins)
	}
	call(lean, "context_remember", map[string]any{"type": "note", "title": "Join retry is capped at two", "body": "The sfu client retries a join twice.", "scope_hint": "global"})
	if got := call(lean, "context_search", map[string]any{"query": "join retry"}); !strings.Contains(got, "Join retry is capped at two") {
		t.Errorf("context_search does not find what was remembered:\n%s", got)
	}
	created := call(lean, "request", map[string]any{"action": "create", "type": "change", "title": "Cap the join retry", "description": "Make the cap a setting.", "criteria": []any{"the cap is read from the settings"}})
	if !strings.Contains(created, "REQ-") {
		t.Fatalf("request create:\n%s", created)
	}
	if got := call(lean, "request", map[string]any{"action": "search", "query": "join retry"}); !strings.Contains(got, "Cap the join retry") {
		t.Errorf("request search does not find the request:\n%s", got)
	}
	if got := call(lean, "idea", map[string]any{"action": "record", "title": "Show the retry count"}); !strings.Contains(got, "IDEA-") {
		t.Errorf("idea record through a tool a lean session does not load up front:\n%s", got)
	}

	// Without the variable the same process loads the work tier as well.
	if got, want := strings.Join(preloaded(child("")), " "), "context_search context_get context_remember request pain idea coord human_task package"; got != want {
		t.Errorf("a session without a profile preloads %q, want %q", got, want)
	}
}

// REQ-643 review: under lean, the briefing and several runbooks share 6,000
// characters. A runbook goes out whole or not at all, and one that did not go
// out is not remembered as delivered: the server's memory and the text the
// session got say the same.
func TestLeanSessionGetsRunbooksWholeOrNotAtAll(t *testing.T) {
	repo := newRepo(t)
	st := leanHookServer(t)
	t.Setenv(profile.EnvVar, "lean")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	proj := scope.Axes{Project: "github.com/test/repo"}
	for i := range 8 {
		if _, err := st.InsertKnowledge(store.Knowledge{Type: "instruction", Title: fmt.Sprintf("rule %d", i),
			Body: strings.Repeat("Keep the change small and say what you did not check. ", 3), Scope: proj}); err != nil {
			t.Fatal(err)
		}
	}
	// A runbook is delivered by itself only once an owner of the project approved it.
	if _, err := st.CreateOrg("person:1", "Acme", "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := claimInOrg(st, "person:1", proj.Project); err != nil {
		t.Fatal(err)
	}
	const session = "lean-runbooks-session"
	var names []string
	for _, word := range []string{"alpha", "beta", "gamma", "delta"} {
		var steps []runbook.Step
		for i := range 40 {
			steps = append(steps, runbook.Step{Title: fmt.Sprintf("%s step %d: %s", word, i, strings.Repeat("work ", 10))})
		}
		spec, err := runbook.Normalize(runbook.Spec{Purpose: "do " + word, Steps: steps, Triggers: runbook.Triggers{Keywords: []string{word}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.WriteRunbook(store.RunbookWrite{Project: proj.Project, Name: "rb-" + word, Spec: spec, Person: "test", AuthorID: 1, Approve: true}); err != nil {
			t.Fatal(err)
		}
		names = append(names, "rb-"+word)
	}
	run := func(event string, extra map[string]any) string {
		payload := map[string]any{"session_id": session, "cwd": repo}
		for k, v := range extra {
			payload[k] = v
		}
		raw, _ := json.Marshal(payload)
		var out bytes.Buffer
		if code := cmdHookWith(bytes.NewReader(raw), []string{event}, &out); code != 0 {
			t.Fatalf("hook %s exited %d", event, code)
		}
		_, text := decodeHook(t, &out)
		return text
	}
	var got strings.Builder
	got.WriteString(run("session-start", nil))
	for _, word := range []string{"alpha", "beta", "gamma", "delta"} {
		got.WriteString("\n" + run("user-prompt-submit", map[string]any{"prompt": "please " + word + " now"}))
	}
	all := got.String()
	if total := utf8.RuneCountInString(all); total > profile.LeanHookBudget {
		t.Fatalf("a lean session got %d characters from its hooks, at most %d", total, profile.LeanHookBudget)
	}
	remembered := func(name string) bool {
		ref, err := st.RunbookRefByScope(proj.Project, "", name, 1)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := st.DB().QueryRow(`SELECT COUNT(*) FROM context_deliveries WHERE session_key=? AND term=?`,
			session, fmt.Sprintf("runbook:%d", ref.ID)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n > 0
	}
	whole := 0
	for _, name := range names {
		header, report := "[runbook "+name+", triggered by", "ctx runbook report "+name+" ok|failed"
		in, closed := strings.Contains(all, header), strings.Contains(all, report)
		if in != closed {
			t.Errorf("%s: header in the text %v, report line %v: the runbook is cut\n%s", name, in, closed, all)
		}
		if closed {
			whole++
		}
		if remembered(name) != closed {
			t.Errorf("%s: remembered as delivered %v, but whole in the text %v", name, remembered(name), closed)
		}
	}
	if whole < 2 || whole == len(names) {
		t.Errorf("%d of %d runbooks delivered; the fixture should deliver some and leave some for lack of room", whole, len(names))
	}
	t.Logf("%d of %d runbooks delivered in %d characters", whole, len(names), utf8.RuneCountInString(all))
}
