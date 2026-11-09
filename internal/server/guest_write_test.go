package server

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
)

// Robin, 2026-11-09: Gäste lesen, sie schreiben nicht, und Wissen freigeben
// braucht das Prüfrecht. Beides gilt über die API (und damit MCP und CLI) auch
// im Log-Modus; unter Durchsetzung ohnehin.
func TestGuestWritesAndUnreviewedApprovalsAreRefusedByTheAPI(t *testing.T) {
	for _, enforce := range []bool{false, true} {
		f := accessAPI(t, enforce)
		staged := f.id["k-robin-staged"]
		rid := f.id["r-mia"]
		// Wissen freigeben: Gast und Mitglied ohne Prüfrecht nicht, rex (can_review) schon.
		f.expect(t, "gus", 403, "PATCH", idPath("/api/knowledge/%d", f.id["k-mia"]), map[string]string{"confidence": "verified"})
		f.expect(t, "mia", 403, "PATCH", idPath("/api/knowledge/%d", staged), map[string]string{"confidence": "verified"})
		f.expect(t, "rex", 204, "PATCH", idPath("/api/knowledge/%d", staged), map[string]string{"confidence": "verified"})
		// Ein Gast schreibt kein Wissen, keine Requests, keine Pains, keine Ideen.
		f.expect(t, "gus", 403, "PATCH", idPath("/api/knowledge/%d", f.id["k-mia"]), map[string]string{"body": "x"})
		f.expect(t, "gus", 403, "POST", "/api/requests", map[string]any{"type": "bug", "title": "x", "project": accProject})
		f.expect(t, "gus", 403, "POST", "/api/pains", map[string]any{"project": accProject, "title": "x"})
		f.expect(t, "gus", 403, "POST", "/api/ideas", map[string]any{"project": accProject, "title": "x"})
		// Aufgaben: auch die eigene meldet ein Gast nicht.
		mine := f.newTask(t, "mia", map[string]any{"kind": "input", "title": "für Gus", "request_id": rid, "assignee": "gus"})
		f.expect(t, "gus", 403, "POST", "/api/tasks/done", map[string]any{"ids": []int64{mine.ID}})
		if got, _ := f.st.TaskByID(mine.ID); got.State != humantask.Open {
			t.Errorf("enforce=%v: a guest marked a task done", enforce)
		}
		// Abläufe: weder anlegen noch eine Fassung schreiben.
		if code, out := f.call(t, "gus", "POST", "/api/runbooks", rbBody("guest-rollout", "vom Gast", false, nil)); code != 403 && code != 404 {
			t.Errorf("enforce=%v: guest writes a runbook: %d %s", enforce, code, out)
		}
		writeRunbookAPI(t, f, "mia", 201, rbBody("member-rollout", "vom Mitglied", false, nil))
		if code, out := f.call(t, "gus", "POST", "/api/runbooks", rbBody("member-rollout", "vom Gast", false, nil)); code != 403 && code != 404 {
			t.Errorf("enforce=%v: guest writes a runbook version: %d %s", enforce, code, out)
		}
	}
}
