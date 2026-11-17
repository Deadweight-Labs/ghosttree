package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const wfText = "---\ndescription: Merges melden\non: pr.merged\ninputs: [pr.title]\nuses: ghosttree/echo\n---\n"

// AC-695.9 (MCP side): an agent creates and changes a workflow through the
// tools; the file is checked first, a file with errors is not saved, and the
// versions are the server's, marked as written over MCP.
func TestWorkflowToolsCreateCheckAndChangeAWorkflow(t *testing.T) {
	ctx := context.Background()
	c, st := newTestClient(t)
	if _, err := st.CreateOrg("person:1", "X", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimProject("person:1", pkgProject, "x"); err != nil {
		t.Fatal(err)
	}
	s := &Server{client: c, ctxAxes: scope.Axes{Project: pkgProject, Branch: "main", Machine: "workstation-a"}}

	res, _, err := s.handleWorkflowGet(ctx, nil, WorkflowGetInput{})
	if err != nil || !strings.Contains(text(t, res), "No workflows") {
		t.Fatalf("empty list: %v", err)
	}
	res, rep, err := s.handleWorkflowCheck(ctx, nil, WorkflowCheckInput{Text: strings.Replace(wfText, "uses:", "retries: 2\nuses:", 1)})
	if err != nil || rep.OK() || !strings.Contains(text(t, res), "unknown key retries") || !rep.Aliases {
		t.Fatalf("check: %+v %v", rep, err)
	}
	res, out, err := s.handleWorkflowWrite(ctx, nil, WorkflowWriteInput{Name: "melden", Text: "---\non: pr.merged\nprompt: \"sag was\"\n---\n"})
	if err != nil || out.Version != 0 || !strings.Contains(text(t, res), "not saved") || !strings.Contains(text(t, res), "stage E5") {
		t.Fatalf("stage 3 saved: %+v %v", out, err)
	}
	if _, err := st.WorkflowDefByName(pkgProject, "melden"); err == nil {
		t.Fatal("a file with errors reached the server")
	}
	res, out, err = s.handleWorkflowWrite(ctx, nil, WorkflowWriteInput{Name: "melden", Text: wfText})
	if err != nil || out.Version != 1 || !strings.Contains(text(t, res), "created, version 1") {
		t.Fatalf("create: %+v %v", out, err)
	}
	changed := strings.Replace(wfText, "[pr.title]", "[pr.title, pr.number]", 1)
	if _, out, err = s.handleWorkflowWrite(ctx, nil, WorkflowWriteInput{Name: "melden", Text: changed, Base: 1, Note: "Nummer dazu"}); err != nil || out.Version != 2 {
		t.Fatalf("change: %+v %v", out, err)
	}
	if _, _, err = s.handleWorkflowWrite(ctx, nil, WorkflowWriteInput{Name: "melden", Text: wfText, Base: 1}); err == nil {
		t.Fatal("a save on an old version went through")
	}
	res, got, err := s.handleWorkflowGet(ctx, nil, WorkflowGetInput{Name: "melden", Versions: true})
	if err != nil || got.Workflow == nil || got.Workflow.Version != 2 || got.Workflow.Text != changed || len(got.Versions) != 2 || !got.CanEdit {
		t.Fatalf("get: %+v %v", got, err)
	}
	if got.Versions[0].Via != store.WorkflowViaMCP || got.Versions[0].Note != "Nummer dazu" || !strings.Contains(text(t, res), "/workflows/melden") {
		t.Fatalf("versions: %+v\n%s", got.Versions, text(t, res))
	}
}
