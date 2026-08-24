package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/eval/requestledger"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRequestToolsRunCompleteAgentWorkflow(t *testing.T) {
	c, st := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Branch: "feature", Machine: "workstation-a"}}
	_, created, err := s.handleRequestCreate(context.Background(), nil, RequestCreateInput{
		Type: "feature", Title: "agent ledger", Description: "associate work", Criteria: []string{"workflow passes"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Asserted on the concise reply rather than the full detail object: a
	// mutation that answers with the whole request charges the agent for every
	// status update out of the context it needs for the work.
	if created.State != "open" || len(created.Criteria) != 1 {
		t.Fatalf("created = %+v", created)
	}
	_, _ = st.UpsertSession(store.Session{Harness: "codex", ExternalID: "mcp-work", Scope: s.ctxAxes})
	otherBranch := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Branch: "other", Machine: "workstation-a"}}
	_, found, err := otherBranch.handleRequestSearch(context.Background(), nil, RequestSearchInput{Query: "agent ledger"})
	if err != nil || len(found.Page.Results) != 1 {
		t.Fatalf("project-wide search=%+v err=%v", found, err)
	}
	_, started, err := s.handleRequestStartWork(context.Background(), nil, RequestStartWorkInput{RequestID: created.RequestID, Role: "primary"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.handleRequestProgress(context.Background(), nil, RequestProgressInput{
		RequestID: created.RequestID, Action: "criterion_met", CriterionID: created.Criteria[0].ID,
		EvidenceKind: "test", EvidenceRef: "go test ./internal/mcpserver",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.handleRequestFinishWork(context.Background(), nil, RequestFinishWorkInput{WorkID: started.Work.ID, State: "completed", Summary: "MCP flow passed"})
	if err != nil {
		t.Fatal(err)
	}
	_, completed, err := s.handleRequestProgress(context.Background(), nil, RequestProgressInput{
		RequestID: created.RequestID, Action: "complete", EvidenceKind: "commit", EvidenceRef: "abc",
	})
	if err != nil || completed.State != "done" {
		t.Fatalf("completed = %+v, err = %v", completed, err)
	}
}

// A request is an intention about work, not a property of the workstation it
// was filed from. Inheriting branch and machine from the session would hide it
// from every scope-exact query made on a different branch or machine.
func TestRequestCreateFilesUnderProjectOnly(t *testing.T) {
	c, st := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Branch: "feature", Machine: "workstation-a"}}
	_, created, err := s.handleRequestCreate(context.Background(), nil, RequestCreateInput{
		Type: "bug", Title: "scoped too narrowly", Description: "...", Criteria: []string{"filed project-wide"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Read back rather than trusting the reply: the mutation answers concisely
	// and no longer echoes the stored scope.
	stored, err := c.GetRequest(created.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.Request.Scope; got.Branch != "" || got.Machine != "" || got.Project != "github.com/x/y" {
		t.Fatalf("scope = %+v, want project only", got)
	}
	// The REST path filters axes exactly, so a scope-exact query from another
	// machine must still see it.
	page, err := st.SearchRequests(requestdomain.SearchFilter{
		Scope: scope.Axes{Project: "github.com/x/y", Branch: "main", Machine: "laptop"}, State: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 1 {
		t.Fatalf("results = %d, want 1: request invisible from another branch and machine", len(page.Results))
	}
}

func TestRequestToolsAreDiscoverableWithSchemasAndAnnotations(t *testing.T) {
	c, _ := newTestClient(t)
	ghosttree := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}}
	protocolServer := mcp.NewServer(&mcp.Implementation{Name: "ghosttree-test", Version: "test"}, nil)
	ghosttree.Register(protocolServer)
	protocolClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := protocolServer.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := protocolClient.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"request_search", "request_get", "request_create", "request_start_work", "request_finish_work", "request_record_progress"}
	for _, tool := range listed.Tools {
		if !slices.Contains(want, tool.Name) {
			continue
		}
		if tool.InputSchema == nil || tool.OutputSchema == nil || tool.Annotations == nil {
			t.Errorf("tool %s missing schema or annotations: %+v", tool.Name, tool)
		}
		want = slices.DeleteFunc(want, func(name string) bool { return name == tool.Name })
	}
	if len(want) != 0 {
		t.Fatalf("missing tools: %v", want)
	}
}

// A mutation answers "what changed". Returning the whole request instead means
// the agent pays for every status update with the context it needs for the
// work, which makes neglecting the ledger cheaper than keeping it — measured
// while working the backlog: six calls to mark five criteria on one request
// cost more context than the code change they recorded.
func TestMutationsAnswerWithoutTheWholeRequest(t *testing.T) {
	c, st := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Branch: "feature", Machine: "workstation-a"}}
	description := strings.Repeat("why this matters, at the length a real request is written. ", 40)
	_, created, err := s.handleRequestCreate(context.Background(), nil, RequestCreateInput{
		Type: "bug", Title: "oversized replies", Description: description,
		Criteria: []string{"first", "second", "third"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.RequestID == 0 || len(created.Criteria) != 3 || created.OpenCriteria != 3 {
		t.Fatalf("created = %+v, want the ids needed to record progress against", created)
	}
	_, _ = st.UpsertSession(store.Session{Harness: "codex", ExternalID: "mcp-terse", Scope: s.ctxAxes})
	_, started, err := s.handleRequestStartWork(context.Background(), nil, RequestStartWorkInput{RequestID: created.RequestID})
	if err != nil {
		t.Fatal(err)
	}
	_, progressed, err := s.handleRequestProgress(context.Background(), nil, RequestProgressInput{
		RequestID: created.RequestID, Action: "criterion_met", CriterionID: created.Criteria[0].ID,
		EvidenceKind: "test", EvidenceRef: "TestMutationsAnswerWithoutTheWholeRequest",
	})
	if err != nil {
		t.Fatal(err)
	}
	if progressed.Criterion == nil || progressed.Criterion.State != "met" || progressed.OpenCriteria != 2 {
		t.Fatalf("progress = %+v, want the criterion state and the remaining count", progressed)
	}

	for name, out := range map[string]any{
		"create": created, "start_work": started, "progress": progressed,
	} {
		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "why this matters") {
			t.Errorf("%s response repeats the request description", name)
		}
		// The budget is per call, not per session: an agent marking ten
		// criteria absorbs it ten times. Shared with the eval suite so the
		// contract has one number, measured here against real output rather
		// than against a figure written into a fixture.
		if len(raw) > requestledger.MaxMutationResponseBytes {
			t.Errorf("%s response is %d bytes, over the %d-byte budget: %s",
				name, len(raw), requestledger.MaxMutationResponseBytes, raw)
		}
	}
}
