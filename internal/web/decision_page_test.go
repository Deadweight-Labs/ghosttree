package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// issueDecision opens a decision of an agent on an issue that waits for a person, with two variants:
// the case DEC-1 of the usability run, a question without an idea.
func issueDecision(t *testing.T, e shellEnv, pa *store.ProjectAccess) (int64, idea.Decision) {
	t.Helper()
	req := rqMake(t, e.St, rqSpec{Title: "Check the webhook signature", Criteria: []string{"one"}})
	d, err := pa.OpenDecision(store.OpenDecisionInput{Subject: idea.SubjectRequest, SubjectID: req.Request.ID, Question: "Verify the signature strictly?",
		Detail: "Without it anyone can trigger the hook.", Way: idea.WayHuman, Actor: agentActor, Variants: []idea.VariantInput{
			{Title: "Strictly", Text: "Refuse every unsigned call.", Pro: "Safe."},
			{Title: "Log only", Text: "Let unsigned calls pass and log them."}}})
	if err != nil {
		t.Fatal(err)
	}
	if !d.NeedsHuman() {
		t.Fatalf("the decision does not wait for a person: %+v", d)
	}
	return req.Request.ID, d
}

// REQ-684 AC-1: every decision has a page, also one without an idea, with its variants and the way to
// vote and decide; the address a tool names for it is that page.
func TestEveryDecisionHasAPageAndTheToolsNameIt(t *testing.T) {
	e, pa := listEnv(t)
	reqID, d := issueDecision(t, e, pa)
	path := "/ui/x/shell/decisions/" + itoa(d.ID)
	if got := links.New(e.Base, shellProject).Decision(d.ID); got != e.Base+path {
		t.Fatalf("the address the tools name = %q, want %q", got, e.Base+path)
	}
	status, page := fetchPage(t, e.Owner, e.Base+path)
	if status != http.StatusOK {
		t.Fatalf("GET %s -> %d, the address the agent was given has no page", path, status)
	}
	for _, want := range []string{"Verify the signature strictly?", "DEC-" + itoa(d.ID), "Without it anyone can trigger the hook.", "Strictly", "Log only",
		`action="/ui/decisions/` + itoa(d.ID) + `/close"`, `formaction="/ui/decisions/` + itoa(d.ID) + `/vote"`, `href="/ui/x/shell/issues/` + itoa(reqID) + `"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// A person who may only read finds the variants and no form; one who may not read, no page.
	if _, guest := fetchPage(t, e.Guest, e.Base+path); strings.Contains(guest, `action="/ui/decisions/`) {
		t.Error("a guest is offered the forms")
	}
	// The issue names its decisions.
	_, issue := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues/"+itoa(reqID))
	if !strings.Contains(issue, "Verify the signature strictly?") || !strings.Contains(issue, `href="`+path+`"`) {
		t.Error("the issue does not point to its decision")
	}
	// Another project's address and an unknown number are as unknown as any page.
	for _, p := range []string{"/ui/x/other/decisions/" + itoa(d.ID), "/ui/x/shell/decisions/99999", "/ui/x/shell/decisions/abc"} {
		if code, _ := fetchPage(t, e.Owner, e.Base+p); code != http.StatusNotFound {
			t.Errorf("GET %s -> %d, want 404", p, code)
		}
	}
}

func TestDecisionOfAnIdeaLeadsToItsWorkshop(t *testing.T) {
	e, pa := listEnv(t)
	i, d := humanDecision(t, pa, shellProject)
	resp, err := e.Owner.Get(e.Base + "/ui/x/shell/decisions/" + itoa(d.ID))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if want := "/ui/x/shell/ideas/" + itoa(i.ID) + "#decision-" + itoa(d.ID); resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != want {
		t.Errorf("the address of a decision of an idea -> %d %q, want a redirect to %s", resp.StatusCode, resp.Header.Get("Location"), want)
	}
}

func TestDecisionOfAnIssueIsTakenOnItsPageAndLeavesTheInbox(t *testing.T) {
	e, pa := listEnv(t)
	_, d := issueDecision(t, e, pa)
	// Waiting for the owner: the inbox row leads to the page of the decision.
	_, inbox := fetchPage(t, e.Owner, e.Base+"/ui/inbox")
	if !strings.Contains(inbox, "Verify the signature strictly?") || !strings.Contains(inbox, `href="/ui/x/shell/decisions/`+itoa(d.ID)+`"`) {
		t.Fatalf("the inbox does not lead to the page of the decision:\n%s", inbox)
	}
	resp := tpPost(t, e, e.Owner, "/ui/decisions/"+itoa(d.ID)+"/close", url.Values{"variant": {"A"}, "reason": {"Safe first"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/ui/x/shell/decisions/"+itoa(d.ID)+"?wdone=decided") {
		t.Fatalf("deciding -> %d %q, want back to the page of the decision", resp.StatusCode, resp.Header.Get("Location"))
	}
	got, err := pa.DecisionView(d.ID)
	if err != nil || got.State != idea.DecisionDecided || got.ChosenVariant != "A" {
		t.Fatalf("the decision is %+v, %v", got, err)
	}
	if _, after := fetchPage(t, e.Owner, e.Base+"/ui/inbox"); strings.Contains(after, "Verify the signature strictly?") {
		t.Error("a decided question stays in Waiting for you")
	}
	// A member may vote on an open one, not decide it.
	_, d2 := issueDecision(t, e, pa)
	if resp := tpPost(t, e, e.Member, "/ui/decisions/"+itoa(d2.ID)+"/close", url.Values{"variant": {"A"}, "reason": {"x"}}); resp.StatusCode == http.StatusSeeOther && !strings.Contains(resp.Header.Get("Location"), "wfail") {
		t.Errorf("a member decided: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// REQ-684 AC-1: DEC-n is found by the search and the jump, and what waits for a person stands under
// "Waiting for me".
func TestDecisionIsFoundByNumberAndWaitingForMe(t *testing.T) {
	e, pa := listEnv(t)
	_, d := issueDecision(t, e, pa)
	want := `href="/ui/x/shell/decisions/` + itoa(d.ID) + `"`
	if _, hit := fetchPage(t, e.Owner, e.Base+"/ui/search?q=DEC-"+itoa(d.ID)); !strings.Contains(hit, want) || !strings.Contains(hit, "Verify the signature strictly?") {
		t.Errorf("the search for DEC-%d does not find it", d.ID)
	}
	if _, jump := fetchPage(t, e.Owner, e.Base+"/ui/search/suggest?q=DEC-"+itoa(d.ID)); !strings.Contains(jump, want) {
		t.Errorf("the jump for DEC-%d offers no target:\n%s", d.ID, jump)
	}
	_, mine := fetchPage(t, e.Owner, e.Base+"/ui/search?preset=fuer-mich")
	if !strings.Contains(mine, "Verify the signature strictly?") {
		t.Error("Waiting for me does not list the decision that waits for the owner")
	}
	// Somebody who may not close it is not the one it waits for.
	if _, other := fetchPage(t, e.Member, e.Base+"/ui/search?preset=fuer-mich"); strings.Contains(other, "Verify the signature strictly?") {
		t.Error("Waiting for me lists a decision for somebody who may not decide it")
	}
}
