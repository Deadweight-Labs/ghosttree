package mcpserver

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
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
		Type: "pitfall", Title: "flaky sfu test", Body: "retry helps", ScopeHint: "project"})
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

func TestGeneralSearchLabelsRequestDomain(t *testing.T) {
	c, st := newTestClient(t)
	_, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "searchable ledger", Scope: scope.Axes{Project: "github.com/x/y"}}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}}
	res, _, err := s.handleSearch(context.Background(), nil, SearchInput{Query: "searchable"})
	if err != nil {
		t.Fatal(err)
	}
	got := text(t, res)
	if !strings.Contains(got, "[request|status=open|type=feature|source=ledger]") {
		t.Fatalf("request provenance missing: %s", got)
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
	if len(ks) != 1 || !ks[0].Scope.IsGlobal() {
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

// Scope separation is the default and must stay that way: a lesson from one
// repo does not silently leak into another.
func TestSearchKeepsOtherProjectsOutByDefault(t *testing.T) {
	c, st := newTestClient(t)
	st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "raspi preflight hangs",
		Body: "serial console needed", Scope: scope.Axes{Project: "github.com/x/sampleproject"}})
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/other", Branch: "main", Machine: "workstation-a"}}
	res, _, err := s.handleSearch(context.Background(), nil, SearchInput{Query: "preflight"})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(t, res); strings.Contains(got, "raspi preflight hangs") {
		t.Errorf("another project's knowledge leaked into the default scope: %s", got)
	}
}

// Sometimes the separation is wrong: a problem here was already solved there.
// The agent must be able to ask for that explicitly.
func TestSearchAllProjectsFindsKnowledgeFromAnotherProject(t *testing.T) {
	c, st := newTestClient(t)
	st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "raspi preflight hangs",
		Body: "serial console needed", Scope: scope.Axes{Project: "github.com/x/sampleproject"}})
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/other", Branch: "main", Machine: "workstation-a"}}
	res, _, err := s.handleSearch(context.Background(), nil, SearchInput{Query: "preflight", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(t, res); !strings.Contains(got, "raspi preflight hangs") {
		t.Errorf("all_projects did not reach the other project: %s", got)
	}
}

// The session archive is the largest body of data ghosttree holds; without a
// project axis it is unreachable from anywhere but its own repo.
func TestSearchAllProjectsFindsSessionsFromAnotherProject(t *testing.T) {
	c, st := newTestClient(t)
	id, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "s1",
		Scope: scope.Axes{Project: "github.com/x/sample-project", Branch: "main", Machine: "workstation-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendChunks(id, []store.Chunk{{Seq: 0, Role: "user", Text: "the vitest suite hangs on teardown", Raw: "{}"}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/other", Branch: "main", Machine: "workstation-a"}}
	res, _, err := s.handleSearch(context.Background(), nil, SearchInput{Query: "teardown", Kind: "sessions", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(t, res); !strings.Contains(got, "teardown") {
		t.Errorf("all_projects did not reach sessions of another project: %s", got)
	}
}

// context_sessions is the tool an agent reaches for when asking "what happened
// here before". It must be able to look past the current repo too.
func TestSessionsToolReachesOtherProjectsOnRequest(t *testing.T) {
	c, st := newTestClient(t)
	id, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "s2",
		Scope: scope.Axes{Project: "github.com/x/sample-project", Branch: "main", Machine: "workstation-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendChunks(id, []store.Chunk{{Seq: 0, Role: "user", Text: "the vitest suite hangs on teardown", Raw: "{}"}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/other", Branch: "main", Machine: "workstation-a"}}

	res, _, err := s.handleSessions(context.Background(), nil, SessionsInput{Query: "teardown"})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(t, res); strings.Contains(got, "teardown") {
		t.Errorf("default scope leaked another project's sessions: %s", got)
	}

	res, _, err = s.handleSessions(context.Background(), nil, SessionsInput{Query: "teardown", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(t, res); !strings.Contains(got, "teardown") {
		t.Errorf("all_projects did not reach the other project's sessions: %s", got)
	}
}

// A named project is the precise form of the same need.
func TestSearchNamedProjectOverridesContext(t *testing.T) {
	c, st := newTestClient(t)
	st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "raspi preflight hangs",
		Body: "serial console needed", Scope: scope.Axes{Project: "github.com/x/sampleproject"}})
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/other", Branch: "main", Machine: "workstation-a"}}
	res, _, err := s.handleSearch(context.Background(), nil, SearchInput{Query: "preflight", Project: "github.com/x/sampleproject"})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(t, res); !strings.Contains(got, "raspi preflight hangs") {
		t.Errorf("named project not searched: %s", got)
	}
}

func TestGetBootstrap(t *testing.T) {
	c, _ := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Branch: "main", Machine: "workstation-a"}}
	s.handleRemember(context.Background(), nil, RememberInput{
		Type: "pitfall", Title: "sequence ids collide", Body: "upstream is not concurrency-safe", ScopeHint: "project"})
	s.handleRemember(context.Background(), nil, RememberInput{
		Type: "decision", Title: "sqlite over postgres", Body: "single writer is enough", ScopeHint: "project"})
	res, _, err := s.handleGet(context.Background(), nil, GetInput{})
	if err != nil {
		t.Fatal(err)
	}
	got := text(t, res)
	if !strings.Contains(got, "sequence ids collide") {
		t.Errorf("bootstrap missing the pitfall it must push: %s", got)
	}
	// The decision is reference material and is announced rather than sent.
	if strings.Contains(got, "single writer is enough") {
		t.Errorf("bootstrap pushed a decision body: %s", got)
	}
	if !strings.Contains(got, "1 decision") {
		t.Errorf("bootstrap did not announce the held-back decision: %s", got)
	}
}

