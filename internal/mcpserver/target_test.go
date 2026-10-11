package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const (
	tgtHere    = "github.com/acme/ghosttree"
	tgtInfra   = "github.com/acme/infra"
	tgtForeign = "github.com/other/secret"
)

// targetFixture: alice owns org acme with two projects; bob owns another org
// with a project alice has no role in.
func targetFixture(t *testing.T) (*client.Client, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for i, name := range []string{"alice", "bob"} {
		if _, err := st.AddAccount(name, "", i == 0); err != nil {
			t.Fatal(err)
		}
	}
	token, _, err := st.CreateToken("alice", store.TokenSpec{Label: "t"})
	if err != nil {
		t.Fatal(err)
	}
	acme, err := st.CreateOrg("person:1", "Acme", "acme")
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateOrg("person:2", "Other", "other")
	if err != nil {
		t.Fatal(err)
	}
	_ = acme
	_ = other
	for _, p := range []struct{ who, remote string }{{"person:1", tgtHere}, {"person:1", tgtInfra}, {"person:2", tgtForeign}} {
		if _, err := st.EnsureProject(p.who, p.remote); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	return client.New(config.Config{ServerURL: srv.URL, Token: token, Machine: "mainex"}), st
}

func targetServer(c *client.Client, project string) *Server {
	return &Server{client: c, sessionRef: "claude-sess-1", ctxAxes: scope.Axes{Project: project, Machine: "mainex"}}
}

func knowledgeNamed(t *testing.T, st *store.Store, title string) store.Knowledge {
	t.Helper()
	rows, err := st.SearchKnowledge(title, scope.Axes{}, 10)
	if err != nil || len(rows) == 0 {
		t.Fatalf("no entry %q: %v", title, err)
	}
	return rows[0]
}

func TestRememberWithoutTargetsBehavesAsBefore(t *testing.T) {
	c, st := targetFixture(t)
	s := targetServer(c, tgtHere)
	if _, _, err := s.handleRemember(context.Background(), nil, RememberInput{Type: "note", Title: "plain note", Body: "b", ScopeHint: "project"}); err != nil {
		t.Fatal(err)
	}
	k := knowledgeNamed(t, st, "plain note")
	if k.Scope.Project != tgtHere || k.Scope.Machine != "" {
		t.Errorf("scope = %+v", k.Scope)
	}
	// Provenance: the session that wrote it.
	if k.SessionRef != "claude-sess-1" {
		t.Errorf("session ref = %q", k.SessionRef)
	}
}

func TestRememberForAnotherProjectByShortNameOrRemote(t *testing.T) {
	c, st := targetFixture(t)
	s := targetServer(c, tgtHere)
	for _, project := range []string{"infra", tgtInfra, "https://GitHub.com/acme/infra.git"} {
		title := "infra note via " + project
		if _, _, err := s.handleRemember(context.Background(), nil, RememberInput{Type: "pitfall", Title: title, Body: "b", ScopeHint: "project", Project: project}); err != nil {
			t.Fatalf("%s: %v", project, err)
		}
		if k := knowledgeNamed(t, st, title); k.Scope.Project != tgtInfra {
			t.Errorf("%s: filed under %q", project, k.Scope.Project)
		}
	}
}

func TestRememberWithoutAnyProjectIsRefusedAndNamesWhatCanBeWritten(t *testing.T) {
	c, st := targetFixture(t)
	s := targetServer(c, "") // a session in /home/main
	_, _, err := s.handleRemember(context.Background(), nil, RememberInput{Type: "note", Title: "homeless", Body: "b", ScopeHint: "project"})
	if err == nil {
		t.Fatal("an entry with no project was filed")
	}
	for _, want := range []string{"project", tgtHere, tgtInfra} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), tgtForeign) {
		t.Errorf("the refusal lists a project the account cannot write to: %v", err)
	}
	if rows, _ := st.SearchKnowledge("homeless", scope.Axes{}, 10); len(rows) != 0 {
		t.Errorf("something was stored anyway: %+v", rows)
	}
	// Naming one is enough.
	if _, _, err := s.handleRemember(context.Background(), nil, RememberInput{Type: "note", Title: "homeless but placed", Body: "b", ScopeHint: "project", Project: "infra"}); err != nil {
		t.Fatal(err)
	}
}

