package mcpserver

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/coretools"
	"github.com/Deadweight-Labs/ghosttree/internal/profile"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// What tools/list loads up front, in bytes of the tools that carry alwaysLoad:
// the ceilings of REQ-643. Like maxToolListBytes they make growth visible;
// raising one should be a decision.
const (
	maxFullUpfrontBytes = 35_000
	maxLeanUpfrontBytes = 16_000
)

func profileSession(t *testing.T, p profile.Profile) *mcp.ClientSession {
	t.Helper()
	session, _ := profileSessionWithStore(t, p)
	return session
}

// profileSessionWithStore is a session on a server of its own: the HTTP server
// of the product on a store that lives for the test, as a local server does.
func profileSessionWithStore(t *testing.T, p profile.Profile) (*mcp.ClientSession, *store.Store) {
	t.Helper()
	c, st := newTestClient(t)
	axes := scope.Axes{Project: "github.com/x/y", Machine: "workstation-a"}
	return connect(t, &Server{client: c, ctxAxes: axes, profile: p}), st
}

// upfront sums the size of the tools a harness loads with their schema at the
// start, and of everything the list names.
func upfront(t *testing.T, res *mcp.ListToolsResult) (up, all int, names []string) {
	t.Helper()
	for _, tool := range res.Tools {
		raw, err := json.Marshal(tool)
		if err != nil {
			t.Fatal(err)
		}
		all += len(raw)
		if v, _ := tool.Meta[alwaysLoadMeta].(bool); v {
			up += len(raw)
			names = append(names, tool.Name)
		}
	}
	sort.Strings(names)
	return up, all, names
}

func TestProfileDecidesWhichToolsLoadUpFront(t *testing.T) {
	cases := []struct {
		p       profile.Profile
		ceiling int
		want    []string
	}{
		{profile.Full, maxFullUpfrontBytes, []string{"context_get", "context_remember", "context_search", "coord", "human_task", "idea", "package", "pain", "request"}},
		{profile.Lean, maxLeanUpfrontBytes, []string{"context_get", "context_remember", "context_search", "request"}},
		// The zero value is what a Server built without a profile has.
		{"", maxFullUpfrontBytes, []string{"context_get", "context_remember", "context_search", "coord", "human_task", "idea", "package", "pain", "request"}},
	}
	for _, c := range cases {
		t.Run(c.p.String(), func(t *testing.T) {
			res, err := profileSession(t, c.p).ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			up, all, names := upfront(t, res)
			if strings.Join(names, " ") != strings.Join(c.want, " ") {
				t.Errorf("loaded up front: %v, want %v", names, c.want)
			}
			if up > c.ceiling {
				t.Errorf("%d bytes loaded up front, over the ceiling of %d", up, c.ceiling)
			}
			if all > maxToolListBytes {
				t.Errorf("tools/list names %d bytes, over the ceiling of %d", all, maxToolListBytes)
			}
			t.Logf("profile %s: %d bytes up front of %d listed (%d tools)", c.p, up, all, len(res.Tools))
			// Deferred is not gone: every core tool is listed, by name.
			if len(res.Tools) != len(coretools.Table()) {
				t.Errorf("%d tools listed, want all %d", len(res.Tools), len(coretools.Table()))
			}
		})
	}
}

func TestInstructionsNameTheToolsNotLoadedUpFront(t *testing.T) {
	full, lean := "", ""
	for p, dst := range map[profile.Profile]*string{profile.Full: &full, profile.Lean: &lean} {
		*dst = profileSession(t, p).InitializeResult().Instructions
	}
	for _, name := range []string{"feature", "runbook", "comment"} {
		if !strings.Contains(full, name) {
			t.Errorf("full instructions do not name %s: %q", name, full)
		}
	}
	for _, name := range []string{"idea", "coord", "human_task", "pain", "package"} {
		if strings.Contains(full, name) {
			t.Errorf("full instructions name %s, which is loaded up front: %q", name, full)
		}
		if !strings.Contains(lean, name) {
			t.Errorf("lean instructions do not name %s: %q", name, lean)
		}
	}
	for _, name := range []string{"context_search", "context_get", "context_remember", "request"} {
		if re := regexp.MustCompile(`\b` + name + `\b`); re.MatchString(lean) {
			t.Errorf("lean instructions name %s, which is loaded up front: %q", name, lean)
		}
	}
	if n := len([]rune(lean)); n > 300 {
		t.Errorf("lean instructions have %d characters; they are in every session", n)
	}
}

// A lean session does what short helper runs do — look something up, record
// what it found, read and start a request — with the four tools it carries, and
// a tool it does not carry up front still answers. The server behind it is the
// real one, on a store that lives for the test.
func TestLeanSessionDoesTheCoreTasks(t *testing.T) {
	session, st := profileSessionWithStore(t, profile.Lean)
	// The session row the collector would have written for the agent.
	if _, err := st.UpsertSession(store.Session{Harness: "codex", ExternalID: "lean-run", Scope: scope.Axes{Project: "github.com/x/y", Machine: "workstation-a"}}); err != nil {
		t.Fatal(err)
	}

	text, failed := callTool(t, session, "context_remember", map[string]any{
		"action": "remember", "type": "pitfall", "title": "Flaky sfu retry test",
		"body": "Retrying the sfu join twice hides a real timeout.", "scope_hint": "project",
		"explanation": "A test that retries hides a timeout. Look at the join code first.",
	})
	if failed {
		t.Fatalf("context_remember: %s", text)
	}

	text, failed = callTool(t, session, "context_search", map[string]any{"query": "sfu retry"})
	if failed || !strings.Contains(text, "Flaky sfu retry test") {
		t.Fatalf("context_search does not find what was remembered: %v %s", failed, text)
	}

	text, failed = callTool(t, session, "request", map[string]any{
		"action": "create", "type": "bug", "title": "sfu join times out under load",
		"description": "The join takes longer than the client waits when twenty peers arrive at once.",
		"criteria":    []any{"twenty peers join within the client timeout"},
	})
	ref := regexp.MustCompile(`REQ-(\d+)`).FindStringSubmatch(text)
	if failed || ref == nil {
		t.Fatalf("request create: %v %s", failed, text)
	}
	text, failed = callTool(t, session, "request", map[string]any{"action": "get", "request_id": ref[1]})
	if failed || !strings.Contains(text, "sfu join times out under load") || !strings.Contains(text, "twenty peers join") {
		t.Fatalf("request get: %v %s", failed, text)
	}
	if text, failed = callTool(t, session, "request", map[string]any{"action": "start_work", "request_id": ref[1]}); failed {
		t.Fatalf("request start_work: %s", text)
	}

	// Tools the lean profile does not load up front: one of the work tier, one of
	// the rare tier. Both answer, and what they write is found by the tools
	// that are loaded.
	text, failed = callTool(t, session, "pain", map[string]any{"action": "record", "title": "Join latency has no dashboard"})
	if failed || !strings.Contains(text, "PAIN-") {
		t.Fatalf("pain record under lean: %v %s", failed, text)
	}
	if text, failed = callTool(t, session, "runbook", map[string]any{"action": "get"}); failed {
		t.Fatalf("runbook get under lean: %s", text)
	}
	if text, failed = callTool(t, session, "comment", map[string]any{"action": "label_list"}); failed {
		t.Fatalf("comment label_list under lean: %s", text)
	}
	text, failed = callTool(t, session, "context_search", map[string]any{"query": "is:pain dashboard"})
	if failed || !strings.Contains(text, "Join latency has no dashboard") {
		t.Fatalf("a pain written through a deferred tool is not found: %v %s", failed, text)
	}
}
