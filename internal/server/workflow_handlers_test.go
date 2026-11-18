package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The token of a running workflow run is a principal of its own (REQ-695,
// KN-4796): it reads as the account the run acts for (the author of the
// version it runs), carries the run's number, reaches nothing else through the
// API and ends with the run.
func TestTheTokenOfARunIsAPrincipalWithTheRunThatOnlyReads(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	const project = "github.com/alpha/app"
	if _, err := st.CreateOrg("person:1", "Alpha", "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimProject("person:1", project, "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveWorkflowDef("person:1", store.WorkflowDefSave{Project: project, Name: "melden", Text: "---\non: pr.merged\nuses: ghosttree/echo\n---\n",
		Event: store.EventPRMerged, Uses: "ghosttree/echo"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RecordWorkflowEvent(store.WorkflowEventInput{Kind: store.EventPRMerged, Project: project, SubjectKind: "pr", SubjectID: "1",
		Data: map[string]string{"base": "main", "default_branch": "1"}, Origin: store.EventOrigin{Kind: store.OriginSystem}, DedupeKey: "m1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DispatchWorkflowEvents(10); err != nil {
		t.Fatal(err)
	}
	run, _, ok, err := st.ClaimWorkflowRun()
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := st.MarkWorkflowRunVersion(run.ID, "1"); err != nil {
		t.Fatal(err)
	}
	token, err := st.IssueAIRunToken(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	resp := req(t, "GET", srv.URL+"/api/whoami", token, nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var who store.Principal
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &who) != nil || who.ID != "person:1" || who.RunID != run.ID || who.TokenKind != store.WorkflowRunKind {
		t.Fatalf("whoami with a run token: %d %s", resp.StatusCode, raw)
	}
	resp = req(t, "GET", srv.URL+"/api/workflows?project="+project, token, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a run token reads beyond whoami: %d", resp.StatusCode)
	}
	resp = req(t, "POST", srv.URL+"/api/knowledge", token, map[string]any{"type": "note", "title": "x", "body": "y", "scope": map[string]string{"project": project}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a run token writes: %d", resp.StatusCode)
	}
	resp = req(t, "PUT", srv.URL+"/api/workflows/melden", token, map[string]any{"project": project, "text": "---\non: pr.merged\nuses: ghosttree/echo\n---\n", "base": 1})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a run token changes a workflow: %d", resp.StatusCode)
	}
	if err := st.FinishWorkflowRun(run.ID, store.WorkflowRunOutcome{State: store.RunDone}); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeAIRunTokens(run.ID); err != nil {
		t.Fatal(err)
	}
	resp = req(t, "GET", srv.URL+"/api/whoami", token, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token after the run: %d", resp.StatusCode)
	}
	if !strings.HasPrefix(token, store.AIRunTokenPrefix) {
		t.Fatal(token)
	}
}