// Without write right and without existing must be the same answer, or the
// error names which projects exist.
func TestAProjectWithoutWriteRightLooksLikeOneThatDoesNotExist(t *testing.T) {
	c, _ := targetFixture(t)
	s := targetServer(c, tgtHere)
	ask := func(project string) string {
		t.Helper()
		_, _, err := s.handleRemember(context.Background(), nil, RememberInput{Type: "note", Title: "t", Body: "b", ScopeHint: "project", Project: project})
		if err == nil {
			t.Fatalf("%s: written", project)
		}
		return strings.ReplaceAll(err.Error(), project, "<name>")
	}
	if foreign, ghost := ask("secret"), ask("zzzghost"); foreign != ghost {
		t.Errorf("different answers:\n  %q\n  %q", foreign, ghost)
	}
	if foreign, ghost := ask(tgtForeign), ask("github.com/nobody/nothing"); foreign != ghost {
		t.Errorf("different answers for remotes:\n  %q\n  %q", foreign, ghost)
	}
	// Same for requests and threads.
	_, _, reqErr := s.handleRequestCreate(context.Background(), nil, RequestCreateInput{Type: "bug", Title: "x", Project: "secret"})
	_, _, ghostErr := s.handleRequestCreate(context.Background(), nil, RequestCreateInput{Type: "bug", Title: "x", Project: "zzzghost"})
	if reqErr == nil || ghostErr == nil || strings.ReplaceAll(reqErr.Error(), "secret", "zzzghost") != ghostErr.Error() {
		t.Errorf("request_create: %v / %v", reqErr, ghostErr)
	}
	_, _, thErr := s.handleThreadOpen(context.Background(), nil, ThreadOpenInput{Title: "x", Project: "secret"})
	_, _, thGhost := s.handleThreadOpen(context.Background(), nil, ThreadOpenInput{Title: "x", Project: "zzzghost"})
	if thErr == nil || thGhost == nil || strings.ReplaceAll(thErr.Error(), "secret", "zzzghost") != thGhost.Error() {
		t.Errorf("thread_open: %v / %v", thErr, thGhost)
	}
}

func TestAmbiguousShortNameIsRefused(t *testing.T) {
	c, st := targetFixture(t)
	if _, err := st.EnsureProject("person:1", "gitlab.com/acme/infra"); err != nil {
		t.Fatal(err)
	}
	s := targetServer(c, tgtHere)
	_, _, err := s.handleRemember(context.Background(), nil, RememberInput{Type: "note", Title: "t", Body: "b", ScopeHint: "project", Project: "infra"})
	if err == nil || !strings.Contains(err.Error(), "several projects") {
		t.Fatalf("err = %v", err)
	}
}

func TestRememberForAMachineNoAgentEverRanOn(t *testing.T) {
	c, st := targetFixture(t)
	s := targetServer(c, tgtHere)
	if _, _, err := s.handleRemember(context.Background(), nil, RememberInput{Type: "pitfall", Title: "media-vm hevc", Body: "use software", ScopeHint: "machine", Machine: "Media-VM"}); err != nil {
		t.Fatal(err)
	}
	k := knowledgeNamed(t, st, "media-vm hevc")
	if k.Scope.Machine != "media-vm" || k.Scope.Project != "" {
		t.Fatalf("scope = %+v", k.Scope)
	}
	md, err := c.Bootstrap(scope.Axes{Machine: "media-vm"}, activation.Context{}, 0, "")
	if err != nil || !strings.Contains(md, "media-vm hevc") {
		t.Errorf("a session on media-vm does not receive it: %v\n%s", err, md)
	}
	// And without naming one it is still this machine.
	if _, _, err := s.handleRemember(context.Background(), nil, RememberInput{Type: "note", Title: "mainex fact", Body: "b", ScopeHint: "machine"}); err != nil {
		t.Fatal(err)
	}
	if k := knowledgeNamed(t, st, "mainex fact"); k.Scope.Machine != "mainex" {
		t.Errorf("scope = %+v", k.Scope)
	}
	// machine does not mix with another placement.
	for _, in := range []RememberInput{
		{ScopeHint: "project", Machine: "media-vm"},
		{ScopeHint: "machine", Project: "infra"},
		{ScopeHint: "global", Project: "infra"},
	} {
		in.Type, in.Title, in.Body = "note", "mixed", "b"
		if _, _, err := s.handleRemember(context.Background(), nil, in); err == nil {
			t.Errorf("%+v was accepted", in)
		}
	}
}

