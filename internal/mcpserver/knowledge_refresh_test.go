package mcpserver

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// REQ-436 AC-2: storing, replacing or retiring a knowledge entry tells cmd/ctx
// to rewrite the mirror, and a failed or read-only call does not.
func TestKnowledgeWritesAskForAMirrorRefresh(t *testing.T) {
	c, _ := newTestClient(t)
	var refreshes atomic.Int32
	srv := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}, afterKnowledge: func() { refreshes.Add(1) }}
	session := connect(t, srv)

	remember := func(title string) string {
		out, failed := callTool(t, session, "context_remember", map[string]any{
			"type": "note", "title": title, "body": "text", "scope_hint": "project"})
		if failed || !strings.Contains(out, "stored KN-") {
			t.Fatalf("remember %q: %v %s", title, failed, out)
		}
		return out
	}
	first := remember("one")
	if refreshes.Load() != 1 {
		t.Fatalf("after remember: %d refreshes", refreshes.Load())
	}

	// A refused call stores nothing and asks for nothing.
	if _, failed := callTool(t, session, "context_remember", map[string]any{"type": "note", "title": "no placement", "body": "x"}); !failed {
		t.Fatal("a note without scope_hint was accepted")
	}
	if refreshes.Load() != 1 {
		t.Fatalf("after a refused call: %d refreshes", refreshes.Load())
	}

	// Replacing: the new entry supersedes the first one.
	id := strings.TrimSuffix(strings.TrimPrefix(first[strings.Index(first, "KN-"):], "KN-"), "")
	id = strings.FieldsFunc(id, func(r rune) bool { return r < '0' || r > '9' })[0]
	out, failed := callTool(t, session, "context_remember", map[string]any{
		"type": "note", "title": "two", "body": "text", "scope_hint": "project",
		"supersedes": []any{mustInt(t, id)}, "supersede_reason": "newer"})
	if failed || refreshes.Load() != 2 {
		t.Fatalf("supersede through remember: %v %s, %d refreshes", failed, out, refreshes.Load())
	}

	// Retiring an entry that replaced nothing.
	third := remember("three")
	third = strings.FieldsFunc(strings.TrimPrefix(third[strings.Index(third, "KN-"):], "KN-"), func(r rune) bool { return r < '0' || r > '9' })[0]
	if out, failed := callTool(t, session, "context_remember", map[string]any{"action": "retire", "id": mustInt(t, third), "reason": "gone"}); failed {
		t.Fatalf("retire: %s", out)
	}
	if refreshes.Load() != 4 {
		t.Fatalf("after remember and retire: %d refreshes", refreshes.Load())
	}

	// Without a callback (a harness that wires none) nothing breaks.
	quiet := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}})
	if _, failed := callTool(t, quiet, "context_remember", map[string]any{"type": "note", "title": "quiet", "body": "x", "scope_hint": "project"}); failed {
		t.Fatal("remember failed without a refresh callback")
	}
}

func mustInt(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	if s == "" {
		t.Fatal("no id")
	}
	return n
}
