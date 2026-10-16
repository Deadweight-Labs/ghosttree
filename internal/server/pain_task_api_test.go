package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

func decode[T any](t *testing.T, body string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return v
}

func (f *accessAPIFixture) newTask(t *testing.T, who string, body map[string]any) humantask.Task {
	t.Helper()
	if _, ok := body["project"]; !ok {
		body["project"] = accProject
	}
	return decode[humantask.Task](t, f.expect(t, who, 201, "POST", "/api/tasks", body))
}

func TestHumanTaskAPILifecycleAndErrors(t *testing.T) {
	f := accessAPI(t, true)
	rid := f.id["r-mia"]
	task := f.newTask(t, "mia", map[string]any{"kind": "secret", "title": "Key anlegen", "location": "Agent Vault: stripe/prod", "check_hint": "Eintrag existiert", "request_id": rid})
	id := task.ID

	// Fehler: Pflichtfelder, Secret, unbekannter Request.
	bad := map[string]any{"project": accProject, "kind": "secret", "title": "x", "request_id": rid}
	out := f.expect(t, "mia", 400, "POST", "/api/tasks", bad)
	if !strings.Contains(out, "location_required") {
		t.Errorf("missing location: %s", out)
	}
	leak := "sk-" + strings.Repeat("a1", 12)
	out = f.expect(t, "mia", 422, "POST", "/api/tasks", map[string]any{"project": accProject, "kind": "input", "title": "Key " + leak, "request_id": rid})
	if !strings.Contains(out, "secret_detected") || strings.Contains(out, leak) {
		t.Errorf("secret answer: %s", out)
	}
	f.expect(t, "mia", 400, "POST", "/api/tasks", map[string]any{"project": accProject, "kind": "input", "title": "x", "request_id": 99999})
	f.expect(t, "mia", 400, "POST", "/api/tasks", map[string]any{"project": accProject, "kind": "input", "title": "x"})
	f.expect(t, "mia", 400, "POST", "/api/tasks", map[string]any{"project": accOther, "kind": "input", "title": "x", "request_id": rid})
	f.expect(t, "gus", 403, "POST", "/api/tasks", map[string]any{"project": accProject, "kind": "input", "title": "x", "request_id": rid})
	f.expect(t, "nora", 409, "POST", "/api/tasks", map[string]any{"project": accProject, "kind": "input", "title": "x", "request_id": rid})

	// Übergänge.
	f.expect(t, "mia", 409, "POST", idPath("/api/tasks/%d/confirm", id), nil)
	done := decode[[]humantask.Task](t, f.expect(t, "lena", 200, "POST", "/api/tasks/done", map[string]any{"ids": []int64{id}, "note": "gesetzt"}))
	if len(done) != 1 || done[0].State != "done" || done[0].DoneVia != "token" {
		t.Errorf("done: %+v", done)
	}
	f.expect(t, "mia", 400, "POST", idPath("/api/tasks/%d/reopen", id), map[string]any{})
	f.expect(t, "mia", 200, "POST", idPath("/api/tasks/%d/reopen", id), map[string]any{"reason": "leer"})
	f.expect(t, "mia", 200, "POST", "/api/tasks/done", map[string]any{"ids": []int64{id}})
	f.expect(t, "mia", 200, "POST", idPath("/api/tasks/%d/confirm", id), nil)
	f.expect(t, "mia", 409, "POST", idPath("/api/tasks/%d/drop", id), map[string]any{"reason": "x"})

	// Aktivität am Request in dieser Reihenfolge.
	detail := decode[requestdomain.Detail](t, f.expect(t, "mia", 200, "GET", idPath("/api/requests/%d", rid), nil))
	var kinds []string
	for _, a := range detail.Activity {
		if strings.HasPrefix(a.Kind, "task.") {
			kinds = append(kinds, a.Kind)
		}
	}
	if got := strings.Join(kinds, " "); got != "task.created task.done task.reopened task.done task.confirmed" {
		t.Errorf("activity: %s", got)
	}
	if len(detail.Tasks) != 1 {
		t.Errorf("request tasks: %+v", detail.Tasks)
	}
}