func TestRequestCreateNeedsAProjectAndRecordsOrigin(t *testing.T) {
	c, st := targetFixture(t)
	create := func(s *Server, in RequestCreateInput) (int64, error) {
		in.Type, in.Title, in.Description, in.Criteria = "bug", "tool X breaks", "d", []string{"fixed"}
		_, out, err := s.handleRequestCreate(context.Background(), nil, in)
		return out.RequestID, err
	}
	// Own project: unchanged, but with the real session ref.
	d, err := create(targetServer(c, tgtHere), RequestCreateInput{})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := st.RequestByID(d)
	if got.Request.Scope.Project != tgtHere || got.Request.SessionRef != "claude-sess-1" {
		t.Errorf("request = %+v", got.Request)
	}
	// Elsewhere.
	d, err = create(targetServer(c, tgtHere), RequestCreateInput{Project: "infra"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := st.RequestByID(d); got.Request.Scope.Project != tgtInfra {
		t.Errorf("filed under %q", got.Request.Scope.Project)
	}
	// No project anywhere: refused, not filed globally.
	_, err = create(targetServer(c, ""), RequestCreateInput{})
	if err == nil || !strings.Contains(err.Error(), tgtInfra) {
		t.Fatalf("err = %v", err)
	}
	page, _ := st.SearchRequests(requestdomain.SearchFilter{Limit: 25})
	for _, h := range page.Results {
		if h.Request.Scope.Project == "" {
			t.Errorf("a global request was created: %+v", h.Request)
		}
	}
}

// An agent is in one project room only. A thread for another project must not
// register it there, and the next thread in its own project must still work:
// own, foreign, own.
func TestThreadOpenInAnotherProject(t *testing.T) {
	c, st := targetFixture(t)
	s := targetServer(c, tgtHere)
	open := func(in ThreadOpenInput) int64 {
		t.Helper()
		res, _, err := s.handleThreadOpen(context.Background(), nil, in)
		if err != nil {
			t.Fatalf("%+v: %v", in, err)
		}
		var id int64
		if _, err := fmt.Sscanf(text(t, res), "thread %d", &id); err != nil {
			t.Fatalf("%q: %v", text(t, res), err)
		}
		return id
	}
	first := open(ThreadOpenInput{Title: "own: first question", First: "my own opening"})
	foreign := open(ThreadOpenInput{Title: "infra: why does the vpn flap?", Project: "infra", First: "seen on two hosts", Question: "Is it the MTU?"})
	last := open(ThreadOpenInput{Title: "own: second question"})
	for id, want := range map[int64]string{first: tgtHere, foreign: tgtInfra, last: tgtHere} {
		th, err := st.ThreadByID(id)
		if err != nil || th.Project != want {
			t.Errorf("thread %d = %+v, %v; want project %s", id, th, err, want)
		}
	}
	// The opening contribution of the foreign thread travels with its question.
	if th, _ := st.ThreadByID(foreign); !strings.Contains(th.Question, "Is it the MTU?") || !strings.Contains(th.Question, "seen on two hosts") {
		t.Errorf("question = %q", th.Question)
	}
	// The agent is in its own room only.
	if agents, err := st.OwnAgents("person:1"); err != nil || len(agents) != 1 || agents[0].RoomKey != store.RoomKeyForProject(tgtHere) {
		t.Errorf("the agent was registered in the other project's room: %+v", agents)
	}
	// No role in the target: refused like an unknown project.
	if _, _, err := s.handleThreadOpen(context.Background(), nil, ThreadOpenInput{Title: "x", Project: tgtForeign}); err == nil {
		t.Error("a thread in a project without write right was opened")
	}
	// No project at all: refused, with the list.
	_, _, err := targetServer(c, "").handleThreadOpen(context.Background(), nil, ThreadOpenInput{Title: "homeless question"})
	if err == nil || !strings.Contains(err.Error(), tgtHere) {
		t.Errorf("err = %v", err)
	}
}

func TestMovingKnowledgeAndRequestsFromTheTools(t *testing.T) {
	c, st := targetFixture(t)
	s := targetServer(c, tgtHere)
	ctx := context.Background()
	if _, _, err := s.handleRemember(ctx, nil, RememberInput{Type: "pitfall", Title: "misfiled", Body: "b", ScopeHint: "project"}); err != nil {
		t.Fatal(err)
	}
	id := knowledgeNamed(t, st, "misfiled").ID
	if _, _, err := s.handleMove(ctx, nil, MoveInput{ID: id, Project: "infra"}); err != nil {
		t.Fatal(err)
	}
	if k, _ := st.KnowledgeByID(id); k.Scope.Project != tgtInfra {
		t.Errorf("scope = %+v", k.Scope)
	}
	if _, _, err := s.handleMove(ctx, nil, MoveInput{ID: id, Machine: "media-vm"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.handleMove(ctx, nil, MoveInput{ID: id, Global: true}); err != nil {
		t.Fatal(err)
	}
	if k, _ := st.KnowledgeByID(id); !k.Scope.IsGlobal() {
		t.Errorf("scope = %+v", k.Scope)
	}
	if _, _, err := s.handleMove(ctx, nil, MoveInput{ID: id}); err == nil {
		t.Error("a move without a target was accepted")
	}
	if _, _, err := s.handleMove(ctx, nil, MoveInput{ID: id, Project: "secret"}); err == nil {
		t.Error("a move into a foreign project was accepted")
	}

	_, out, err := s.handleRequestCreate(ctx, nil, RequestCreateInput{Type: "bug", Title: "misfiled request", Description: "d", Criteria: []string{"c"}})
	if err != nil {
		t.Fatal(err)
	}
	rid := out.RequestID
	if _, _, err := s.handleRequestProgress(ctx, nil, RequestProgressInput{RequestID: rid, Action: "correct", Project: "infra"}); err == nil {
		t.Error("a move without a reason was accepted")
	}
	if _, _, err := s.handleRequestProgress(ctx, nil, RequestProgressInput{RequestID: rid, Action: "correct", Project: "infra", Reason: "it is about the vpn"}); err != nil {
		t.Fatal(err)
	}
	d, _ := st.RequestByID(rid)
	if d.Request.Scope.Project != tgtInfra || d.Activity[len(d.Activity)-1].Kind != "request.moved" {
		t.Errorf("request = %+v / %+v", d.Request.Scope, d.Activity)
	}
}

// The new fields must be optional at the wire (#846): a tool whose description
// invites omitting a field while the schema demands it is unusable.
func TestTargetFieldsAreOptionalInTheSchema(t *testing.T) {
	c, _ := targetFixture(t)
	session := connect(t, targetServer(c, tgtHere))
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	optional := map[string][]string{
		"context_remember":        {"project", "machine"},
		"request_create":          {"project"},
		"thread_open":             {"project"},
		"request_record_progress": {"project"},
		"context_move":            {"project", "machine", "global"},
	}
	seen := 0
	for _, tool := range tools.Tools {
		fields, ok := optional[tool.Name]
		if !ok {
			continue
		}
		seen++
		raw, _ := json.Marshal(tool.InputSchema)
		var schema struct {
			Required   []string                  `json:"required"`
			Properties map[string]map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		for _, f := range fields {
			if _, ok := schema.Properties[f]; !ok {
				t.Errorf("%s has no %s", tool.Name, f)
			}
			if slices.Contains(schema.Required, f) {
				t.Errorf("%s: %s is required, which breaks every call that does not need it", tool.Name, f)
			}
		}
		if tool.Name == "context_remember" && !strings.Contains(tool.Description, "different project or machine") {
			t.Errorf("the description does not tell agents when to use the targets: %s", tool.Description)
		}
	}
	if seen != len(optional) {
		t.Errorf("found %d of %d tools", seen, len(optional))
	}
	// And calls that omit them pass the schema.
	if got, failed := callTool(t, session, "context_remember", map[string]any{"type": "note", "title": "wire", "body": "b", "scope_hint": "project"}); failed {
		t.Errorf("a call without targets failed: %s", got)
	}
	if got, failed := callTool(t, session, "context_remember", map[string]any{"type": "note", "title": "wire machine", "body": "b", "scope_hint": "machine", "machine": "media-vm"}); failed {
		t.Errorf("a call with a machine failed: %s", got)
	}
}
