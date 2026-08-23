package mcpserver

import (
	"context"
	"slices"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRequestToolsRunCompleteAgentWorkflow(t *testing.T) {
	c, st := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}}
	_, created, err := s.handleRequestCreate(context.Background(), nil, RequestCreateInput{
		Type: "feature", Title: "agent ledger", Description: "associate work", Criteria: []string{"workflow passes"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Detail.Request.State != "open" || len(created.Detail.Criteria) != 1 {
		t.Fatalf("created = %+v", created)
	}
	sessionID, _ := st.UpsertSession(store.Session{Harness: "codex", ExternalID: "mcp-work"})
	_, started, err := s.handleRequestStartWork(context.Background(), nil, RequestStartWorkInput{RequestID: created.Detail.Request.ID, SessionID: sessionID, Role: "primary"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.handleRequestProgress(context.Background(), nil, RequestProgressInput{
		RequestID: created.Detail.Request.ID, Action: "criterion_met", CriterionID: created.Detail.Criteria[0].ID,
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
		RequestID: created.Detail.Request.ID, Action: "complete", EvidenceKind: "commit", EvidenceRef: "abc",
	})
	if err != nil || completed.Detail.Request.State != "done" {
		t.Fatalf("completed = %+v, err = %v", completed, err)
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
