package mcpserver

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newTestClient(t *testing.T) (*client.Client, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("robin")
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	return client.New(config.Config{ServerURL: srv.URL, Token: token, Machine: "workstation-a"}), st
}

func text(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatal("empty tool result")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content is %T, want *mcp.TextContent", res.Content[0])
	}
	return tc.Text
}

func TestRememberAndSearchTools(t *testing.T) {
	c, _ := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Branch: "main", Machine: "workstation-a"}}
	_, _, err := s.handleRemember(context.Background(), nil, RememberInput{
		Type: "pitfall", Title: "flaky sfu test", Body: "retry helps", ScopeHint: ""})
	if err != nil {
		t.Fatal(err)
	}
	res, _, err := s.handleSearch(context.Background(), nil, SearchInput{Query: "sfu"})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(t, res); !strings.Contains(got, "flaky sfu test") {
		t.Errorf("search result missing entry: %s", got)
	}
}

func TestScopeHintMachine(t *testing.T) {
	c, st := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Machine: "workstation-a"}}
	if _, _, err := s.handleRemember(context.Background(), nil, RememberInput{
		Type: "note", Title: "needs xcode flag", Body: "...", ScopeHint: "machine"}); err != nil {
		t.Fatal(err)
	}
	ks, _ := st.SearchKnowledge("xcode", scope.Axes{Machine: "workstation-a"}, 10)
	if len(ks) != 1 || ks[0].Scope.Project != "" {
		t.Errorf("machine hint: %+v", ks)
	}
}

func TestScopeHintGlobal(t *testing.T) {
	c, st := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Branch: "main", Machine: "workstation-a"}}
	if _, _, err := s.handleRemember(context.Background(), nil, RememberInput{
		Type: "note", Title: "prefers tabs", Body: "...", ScopeHint: "global"}); err != nil {
		t.Fatal(err)
	}
	ks, _ := st.KnowledgeForContext(scope.Axes{})
	if len(ks) != 1 || ks[0].Scope != (scope.Axes{}) {
		t.Errorf("global hint: %+v", ks)
	}
}

// Search must still surface global and project-level knowledge while the
// session sits on a branch, so knowledge search uses the scope union.
func TestSearchFindsGlobalKnowledgeFromBranchContext(t *testing.T) {
	c, _ := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Branch: "feat", Machine: "workstation-a"}}
	s.handleRemember(context.Background(), nil, RememberInput{
		Type: "note", Title: "always run gofmt", Body: "house rule", ScopeHint: "global"})
	res, _, err := s.handleSearch(context.Background(), nil, SearchInput{Query: "gofmt", Kind: "knowledge"})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(t, res); !strings.Contains(got, "always run gofmt") {
		t.Errorf("global entry not found from branch context: %s", got)
	}
}

func TestGetBootstrap(t *testing.T) {
	c, _ := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Branch: "main", Machine: "workstation-a"}}
	s.handleRemember(context.Background(), nil, RememberInput{
		Type: "decision", Title: "sqlite over postgres", Body: "single writer is enough"})
	res, _, err := s.handleGet(context.Background(), nil, GetInput{})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(t, res); !strings.Contains(got, "sqlite over postgres") {
		t.Errorf("bootstrap missing entry: %s", got)
	}
}

func TestSessionsTool(t *testing.T) {
	c, _ := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Machine: "workstation-a"}}
	id, err := c.UpsertSession(store.Session{Harness: "codex", ExternalID: "s1",
		Scope: scope.Axes{Project: "github.com/x/y", Branch: "old", Machine: "workstation-a"},
		StartedAt: "2026-08-23T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AppendChunks(id, []store.Chunk{{Seq: 0, Role: "user", Text: "the livekit sfu keeps dropping", Raw: "{}"}}); err != nil {
		t.Fatal(err)
	}
	res, _, err := s.handleSessions(context.Background(), nil, SessionsInput{Query: "livekit"})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(t, res); !strings.Contains(got, "livekit") {
		t.Errorf("session search: %s", got)
	}
	res, _, err = s.handleSessions(context.Background(), nil, SessionsInput{SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(t, res); !strings.Contains(got, "keeps dropping") {
		t.Errorf("session read: %s", got)
	}
}