func TestHumanTaskAPIGuestSeesOnlyAssignedAndGetsNotFound(t *testing.T) {
	f := accessAPI(t, true)
	rid := f.id["r-mia"]
	open := f.newTask(t, "mia", map[string]any{"kind": "input", "title": "nur Mitglieder", "request_id": rid})
	mine := f.newTask(t, "mia", map[string]any{"kind": "input", "title": "für Gus", "request_id": rid, "assignee": "gus"})
	f.expect(t, "gus", 404, "GET", idPath("/api/tasks/%d", open.ID), nil)
	f.expect(t, "gus", 200, "GET", idPath("/api/tasks/%d", mine.ID), nil)
	f.expect(t, "gus", 404, "POST", "/api/tasks/done", map[string]any{"ids": []int64{mine.ID, open.ID}})
	list := decode[[]humantask.Task](t, f.expect(t, "gus", 200, "GET", "/api/tasks?project="+accProject+"&state=all", nil))
	if len(list) != 1 || list[0].ID != mine.ID {
		t.Errorf("guest list: %+v", list)
	}
	// Request-Detail: Gast sieht nur die zugewiesene Aufgabe und keine task-Aktivität.
	body := f.expect(t, "gus", 200, "GET", idPath("/api/requests/%d", rid), nil)
	if strings.Contains(body, "nur Mitglieder") || strings.Contains(body, "task.created") {
		t.Errorf("guest request detail leaks tasks: %s", body)
	}
	if !strings.Contains(body, "für Gus") {
		t.Errorf("guest does not see the assigned task: %s", body)
	}
	f.expect(t, "gus", 200, "POST", "/api/tasks/done", map[string]any{"ids": []int64{mine.ID}, "note": "ok"})
	f.expect(t, "gus", 403, "POST", idPath("/api/tasks/%d/confirm", mine.ID), nil)
	f.expect(t, "nora", 404, "GET", "/api/tasks?project="+accProject, nil)
	// Zuweisung an ein Nichtmitglied.
	out := f.expect(t, "mia", 400, "POST", "/api/tasks", map[string]any{"project": accProject, "kind": "input", "title": "x", "request_id": rid, "assignee": "nora"})
	if !strings.Contains(out, "assignee_not_member") {
		t.Errorf("assignee: %s", out)
	}
}

