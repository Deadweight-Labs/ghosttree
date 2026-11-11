package mcpserver

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/coretools"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxToolListBytes is a ceiling on what tools/list sends. It is not a target:
// it makes growth visible. Before the core tools (REQ-436) the list was
// 100,709 bytes for 76 tools, with them 41,588; raising the ceiling should be
// a decision.
const maxToolListBytes = 42_000

// Claude Code cuts a description of a tool it loads up front at 4,096
// characters.
const maxUpfrontDescription = 4096

func coreSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	c, _ := newTestClient(t)
	return connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}})
}

func TestToolListShowsOnlyTheCoreToolsLoadedUpFront(t *testing.T) {
	session := coreSession(t)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) > coretools.MaxCore || len(res.Tools) != len(coretools.Table()) {
		t.Fatalf("listed %d tools, want the %d core tools (at most %d)", len(res.Tools), len(coretools.Table()), coretools.MaxCore)
	}
	whole, _ := json.Marshal(res)
	if len(whole) > maxToolListBytes {
		t.Errorf("tools/list is %d bytes, over the ceiling of %d", len(whole), maxToolListBytes)
	}
	if strings.Contains(string(whole), `"null"`) {
		t.Errorf("a field of a core tool is listed as nullable; every field may be left out")
	}
	for _, tool := range res.Tools {
		if tool.Name != "pain" {
			continue
		}
		raw, _ := json.Marshal(tool.InputSchema)
		if n := strings.Count(string(raw), "a request this problem motivates"); n != 1 {
			t.Errorf("pain describes the items of motivates and link_add %d times, want once", n)
		}
		if !strings.Contains(string(raw), "items as in motivates") {
			t.Errorf("link_add does not say where its items are described")
		}
	}
	hidden := map[string]bool{}
	for _, name := range coretools.Tools() {
		if !coretools.IsCore(name) {
			hidden[name] = true
		}
	}
	word := regexp.MustCompile(`[a-z]+(?:_[a-z]+)+`)
	for _, tool := range res.Tools {
		if !coretools.IsCore(tool.Name) {
			t.Errorf("%s is listed but is no core tool", tool.Name)
		}
		if n := len([]rune(tool.Description)); n > maxUpfrontDescription {
			t.Errorf("%s: description has %d characters, Claude Code cuts at %d", tool.Name, n, maxUpfrontDescription)
		}
		// The full profile loads the core and work tiers up front and leaves the
		// rare tier to tool search (profile_test.go checks the other profile).
		want := coretools.TierOf(tool.Name) != coretools.TierRare
		if v, _ := tool.Meta[alwaysLoadMeta].(bool); v != want {
			t.Errorf("%s: %s is %v, want %v for its tier", tool.Name, alwaysLoadMeta, v, want)
		}
		if tool.OutputSchema != nil {
			t.Errorf("%s: a core tool has no single output schema", tool.Name)
		}
		actions := map[string]bool{}
		for _, a := range coretools.ActionNames(tool.Name) {
			actions[a] = true
		}
		raw, _ := json.Marshal(tool)
		for _, w := range word.FindAllString(string(raw), -1) {
			if hidden[w] && !actions[w] && !isSchemaKey(raw, w) {
				t.Errorf("%s names %s, which an agent can no longer call", tool.Name, w)
			}
		}
	}
}

// isSchemaKey says whether w occurs only as a property name ("w":), which is
// where shared words like feature_pr_paths never stand.
func isSchemaKey(raw []byte, w string) bool {
	s := string(raw)
	return strings.Count(s, w) == strings.Count(s, `"`+w+`":`)
}

