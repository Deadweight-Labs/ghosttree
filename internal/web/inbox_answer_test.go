package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Die Weboberfläche beantwortet einen Eintrag des Posteingangs mit einem Vorschlag:
// nur eine interaktive Sitzung mit CSRF-Token, nur wer die Aufgabe sehen darf.
func TestInboxAnswerFormAnswersATaskForAnInteractiveSession(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	task := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Kind: "input", Title: "Welche Variante?", Options: []string{"Variante A", "Variante B"}})
	form := func() url.Values {
		return url.Values{"kind": {"task"}, "id": {strconv.FormatInt(task.ID, 10)}, "option": {"2"}}
	}
	// Ohne CSRF-Token: 403, nichts geschieht.
	if resp := sameOriginPostForm(t, e.Member, e.Base+"/ui/inbox/answer", form()); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("without a csrf token: %d", resp.StatusCode)
	}
	// Eine Sitzung aus eingefügtem Token ist kein Mensch im Browser.
	personal, _, err := e.St.CreateToken("anna", store.TokenSpec{Label: "bearer"})
	if err != nil {
		t.Fatal(err)
	}
	if resp := tpPost(t, e, login(t, e.Srv, personal), "/ui/inbox/answer", form()); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a pasted token answered: %d", resp.StatusCode)
	}
	// Wer die Aufgabe nicht sehen darf (Gast, nicht der Adressat), findet sie nicht.
	if resp := tpPost(t, e, e.Guest, "/ui/inbox/answer", form()); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("guest: %d", resp.StatusCode)
	}
	if got, _ := e.St.TaskByID(task.ID); got.State != humantask.Open {
		t.Fatalf("a refused answer changed the task: %+v", got)
	}
	bad := form()
	bad.Set("option", "9")
	if resp := tpPost(t, e, e.Member, "/ui/inbox/answer", bad); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("an option that is not offered: %d", resp.StatusCode)
	}
	resp := tpPost(t, e, e.Member, "/ui/inbox/answer", form())
	out := body(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(out, `"Variante B"`) {
		t.Fatalf("answer: %d %s", resp.StatusCode, out)
	}
	if got, _ := e.St.TaskByID(task.ID); got.State != humantask.Done || got.DoneNote != "Variante B" || got.DoneVia != "web" {
		t.Fatalf("task after the answer: %+v", got)
	}
}
