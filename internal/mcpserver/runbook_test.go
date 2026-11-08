package mcpserver

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func rbTestSpec() runbook.Spec {
	return runbook.Spec{
		Purpose: "ausrollen",
		Params:  []runbook.Param{{Name: "sha", Type: runbook.TypeSHA}},
		Steps:   []runbook.Step{{Title: "bauen", Command: "VERSION={{sha}} scripts/build-dist.sh"}, {Title: "starten", Command: "systemctl start x"}},
	}
}

// Der Weg eines Agenten: vorschlagen (nie freigegeben), lesen mit Parametern,
// zurückmelden mit Abweichung.
func TestRunbookToolsProposeReadAndReport(t *testing.T) {
	ctx := context.Background()
	c, st := newTestClient(t)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: pkgProject, Branch: "main", Machine: "workstation-a"}}

	res, _, err := s.handleRunbookGet(ctx, nil, RunbookGetInput{})
	if err != nil || !strings.Contains(text(t, res), "No runbooks apply here") {
		t.Fatalf("empty list: %v %s", err, text(t, res))
	}
	res, w, err := s.handleRunbookWrite(ctx, nil, RunbookWriteInput{Name: "rollout", Spec: rbTestSpec()})
	if err != nil || w.Approved || w.Version != 1 || !strings.Contains(text(t, res), "NOT approved") {
		t.Fatalf("write: %+v %v %s", w, err, text(t, res))
	}
	// Ein ungültiger Ablauf wird abgelehnt.
	bad := rbTestSpec()
	bad.Steps[0].Command = "a\nb"
	if _, _, err := s.handleRunbookWrite(ctx, nil, RunbookWriteInput{Name: "bad", Spec: bad}); err == nil {
		t.Fatal("multi-line command accepted")
	}
	res, out, err := s.handleRunbookGet(ctx, nil, RunbookGetInput{Name: "rollout", Params: map[string]string{"sha": "abc1234"}})
	if err != nil || out.Runbook == nil || out.Runbook.Approved {
		t.Fatalf("get: %+v %v", out.Runbook, err)
	}
	for _, want := range []string{"VERSION=abc1234 scripts/build-dist.sh", "NOT APPROVED", "last success: none reported yet"} {
		if !strings.Contains(text(t, res), want) {
			t.Errorf("checklist misses %q:\n%s", want, text(t, res))
		}
	}
	if _, _, err := s.handleRunbookGet(ctx, nil, RunbookGetInput{Name: "rollout", Params: map[string]string{"sha": "abc1234;id"}}); err == nil {
		t.Fatal("an unsafe parameter value was accepted")
	}
	if _, _, err := s.handleRunbookGet(ctx, nil, RunbookGetInput{Name: "gibt-es-nicht"}); err == nil {
		t.Fatal("an unknown runbook was found")
	}
	res, list, err := s.handleRunbookGet(ctx, nil, RunbookGetInput{})
	if err != nil || len(list.List) != 1 || !strings.Contains(text(t, res), "rollout [v1 NOT APPROVED") {
		t.Fatalf("list: %+v %v %s", list, err, text(t, res))
	}
	// Zurückmelden, mit einer Abweichung.
	res, rep, err := s.handleRunReport(ctx, nil, RunReportInput{Name: "rollout", Result: "ok", Params: map[string]string{"sha": "abc1234"},
		Deviations: []runbook.Deviation{{Kind: runbook.DevAdd, Step: 2, Title: "Cache leeren", Command: "make clean"}}})
	if err != nil || rep.RunID == 0 || rep.ProposalID == 0 || !strings.Contains(text(t, res), "must approve") {
		t.Fatalf("report: %+v %v %s", rep, err, text(t, res))
	}
	if _, _, err := s.handleRunReport(ctx, nil, RunReportInput{Name: "rollout", Result: "maybe"}); err == nil {
		t.Fatal("an invalid result was accepted")
	}
	// Der Lauf ändert den Ablauf nicht; der Vorschlag wartet.
	ref, _ := st.RunbookRefByScope(pkgProject, "", "rollout", 0)
	if v, _ := st.RunbookView(ref.ID, 0); v.Head != 1 || v.PendingProposals != 1 || v.LastSuccess == nil {
		t.Fatalf("view after the report: %+v", v)
	}
}

func TestRunbookToolsAreDiscoverable(t *testing.T) {
	c, _ := newTestClient(t)
	ghosttree := &Server{client: c, ctxAxes: scope.Axes{Project: pkgProject}}
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
	want := []string{"runbook_get", "runbook_write", "run_report"}
	for _, tool := range listed.Tools {
		if slices.Contains(want, tool.Name) {
			if tool.InputSchema == nil || tool.OutputSchema == nil || tool.Annotations == nil {
				t.Errorf("tool %s missing schema or annotations", tool.Name)
			}
			want = slices.DeleteFunc(want, func(n string) bool { return n == tool.Name })
		}
		// Ausführen und Freigeben gibt es über MCP nicht.
		if strings.Contains(tool.Name, "runbook_run") || strings.Contains(tool.Name, "runbook_approve") {
			t.Errorf("tool %s must not exist", tool.Name)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing tools: %v", want)
	}
}