// The path gate is the one that survived: a path is objectively determinable
// from the working directory, unlike a task label the agent has to guess.
func TestGetBootstrapAppliesThePathGate(t *testing.T) {
	c, st := newTestClient(t)
	id, _ := st.InsertKnowledge(store.Knowledge{Type: "instruction", Title: "core rule", Body: "b"})
	if err := st.SetActivation(id, activation.Rule{Paths: []string{"core/**"}}); err != nil {
		t.Fatal(err)
	}
	s := NewServer(c, scope.Axes{}, activation.Context{RepoPath: "ui"})
	res, _, err := s.handleGet(context.Background(), nil, GetInput{Paths: []string{"core/lib"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(t, res); !strings.Contains(got, "core rule") {
		t.Fatalf("path-gated instruction missing when the path matches: %s", got)
	}
	// A context outside the gated paths must not receive it.
	res, _, err = s.handleGet(context.Background(), nil, GetInput{Paths: []string{"docs/readme.md"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(t, res); strings.Contains(got, "core rule") {
		t.Errorf("path-gated instruction served outside its paths: %s", got)
	}
}

func TestSearchRendersInstructionActivation(t *testing.T) {
	c, st := newTestClient(t)
	id, _ := st.InsertKnowledge(store.Knowledge{Type: "instruction", Title: "core searchable", Body: "rule", Status: "active", Confidence: "trusted", Origin: "distilled", SessionRef: "AGENTS.md"})
	if err := st.SetActivation(id, activation.Rule{Paths: []string{"core/**"}}); err != nil {
		t.Fatal(err)
	}
	s := NewServer(c, scope.Axes{})
	res, _, err := s.handleSearch(context.Background(), nil, SearchInput{Query: "searchable", Kind: "knowledge"})
	if err != nil {
		t.Fatal(err)
	}
	got := text(t, res)
	for _, want := range []string{"status:active", "confidence:trusted", "activation:paths:core/**", "source:AGENTS.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("search result missing %q: %s", want, got)
		}
	}
}

// A decision that does not say why it was taken is a decision nobody can
// revisit later, so storing one returns a hint instead of failing.
func TestDecisionWithoutReasoningGetsHint(t *testing.T) {
	c, _ := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Machine: "workstation-a"}}
	res, _, err := s.handleRemember(context.Background(), nil, RememberInput{
		Type: "decision", Title: "sqlite over postgres", Body: "it is simpler", ScopeHint: "project"})
	if err != nil {
		t.Fatal(err)
	}
	got := text(t, res)
	if !strings.Contains(got, "stored #") {
		t.Errorf("hint must not replace the confirmation: %s", got)
	}
	for _, want := range []string{"why", "alternatives", "tradeoffs"} {
		if !strings.Contains(got, want) {
			t.Errorf("hint should name %q, got: %s", want, got)
		}
	}
}

func TestDecisionWithReasoningGetsNoHint(t *testing.T) {
	c, _ := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Machine: "workstation-a"}}
	res, _, err := s.handleRemember(context.Background(), nil, RememberInput{
		Type:  "decision",
		Title: "sqlite over postgres",
		Body: `Why: a single writer is enough for this workload.
Alternatives: postgres, rejected because it needs a second service.
Tradeoffs: no concurrent writers, and no network access to the data.`, ScopeHint: "project"})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(t, res); strings.Contains(got, "hint:") {
		t.Errorf("structured decision should get no hint: %s", got)
	}
}

func TestNonDecisionGetsNoHint(t *testing.T) {
	c, _ := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Machine: "workstation-a"}}
	res, _, err := s.handleRemember(context.Background(), nil, RememberInput{
		Type: "note", Title: "ollama lives in /usr/bin", Body: "nothing structured here", ScopeHint: "project"})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(t, res); strings.Contains(got, "hint:") {
		t.Errorf("only decisions get the hint, got: %s", got)
	}
}