func TestBootstrapShowsWaitingTasks(t *testing.T) {
	f := accessAPI(t, true)
	rid := f.id["r-mia"]
	boot := func(who string) string {
		return f.expect(t, who, 200, "GET", "/api/context/bootstrap?project="+accProject, nil)
	}
	if got := boot("mia"); strings.Contains(got, "Waiting on the human") {
		t.Errorf("section shown without tasks:\n%s", got)
	}
	done := f.newTask(t, "mia", map[string]any{"kind": "file", "title": "Schema-Dump bereitlegen", "location": "./db/schema.sql", "check_hint": "Datei existiert", "request_id": rid})
	forGus := f.newTask(t, "mia", map[string]any{"kind": "secret", "title": "Key für Gus", "location": ".env.local", "request_id": rid, "assignee": "gus"})
	for i := 0; i < 7; i++ {
		f.newTask(t, "mia", map[string]any{"kind": "input", "title": fmt.Sprintf("Offene Frage %d", i), "request_id": rid})
	}
	long := strings.Repeat("lang ", 60)
	f.expect(t, "mia", 200, "POST", "/api/tasks/done", map[string]any{"ids": []int64{done.ID}, "note": long})
	out := boot("mia")
	i1, i2 := strings.Index(out, "Done by the human"), strings.Index(out, "Still open")
	if i1 < 0 || i2 < i1 {
		t.Fatalf("order of the groups:\n%s", out)
	}
	if !strings.Contains(out, fmt.Sprintf("TASK-%d [file] Schema-Dump bereitlegen -> ./db/schema.sql (REQ-%d) | check: Datei existiert | human: ", done.ID, rid)) {
		t.Errorf("done line:\n%s", out)
	}
	if !strings.Contains(out, "marked via token") || !strings.Contains(out, "…") {
		t.Errorf("token marker or truncation missing:\n%s", out)
	}
	if n := strings.Count(out[i2:], "\n- TASK-"); n != 5 {
		t.Errorf("open lines capped at 5, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "(+3 more: ctx task list)") || !strings.Contains(out, ", for gus") {
		t.Errorf("more-line or assignee missing:\n%s", out)
	}
	if strings.Index(out, "## Waiting on the human") < strings.Index(out, "## Work ledger") {
		t.Errorf("section before the work ledger:\n%s", out)
	}
	// Gast: nur das Zugewiesene, auch ohne Lesen auf Session-Metadaten.
	g := boot("gus")
	if !strings.Contains(g, fmt.Sprintf("TASK-%d", forGus.ID)) || strings.Contains(g, "Offene Frage") || strings.Contains(g, "Schema-Dump") {
		t.Errorf("guest bootstrap:\n%s", g)
	}
	if got := boot("nora"); strings.Contains(got, "Waiting on the human") {
		t.Errorf("stranger sees the section:\n%s", got)
	}
}

func TestPainAPICriteria(t *testing.T) {
	f := accessAPI(t, true)
	r1, r2 := f.id["r-mia"], f.id["r-robin"]
	kid := f.id["k-mia"]
	create := map[string]any{"project": accProject, "title": "Schreiben geht nur ganz", "body": "Text",
		"motivates": []map[string]any{{"request_id": r1}, {"request_id": r2}, {"knowledge_id": kid}, {"external_ref": "https://example.invalid/1"}}}
	d := decode[pain.Detail](t, f.expect(t, "mia", 201, "POST", "/api/pains", create))
	if d.Pain.State != "active" || len(d.Motivates) != 4 {
		t.Fatalf("pain: %+v", d)
	}
	pid := d.Pain.ID
	f.expect(t, "gus", 403, "POST", "/api/pains", create)
	f.expect(t, "nora", 404, "GET", idPath("/api/pains/%d", pid), nil)
	f.expect(t, "gus", 200, "GET", idPath("/api/pains/%d", pid), nil)

	// Zustände und Fehler.
	f.expect(t, "mia", 400, "POST", idPath("/api/pains/%d/state", pid), map[string]any{"state": "mitigated", "evidence_kind": "commit", "evidence_ref": "abc"})
	f.expect(t, "mia", 400, "POST", idPath("/api/pains/%d/state", pid), map[string]any{"state": "resolved"})
	f.expect(t, "mia", 409, "POST", idPath("/api/pains/%d/state", pid), map[string]any{"state": "active", "note": "x"})
	f.expect(t, "mia", 200, "POST", idPath("/api/pains/%d/state", pid), map[string]any{"state": "mitigated", "remaining": "Lesen fehlt", "evidence_kind": "request", "evidence_ref": fmt.Sprintf("REQ-%d", r1)})
	f.expect(t, "gus", 403, "POST", idPath("/api/pains/%d/state", pid), map[string]any{"state": "obsolete", "note": "x"})

	// Rückweg am Request und Suche.
	detail := decode[requestdomain.Detail](t, f.expect(t, "gus", 200, "GET", idPath("/api/requests/%d", r1), nil))
	if len(detail.Pains) != 1 || detail.Pains[0].Remaining != "Lesen fehlt" {
		t.Errorf("request pains: %+v", detail.Pains)
	}
	list := decode[[]pain.Pain](t, f.expect(t, "mia", 200, "GET", "/api/pains?project="+accProject, nil))
	if len(list) != 1 {
		t.Errorf("list: %+v", list)
	}
	f.expect(t, "nora", 404, "GET", "/api/pains?project="+accProject, nil)
	f.expect(t, "mia", 400, "GET", "/api/pains", nil)

	// Kante entfernen verlangt einen Grund.
	f.expect(t, "mia", 400, "DELETE", idPath("/api/pain-links/%d", d.Motivates[0].ID), nil)
	f.expect(t, "mia", 204, "DELETE", idPath("/api/pain-links/%d?reason=falsch", d.Motivates[0].ID), nil)
	f.expect(t, "mia", 200, "PATCH", idPath("/api/pains/%d", pid), map[string]any{"title": "Neu", "reason": "Tippfehler"})
}