// Every registered tool is reachable through a core action, and every action
// names a registered tool: nothing falls out of reach, nothing routes nowhere.
func TestEveryRegisteredToolHasACoreAction(t *testing.T) {
	session := coreSession(t)
	res, err := listRegistered(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	var registered []string
	for _, tool := range res.Tools {
		registered = append(registered, tool.Name)
	}
	sort.Strings(registered)
	routed := coretools.Tools()
	if strings.Join(registered, " ") != strings.Join(routed, " ") {
		t.Fatalf("registered and routed tools differ:\nregistered %v\nrouted     %v", registered, routed)
	}
}

func TestCoreActionsReachTheRegisteredTools(t *testing.T) {
	session := coreSession(t)

	text, failed := callTool(t, session, "pain", map[string]any{"action": "record", "title": "Builds are slow on the CI runner"})
	if failed || !strings.Contains(text, "PAIN-") {
		t.Fatalf("pain record: %v %s", failed, text)
	}
	text, failed = callTool(t, session, "pain", map[string]any{"action": "search"})
	if failed || !strings.Contains(text, "Builds are slow") {
		t.Fatalf("pain search: %v %s", failed, text)
	}
	// An inner action: human_task list goes to human_task_list, comment
	// label_list to label with action list.
	if text, failed = callTool(t, session, "human_task", map[string]any{"action": "list"}); failed {
		t.Fatalf("human_task list: %s", text)
	}
	if text, failed = callTool(t, session, "comment", map[string]any{"action": "label_list"}); failed {
		t.Fatalf("comment label_list: %s", text)
	}
	// The default action: context_search without action searches, and the
	// registered tool's own checks still apply.
	text, failed = callTool(t, session, "context_search", map[string]any{})
	if !failed || !strings.Contains(text, "give a query") {
		t.Fatalf("context_search without query: %v %s", failed, text)
	}
	// The registered name still answers.
	if text, failed = callTool(t, session, "pain_search", map[string]any{}); failed || !strings.Contains(text, "Builds are slow") {
		t.Fatalf("pain_search by its old name: %v %s", failed, text)
	}
}

func TestCoreCallsExplainAWrongActionOrField(t *testing.T) {
	session := coreSession(t)
	text, failed := callTool(t, session, "request", map[string]any{})
	if !failed || !strings.Contains(text, "needs an action") || !strings.Contains(text, "criterion_met") {
		t.Fatalf("request without action: %v %s", failed, text)
	}
	text, failed = callTool(t, session, "pain", map[string]any{"action": "close"})
	if !failed || !strings.Contains(text, `no action "close"`) || !strings.Contains(text, "record") {
		t.Fatalf("unknown action: %v %s", failed, text)
	}
	text, failed = callTool(t, session, "pain", map[string]any{"action": "get", "id": 1, "query": "x"})
	if !failed || !strings.Contains(text, "does not take query") || !strings.Contains(text, "it takes id") {
		t.Fatalf("foreign field: %v %s", failed, text)
	}
	// Empty values of fields the action does not take are dropped, not refused:
	// a model often fills the whole shared schema.
	text, failed = callTool(t, session, "pain", map[string]any{"action": "search", "id": 0, "title": "", "motivates": []any{}})
	if failed {
		t.Fatalf("empty foreign fields refused: %s", text)
	}
	// A required field of the routed tool is still required.
	text, failed = callTool(t, session, "pain", map[string]any{"action": "record"})
	if !failed || !strings.Contains(text, "title") {
		t.Fatalf("missing title: %v %s", failed, text)
	}
}

func TestCoreDescriptionsCarryTheWritingRules(t *testing.T) {
	session := coreSession(t)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var remember *mcp.Tool
	for _, tool := range res.Tools {
		if tool.Name == "context_remember" {
			remember = tool
		}
	}
	if remember == nil {
		t.Fatal("context_remember not listed")
	}
	for _, want := range []string{"Search first", "supersede", "scope_hint project plus machine", "describe_file"} {
		if !strings.Contains(remember.Description, want) {
			t.Errorf("context_remember description lacks %q", want)
		}
	}
	raw, _ := json.Marshal(remember.InputSchema)
	if !strings.Contains(string(raw), "Running a repository on a host is BOTH") {
		t.Error("scope_hint lost its full description")
	}
}

func TestRewriteToolNames(t *testing.T) {
	for in, want := range map[string]string{
		"call request_get for one entry":   "call request action=get for one entry",
		"then context_retire it":           "then context_remember action=retire it",
		"search with context_search first": "search with context_search first",
		"a label or topic":                 "a label or topic",
		"feature_pr_paths":                 "feature action=pr_paths",
	} {
		if got := RewriteToolNames(in); got != want {
			t.Errorf("RewriteToolNames(%q) = %q, want %q", in, got, want)
		}
	}
}

// Codex and OpenCode calls are counted by ctx mcp itself, as core tool and
// action, also when the older name was called.
func TestMCPCountsCallsForHarnessesWithoutAToolHook(t *testing.T) {
	c, _ := newTestClient(t)
	got := make(chan store.ToolUseBatch, 4)
	srv := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}, sessionRef: "codex-sess-1",
		usageHarness: "codex", reportUses: func(b store.ToolUseBatch) { got <- b }}
	session := connect(t, srv)
	callTool(t, session, "pain", map[string]any{"action": "search"})
	callTool(t, session, "request_search", map[string]any{"query": "x"})
	// The reports leave in the background, so their order is not fixed.
	seen := map[string]bool{}
	for range 2 {
		b := <-got
		u := b.Uses[0]
		if b.Harness != "codex" || b.SessionRef != "codex-sess-1" || u.Calls != 1 {
			t.Fatalf("report %+v", b)
		}
		seen[u.Tool+" "+u.Action] = true
	}
	for _, want := range []string{"pain search", "request search"} {
		if !seen[want] {
			t.Fatalf("no report for %s, got %v", want, seen)
		}
	}
	// Without a harness to count for (Claude Code: its hook counts) nothing is sent.
	srv2 := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}, sessionRef: "claude-sess",
		reportUses: func(b store.ToolUseBatch) { got <- b }}
	callTool(t, connect(t, srv2), "pain", map[string]any{"action": "search"})
	select {
	case b := <-got:
		t.Fatalf("counted without a harness: %+v", b)
	default:
	}
}