func TestSessionsTool(t *testing.T) {
	c, _ := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Machine: "workstation-a"}}
	id, err := c.UpsertSession(store.Session{Harness: "codex", ExternalID: "s1",
		Scope:     scope.Axes{Project: "github.com/x/y", Branch: "old", Machine: "workstation-a"},
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

// Branch scope is the exception the default no longer applies. It has to keep
// working, because "this only holds while the migration is in flight" is a real
// thing to want to say — it just is not the normal case.
func TestScopeHintBranchStillNarrows(t *testing.T) {
	c, st := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Branch: "feat/migration", Machine: "workstation-a"}}
	if _, _, err := s.handleRemember(context.Background(), nil, RememberInput{
		Type: "pitfall", Title: "dual write window", Body: "...", ScopeHint: "branch"}); err != nil {
		t.Fatal(err)
	}
	ks, _ := st.KnowledgeForContext(scope.Axes{Project: "github.com/x/y", Branch: "feat/migration"})
	if len(ks) != 1 || ks[0].Scope.Branch != "feat/migration" {
		t.Fatalf("branch hint: %+v", ks)
	}
	if other, _ := st.KnowledgeForContext(scope.Axes{Project: "github.com/x/y", Branch: "main"}); len(other) != 0 {
		t.Errorf("branch-scoped entry leaked to another branch: %+v", other)
	}
}

// Placement is a judgement, and the tool asks for it. A default put everything
// on the branch and stranded 127 entries; the correction put everything on the
// project, which is right more often and still nobody's decision.
func TestRememberRefusesToPlaceAnEntryOnItsOwn(t *testing.T) {
	c, _ := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Branch: "feat/whatever", Machine: "workstation-a"}}
	_, _, err := s.handleRemember(context.Background(), nil, RememberInput{
		Type: "pitfall", Title: "stale binary in PATH", Body: "..."})
	if err == nil {
		t.Fatal("an entry was filed without anyone saying where it belongs")
	}
	// The refusal has to carry the test, or it just costs a round trip.
	for _, want := range []string{"required", "merged or abandoned"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q: %v", want, err)
		}
	}
	if _, _, err := s.handleRemember(context.Background(), nil, RememberInput{
		Type: "pitfall", Title: "stale binary in PATH", Body: "...", ScopeHint: "sonstwo"}); err == nil {
		t.Error("an unknown placement was accepted")
	}
}

// Choosing project must still mean every branch, not the writing one.
func TestProjectPlacementReachesEveryBranch(t *testing.T) {
	c, st := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Branch: "feat/whatever", Machine: "workstation-a"}}
	if _, _, err := s.handleRemember(context.Background(), nil, RememberInput{
		Type: "pitfall", Title: "stale binary in PATH", Body: "...", ScopeHint: "project"}); err != nil {
		t.Fatal(err)
	}
	ks, _ := st.KnowledgeForContext(scope.Axes{Project: "github.com/x/y", Branch: "main"})
	if len(ks) != 1 || ks[0].Scope.Branch != "" {
		t.Errorf("project placement should reach every branch, got %+v", ks)
	}
}
