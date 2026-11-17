package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-693 (review of E0, finding 3): the session reference "workflow-run:<id>"
// is what makes an event a run's own, so only the run may write it. Everyone
// else is refused wherever a reference is accepted.
func TestNobodyButTheRunWritesTheRunReference(t *testing.T) {
	f := accessAPI(t, true)
	const ref = "workflow-run:1"
	for _, who := range []string{"robin", "lena", "mia"} {
		for name, call := range map[string]struct {
			path string
			body map[string]any
		}{
			"knowledge": {"/api/knowledge", map[string]any{"type": "note", "title": "Aus einem Lauf", "body": "x", "scope": map[string]string{"project": accProject},
				"confidence": "staged", "origin": "agent", "harness": "claude", "session_ref": ref}},
			"knowledge in other case": {"/api/knowledge", map[string]any{"type": "note", "title": "Aus einem Lauf", "body": "x", "scope": map[string]string{"project": accProject},
				"confidence": "staged", "origin": "agent", "harness": "claude", "session_ref": " Workflow-Run:7"}},
			"request": {"/api/requests", map[string]any{"project": accProject, "type": "feature", "title": "Aus einem Lauf", "criteria": []string{"a"}, "session_ref": ref}},
		} {
			code, out := f.call(t, who, "POST", call.path, call.body)
			if code != 403 || !strings.Contains(out, "workflow_run_reserved") {
				t.Errorf("%s writing %s with %q = %d (%s)", who, name, ref, code, strings.TrimSpace(out))
			}
		}
		// A session named like a run is refused at the door, so no idea can
		// pick it up as its reference later.
		code, out := f.call(t, who, "POST", "/api/sessions", map[string]any{"harness": "claude", "external_id": ref, "scope": map[string]string{"project": accProject}})
		if code != 403 || !strings.Contains(out, "workflow_run_reserved") {
			t.Errorf("%s registering a session named like a run = %d (%s)", who, code, strings.TrimSpace(out))
		}
	}
	// What the same callers write without the reference still works.
	f.expect(t, "mia", 200, "POST", "/api/knowledge", map[string]any{"type": "note", "title": "Gewöhnlich", "body": "x", "scope": map[string]string{"project": accProject},
		"confidence": "staged", "origin": "agent", "harness": "claude", "session_ref": "plain"})
}

// The unit behind it: the run's own principal may name its own reference and
// no other, its writes carry it whatever the request names, and the session
// header with a run reference counts for nobody else.
func TestTheRunsPrincipalCarriesItsOwnReference(t *testing.T) {
	a := &api{}
	run := store.Principal{ID: "person:3", Label: "workflow", TokenKind: store.WorkflowRunKind, RunID: 5}
	plain := store.Principal{ID: "person:3", Label: "mia"}
	req := func(p store.Principal) *http.Request {
		r := httptest.NewRequest("POST", "/api/knowledge", nil)
		r.Header.Set(originctx.SessionHeader, "workflow-run:5")
		return r.WithContext(context.WithValue(r.Context(), personKey{}, p))
	}
	gate := func(p store.Principal, ref string) int {
		w := httptest.NewRecorder()
		if a.gateWorkflowRunRef(w, req(p), ref) {
			return 200
		}
		return w.Code
	}
	if gate(run, "workflow-run:5") != 200 || gate(run, "WORKFLOW-RUN:5") != 200 || gate(run, "") != 200 {
		t.Error("the run may name its own reference")
	}
	if gate(run, "workflow-run:6") != 403 || gate(plain, "workflow-run:5") != 403 {
		t.Error("another run's reference, or the run's reference from a plain account, is refused")
	}
	if got := runRefOf(req(run), "something-else"); got != "workflow-run:5" {
		t.Errorf("what a run writes carries its reference: %q", got)
	}
	if got := runRefOf(req(plain), "something-else"); got != "something-else" {
		t.Errorf("a plain account keeps its own reference: %q", got)
	}
	if sessionHeader(req(plain)) != "" {
		t.Error("the run header counts for a plain account")
	}
	if sessionHeader(req(run)) != "workflow-run:5" {
		t.Error("the run's own header is lost")
	}
}
