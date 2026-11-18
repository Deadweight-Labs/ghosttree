package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Usability run 5, WP-R5-1 (REQ-703): a run that has no result says in words
// what went wrong, which connection and model it used, where that is fixed
// and by whom; leads start it again; proposals of a run lead to the review.

// endRun makes a run of workflow for a new idea event and ends it as out says,
// the way the executor would.
func endRun(t *testing.T, st *store.Store, workflowName, key string, out store.WorkflowRunOutcome) store.WorkflowRun {
	t.Helper()
	if _, err := st.SetWorkflowTriggerAsOperator(store.WorkflowTrigger{Project: shellProject, Workflow: workflowName, EventKind: store.EventIdeaCreated, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RecordWorkflowEvent(store.WorkflowEventInput{Kind: store.EventIdeaCreated, Project: shellProject, SubjectKind: "idea", SubjectID: "7",
		Data: map[string]string{"title": "Neue Idee"}, Origin: store.EventOrigin{Kind: store.OriginHuman}, DedupeKey: key}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DispatchWorkflowEvents(10); err != nil {
		t.Fatal(err)
	}
	run, _, ok, err := st.ClaimWorkflowRun()
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := st.FinishWorkflowRun(run.ID, out); err != nil {
		t.Fatal(err)
	}
	run, _, err = st.WorkflowRunByID(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestFailedRunSaysInWordsWhatWentWrongWithConnectionAndModel(t *testing.T) {
	e, _ := workflowEnv(t)
	run := endRun(t, e.St, "idee-abgleich", "fail-404", store.WorkflowRunOutcome{State: store.RunFailed, Reason: "LLM HTTP 404: 404 page not found",
		Connection: "standard", Model: "jev-text-cpu:latest", Inputs: "prompt"})
	href := "/ui/x/shell/workflows/runs/" + itoa(run.ID)
	_, page := fetchPage(t, e.Lead, e.Base+href)
	mustContain(t, "failed run", page,
		"No model answers at the address of the connection (HTTP 404). Usually the base address is wrong.",
		"Connections and caps are kept by the owners of Alpha.", `href="/ui/orgs?org=alpha#ai"`,
		`<summary>Message in full</summary>`, "LLM HTTP 404: 404 page not found",
		"standard", "jev-text-cpu:latest", "0.00 $",
		"No output: the run ended before a model answered.", `class="box wfin"`,
		`action="/ui/workflows/rerun"`, "Run again")
	// The list says the same in short.
	_, list := fetchPage(t, e.Lead, e.Base+"/ui/x/shell/workflows")
	mustContain(t, "list", list, "No model answers at the address of the connection")
	mustNotContain(t, "list", list, "404 page not found")
	// A member reads the page but cannot start the run again.
	_, member := fetchPage(t, e.Member, e.Base+href)
	mustContain(t, "member", member, "No model answers at the address")
	mustNotContain(t, "member", member, `action="/ui/workflows/rerun"`)
}

func TestSkippedRunNamesThePolicyAndWhoSetsIt(t *testing.T) {
	e, _ := workflowEnv(t)
	run := endRun(t, e.St, "idee-abgleich", "skip-policy", store.WorkflowRunOutcome{State: store.RunSkipped, Reason: "policy.instruction"})
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/workflows/runs/"+itoa(run.ID))
	mustContain(t, "skipped run", page, "The workflow policy forbids an own instruction to the model, and this workflow has one",
		"The policy is set by the owners of Alpha.", `href="/ui/orgs?org=alpha#workflows"`, "Workflow policy")
	mustNotContain(t, "skipped run", page, "stage 2", "Message in full")
}

func TestLeadStartsAFailedRunAgainForTheSameIdea(t *testing.T) {
	e, done := workflowEnv(t)
	e.St.SetAccessMode(store.AccessMode{Enforce: true})
	run := endRun(t, e.St, "idee-abgleich", "fail-again", store.WorkflowRunOutcome{State: store.RunFailed, Reason: "LLM HTTP 503: down"})
	post := func(c *http.Client, id int64) *http.Response {
		t.Helper()
		resp := postForm(t, e, c, "/ui/workflows/rerun", url.Values{"project": {shellProject}, "id": {strconv.FormatInt(id, 10)}})
		resp.Body.Close()
		return resp
	}
	if resp := post(e.Member, run.ID); resp.StatusCode == http.StatusSeeOther {
		t.Fatalf("a member started a run again: %d", resp.StatusCode)
	}
	resp := post(e.Lead, run.ID)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("lead: %d", resp.StatusCode)
	}
	runs, _ := e.St.WorkflowRunsIn(shellProject, store.WorkflowRunFilter{Workflow: "idee-abgleich"})
	if len(runs) != 2 {
		t.Fatalf("runs after the rerun: %+v", runs)
	}
	again := runs[0]
	if again.ID == run.ID || again.State != store.RunQueued || again.SubjectKind != "idea" || again.SubjectID != "7" || again.ChainDepth != run.ChainDepth ||
		resp.Header.Get("Location") != "/ui/x/shell/workflows/runs/"+itoa(again.ID) {
		t.Fatalf("rerun: %+v (location %s)", again, resp.Header.Get("Location"))
	}
	ev, err := e.St.WorkflowEventByID(again.EventID)
	if err != nil || ev.DispatchedAt == "" || ev.Origin.Kind != store.OriginHuman || ev.Data["title"] != "Neue Idee" {
		t.Fatalf("event of the rerun: %+v %v", ev, err)
	}
	// The copied event starts no other trigger: the dispatcher has nothing to do.
	if events, _ := e.St.WorkflowWorkPending(); events {
		t.Fatal("the rerun left an event for the dispatcher")
	}
	// A run that ended done delivered already and is not started again.
	if resp := post(e.Lead, done.ID); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/x/shell/workflows/runs/"+itoa(done.ID) {
		t.Fatalf("rerun of a done run: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if runs, _ := e.St.WorkflowRunsIn(shellProject, store.WorkflowRunFilter{}); len(runs) != 3 {
		t.Fatalf("a done run was started again: %d runs", len(runs))
	}
	_, page := fetchPage(t, e.Lead, e.Base+"/ui/x/shell/workflows/runs/"+itoa(done.ID))
	mustNotContain(t, "done run", page, `action="/ui/workflows/rerun"`)
}

// AC-703.3 with a fake provider: an idea starts the template "idee-abgleich",
// its proposals wait in review and the run page and the workflows tab lead
// there; accepted, they apply. With the automatic approval for relations, the
// next run applies its link at once and the run page takes it back.
func TestIdeaMatchProposalLeadsToReviewAndAutoApprovalIsUndone(t *testing.T) {
	e := e3Web(t)
	pa := e.St.Access(store.Principal{ID: "person:1", Label: "alice"})
	actor := idea.Actor{AccountID: 1, Person: "alice"}
	f := createFeature(t, pa, "Workflows", "Abläufe, die Ereignisse auslösen")
	e.fromTemplate(t, "idee-abgleich")
	e.provider.answer = func(system, user string) string {
		return jsonText(map[string]any{
			"comment":   map[string]any{"body": "Passt zu FEAT-" + itoa(f) + ".", "evidence": []string{"Abläufe, die Ereignisse auslösen"}},
			"relations": []map[string]any{{"feature": "FEAT-" + itoa(f), "evidence": []string{"Abläufe, die Ereignisse auslösen"}}},
		})
	}
	first, err := pa.CreateIdea(store.CreateIdeaInput{Project: shellProject, Title: "Workflows aus Vorlagen", Body: "Abläufe, die Ereignisse auslösen, per Klick.", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	e.tick(t)
	run := e.lastRunOf(t, "idee-abgleich")
	if run.State != store.RunDone || run.Connection != "team" || run.Model != "claude-haiku-5-5" {
		t.Fatalf("run: %+v", run)
	}
	runHref := "/ui/x/shell/workflows/runs/" + itoa(run.ID)
	_, page := fetchPage(t, e.Owner, e.Base+runHref)
	mustContain(t, "run page", page, "2 proposals wait in review", `href="/ui/x/shell/review"`, ">Review</a>")
	_, list := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/workflows")
	mustContain(t, "workflows tab", list, "2 proposals wait in review", `class="box wfwait"`)
	// The idea names the run it started.
	_, ideaPage := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/ideas/"+itoa(first.ID))
	mustContain(t, "idea page", ideaPage, "wfside", `href="`+runHref+`"`, "ui-workflows.css")
	results := e.resultsOf(t, run)
	link, ok := resultWith(results, "relation", store.ResultProposed, "")
	if !ok {
		t.Fatalf("results: %+v", results)
	}
	_, review := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/review")
	mustContain(t, "review", review, fmt.Sprintf("IDEA-%d affects FEAT-%d", first.ID, f))
	e.approve(t, link.NoteID)
	if got, _ := pa.IdeaView(first.ID); len(got.Features) != 1 {
		t.Fatalf("accepted link: %+v", got.Features)
	}
	_, page = fetchPage(t, e.Owner, e.Base+runHref)
	mustContain(t, "after accepting", page, "1 proposal waits in review", "Accepted")

	// With the automatic approval switched on for knowledge, "knowledge after a
	// merge" applies its entry at once, and the run page takes it back. (The
	// link of an idea to a feature applies automatically when "relation" is on:
	// see TestIdeaLinkAppliesAutomatically...)
	if err := e.St.SetWorkflowPolicy("person:1", e.org.ID, store.WorkflowPolicy{AutoKinds: []string{"knowledge"}}); err != nil {
		t.Fatal(err)
	}
	e.fromTemplate(t, "wissen-nach-merge")
	e.provider.answer = func(system, user string) string {
		return jsonText(map[string]any{"known": []string{}, "proposals": []map[string]any{
			{"type": "pitfall", "title": "GitHub stellt Webhooks doppelt zu", "body": "Ohne Schlüssel entstünden zwei Läufe.", "evidence": []string{"Der Webhook kommt manchmal doppelt"}}}})
	}
	e.merge(t, 41, "fix: Webhook doppelt", "Der Webhook kommt manchmal doppelt; wir prüfen den Schlüssel.")
	e.close(t, 41, "fix: Webhook doppelt")
	e.tick(t)
	run2 := e.lastRunOf(t, "wissen-nach-merge")
	auto, ok := resultWith(e.resultsOf(t, run2), "knowledge", store.ResultAuto, "")
	if !ok {
		t.Fatalf("results of the merge run: %+v", e.resultsOf(t, run2))
	}
	kn, _ := strconv.ParseInt(strings.TrimPrefix(auto.Ref, "KN-"), 10, 64)
	if k, err := e.St.KnowledgeByID(kn); err != nil || k.Confidence == "staged" || k.Status != "active" {
		t.Fatalf("automatic entry: %+v %v", k, err)
	}
	run2Href := "/ui/x/shell/workflows/runs/" + itoa(run2.ID)
	_, page = fetchPage(t, e.Owner, e.Base+run2Href)
	mustContain(t, "auto run", page, "Applied automatically", `action="/ui/workflows/undo"`)
	resp := sameOriginPostForm(t, e.Owner, e.Base+"/ui/workflows/undo", url.Values{"csrf_token": {csrfOn(t, page)}, "id": {itoa(auto.ID)}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != run2Href {
		t.Fatalf("undo: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if k, err := e.St.KnowledgeByID(kn); err != nil || k.Status != "deprecated" {
		t.Fatalf("entry after undo: %+v %v", k, err)
	}
	_, page = fetchPage(t, e.Owner, e.Base+run2Href)
	mustContain(t, "after undo", page, "Undone")
	if strings.Contains(page, `action="/ui/workflows/undo"`) {
		t.Fatal("undo still offered")
	}
}

func createFeature(t *testing.T, pa *store.ProjectAccess, name, summary string) int64 {
	t.Helper()
	f, err := pa.CreateFeature(store.CreateFeatureInput{Project: shellProject, Content: feature.Patch{Name: &name, Summary: &summary}, Actor: feature.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	return f.ID
}
