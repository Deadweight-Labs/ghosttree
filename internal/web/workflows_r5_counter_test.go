package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-713, counter-check of run 5. N-A: with the automatic approval on for
// "relation", the idea-to-feature link of "idee-abgleich" applies at once and
// the run page takes it back; the comment still waits. N-G: the run page says
// how the results stand now. N-C: a member sees where the proposals wait and
// who decides.
func TestIdeaLinkAppliesAutomaticallyAndTheRunPageSumsUpTheResultsAsTheyStandNow(t *testing.T) {
	e := e3Web(t)
	e.St.SetAccessMode(store.AccessMode{Enforce: true})
	pa := e.St.Access(store.Principal{ID: "person:1", Label: "alice"})
	f := createFeature(t, pa, "Workflows", "Abläufe, die Ereignisse auslösen")
	e.fromTemplate(t, "idee-abgleich")
	e.provider.answer = func(system, user string) string {
		return jsonText(map[string]any{
			"comment":   map[string]any{"body": "Passt zu FEAT-" + itoa(f) + ".", "evidence": []string{"Abläufe, die Ereignisse auslösen"}},
			"relations": []map[string]any{{"feature": "FEAT-" + itoa(f), "evidence": []string{"Abläufe, die Ereignisse auslösen"}}},
		})
	}
	if err := e.St.SetWorkflowPolicy("person:1", e.org.ID, store.WorkflowPolicy{AutoKinds: []string{"relation"}}); err != nil {
		t.Fatal(err)
	}
	i, err := pa.CreateIdea(store.CreateIdeaInput{Project: shellProject, Title: "Workflows aus Vorlagen", Body: "Abläufe, die Ereignisse auslösen, per Klick.", Actor: idea.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	e.tick(t)
	run := e.lastRunOf(t, "idee-abgleich")
	results := e.resultsOf(t, run)
	link, ok := resultWith(results, "relation", store.ResultAuto, "")
	if !ok || link.Ref != "FEAT-"+itoa(f) {
		t.Fatalf("results: %+v", results)
	}
	comment, ok := resultWith(results, "comment", store.ResultProposed, "")
	if !ok {
		t.Fatalf("results: %+v", results)
	}
	if got, _ := pa.IdeaView(i.ID); len(got.Features) != 1 {
		t.Fatalf("the link was not applied: %+v", got.Features)
	}
	runHref := "/ui/x/shell/workflows/runs/" + itoa(run.ID)
	_, page := fetchPage(t, e.Owner, e.Base+runHref)
	mustContain(t, "run page", page, "1 waiting for review · 1 applied automatically", "Applied automatically", `action="/ui/workflows/undo"`)
	// A member sees the proposal and who decides, and no way to decide.
	_, member := fetchPage(t, e.Member, e.Base+runHref)
	mustContain(t, "member", member, `class="needrole"`, "Needs Lead")
	mustNotContain(t, "member", member, ">Review</a>", `action="/ui/workflows/undo"`)
	_, list := fetchPage(t, e.Member, e.Base+"/ui/x/shell/workflows")
	mustContain(t, "member list", list, "1 proposal waits in review", `class="needrole"`)
	// The same on the proposal itself: the entry says who verifies it.
	_, entry := fetchPage(t, e.Member, e.Base+"/ui/knowledge/"+itoa(comment.NoteID))
	mustContain(t, "member entry", entry, `class="needrole"`, "Needs Lead")
	mustNotContain(t, "member entry", entry, `action="/ui/review/`+itoa(comment.NoteID)+`/approve"`)
	// Accepted: the summary follows, the sentence the run stored does not.
	e.approve(t, comment.NoteID)
	_, page = fetchPage(t, e.Owner, e.Base+runHref)
	mustContain(t, "after accepting", page, "1 accepted · 1 applied automatically")
	mustNotContain(t, "after accepting", page, "waiting for review")
	// Undo takes the link back.
	resp := sameOriginPostForm(t, e.Owner, e.Base+"/ui/workflows/undo", url.Values{"csrf_token": {csrfOn(t, page)}, "id": {itoa(link.ID)}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("undo: %d", resp.StatusCode)
	}
	if got, _ := pa.IdeaView(i.ID); len(got.Features) != 0 {
		t.Fatalf("the link survived the undo: %+v", got.Features)
	}
	_, page = fetchPage(t, e.Owner, e.Base+runHref)
	mustContain(t, "after undo", page, "1 accepted · 1 undone")
}

// N-D: a run skipped because the newest version fails the check leads to the
// workflow and shows the first error of the check.
func TestRunSkippedForAnInvalidVersionLeadsToTheWorkflowAndShowsTheError(t *testing.T) {
	e, _ := workflowEnv(t)
	text := "---\non: idea.created\nuses: ghosttree/gibt-es-nicht\n---\n"
	if _, err := e.St.SaveWorkflowDef("person:1", store.WorkflowDefSave{Project: shellProject, Name: "kaputt", Text: text, Event: "idea.created", Uses: "ghosttree/gibt-es-nicht"}); err != nil {
		t.Fatal(err)
	}
	run := endRun(t, e.St, "kaputt", "skip-invalid", store.WorkflowRunOutcome{State: store.RunSkipped, Reason: "invalid"})
	href := "/ui/x/shell/workflows/runs/" + itoa(run.ID)
	_, page := fetchPage(t, e.Lead, e.Base+href)
	mustContain(t, "lead", page, "The newest version does not pass the check", `href="/ui/x/shell/workflows/kaputt?edit=1"`, "Fix the workflow", "gibt-es-nicht", "Message in full")
	_, member := fetchPage(t, e.Member, e.Base+href)
	mustContain(t, "member", member, `href="/ui/x/shell/workflows/kaputt"`, "Open the workflow")
	mustNotContain(t, "member", member, "?edit=1")
}

// N-E: a run that is queued or running says so and reloads by itself; a run
// that is over does neither.
func TestQueuedRunSaysItWaitsAndReloadsByItself(t *testing.T) {
	e, _ := workflowEnv(t)
	if _, err := e.St.SetWorkflowTriggerAsOperator(store.WorkflowTrigger{Project: shellProject, Workflow: "idee-abgleich", EventKind: store.EventIdeaCreated, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.St.RecordWorkflowEvent(store.WorkflowEventInput{Kind: store.EventIdeaCreated, Project: shellProject, SubjectKind: "idea", SubjectID: "9",
		Data: map[string]string{"title": "x"}, Origin: store.EventOrigin{Kind: store.OriginHuman}, DedupeKey: "queued-9"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.St.DispatchWorkflowEvents(10); err != nil {
		t.Fatal(err)
	}
	runs, _ := e.St.WorkflowRunsIn(shellProject, store.WorkflowRunFilter{Workflow: "idee-abgleich"})
	if len(runs) != 1 || runs[0].State != store.RunQueued {
		t.Fatalf("runs: %+v", runs)
	}
	_, page := fetchPage(t, e.Lead, e.Base+"/ui/x/shell/workflows/runs/"+itoa(runs[0].ID))
	mustContain(t, "queued run", page, "Waiting for its turn. This page updates by itself.", `data-refresh="`, "wfspin")
	done := endRun(t, e.St, "idee-abgleich", "done-9", store.WorkflowRunOutcome{State: store.RunDone, Result: "ok"})
	_, over := fetchPage(t, e.Lead, e.Base+"/ui/x/shell/workflows/runs/"+itoa(done.ID))
	mustNotContain(t, "finished run", over, "updates by itself", `data-refresh="`)
}

// N-B: the address of a form that only takes POST leads back to the section.
func TestAddressOfAConnectionFormLeadsBackToTheSection(t *testing.T) {
	e := aiWeb(t)
	stay := *e.Owner
	stay.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := stay.Get(e.Base + "/ui/orgs/ai/connection?org=alpha")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/orgs?org=alpha#ai" {
		t.Fatalf("GET of the form address: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// S11, N-H, N-I: closing the discussion asks when the concept still lists open
// questions, also when every decision is made; the decided questions stand
// under the open questions as answered.
func TestClosingAsksForOpenQuestionsOfTheConceptAndDecidedOnesStandAnswered(t *testing.T) {
	e, pa := listEnv(t)
	p := wsPoster{t, e}
	i := plainIdea(t, pa, "Questions stay")
	phase := "/ui/ideas/" + strconv.FormatInt(i.ID, 10) + "/phase"
	if _, _, err := pa.PutIdeaConcept(i.ID, store.PutIdeaConceptInput{Actor: humanActor, Patch: idea.ConceptPatch{About: nStr("About."), Questions: nStr("Which queue?")}}); err != nil {
		t.Fatal(err)
	}
	p.post(e.Owner, phase, url.Values{"mode": {"discuss"}})
	d, err := pa.OpenDecision(store.OpenDecisionInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Question: "Which queue?", Way: idea.WayHuman, Actor: humanActor,
		Variants: []idea.VariantInput{{Title: "SQLite"}, {Title: "Redis"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp := p.post(e.Owner, "/ui/decisions/"+strconv.FormatInt(d.ID, 10)+"/close", url.Values{"variant": {"A"}, "reason": {"simple"}}); !strings.Contains(resp.Header.Get("Location"), "wdone=decided") {
		t.Fatalf("decide -> %q", resp.Header.Get("Location"))
	}
	_, page := fetchPage(t, e.Owner, e.Base+ideaPath(i))
	mustContain(t, "idea", page, `class="ws-ans"`, "Answered", "Which queue?")
	resp := p.post(e.Owner, phase, url.Values{"mode": {"close"}})
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "wask=close") {
		t.Fatalf("close with open questions in the concept -> %q", loc)
	}
	_, asked := fetchPage(t, e.Owner, e.Base+strings.SplitN(loc, "#", 2)[0])
	mustContain(t, "ask", asked, `id="ws-ask"`, "still lists open questions", `name="confirm" value="1"`)
	if got, _ := pa.IdeaView(i.ID); got.Phase != idea.PhaseDiscussion {
		t.Fatalf("the unconfirmed close went through: %s", got.Phase)
	}
	p.post(e.Owner, phase, url.Values{"mode": {"close"}, "confirm": {"1"}})
	if got, _ := pa.IdeaView(i.ID); got.Phase != idea.PhaseDiscussed {
		t.Fatalf("the confirmed close did not go through: %s", got.Phase)
	}
}
