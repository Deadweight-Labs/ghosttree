package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

func TestRequestAPICreateSearchAndRejectIncompleteCompletion(t *testing.T) {
	srv, token := newTestServer(t)
	resp := req(t, "POST", srv.URL+"/api/requests", token, map[string]any{
		"type": "feature", "title": "request ledger", "description": "associate sessions",
		"project": "github.com/x/y", "criteria": []string{"search works"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var created requestdomain.Detail
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Request.State != "open" || len(created.Criteria) != 1 {
		t.Fatalf("created = %+v", created)
	}

	resp = req(t, "GET", srv.URL+"/api/requests/search?q=sessions&project=github.com/x/y", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search status = %d", resp.StatusCode)
	}
	var page requestdomain.SearchPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 1 || page.Results[0].Request.ID != created.Request.ID {
		t.Fatalf("search = %+v", page)
	}

	resp = req(t, "POST", fmt.Sprintf("%s/api/requests/%d/complete", srv.URL, created.Request.ID), token,
		map[string]string{"evidence_kind": "commit", "evidence_ref": "abc"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("complete status = %d, want 409", resp.StatusCode)
	}
	var apiErr map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&apiErr); err != nil {
		t.Fatal(err)
	}
	if apiErr["code"] != "open_criteria" || apiErr["resolution"] == "" {
		t.Fatalf("error = %+v", apiErr)
	}
}

func TestRequestAPIWorkAndCriterionLifecycle(t *testing.T) {
	srv, token := newTestServer(t)
	sessionResp := req(t, "POST", srv.URL+"/api/sessions", token, map[string]any{"harness": "codex", "external_id": "api-work"})
	var session struct {
		ID int64 `json:"id"`
	}
	_ = json.NewDecoder(sessionResp.Body).Decode(&session)
	createResp := req(t, "POST", srv.URL+"/api/requests", token, map[string]any{"type": "change", "title": "work lifecycle"})
	var detail requestdomain.Detail
	_ = json.NewDecoder(createResp.Body).Decode(&detail)

	resp := req(t, "POST", fmt.Sprintf("%s/api/requests/%d/work", srv.URL, detail.Request.ID), token,
		map[string]any{"session_id": session.ID, "role": "primary"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start work status = %d", resp.StatusCode)
	}
	var started struct {
		Work requestdomain.Work `json:"work"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&started)

	resp = req(t, "POST", fmt.Sprintf("%s/api/requests/%d/criteria", srv.URL, detail.Request.ID), token,
		map[string]string{"description": "API works"})
	var criterion requestdomain.Criterion
	_ = json.NewDecoder(resp.Body).Decode(&criterion)
	resp = req(t, "PATCH", fmt.Sprintf("%s/api/criteria/%d", srv.URL, criterion.ID), token,
		map[string]string{"state": "met", "evidence_kind": "test", "evidence_ref": "go test ./internal/server"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("criterion status = %d", resp.StatusCode)
	}

	resp = req(t, "PATCH", fmt.Sprintf("%s/api/request-work/%d", srv.URL, started.Work.ID), token,
		map[string]string{"state": "paused", "summary": "API done; MCP remains"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("finish work status = %d", resp.StatusCode)
	}
	resp = req(t, "POST", fmt.Sprintf("%s/api/requests/%d/complete", srv.URL, detail.Request.ID), token,
		map[string]string{"evidence_kind": "commit", "evidence_ref": "abc"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete status = %d", resp.StatusCode)
	}
}
