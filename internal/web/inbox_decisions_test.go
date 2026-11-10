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

// humanDecision opens a decision on an idea of alice that waits for a person, with
// two variants, and fails the test when the rule decided otherwise.
func humanDecision(t *testing.T, pa *store.ProjectAccess, project string) (idea.Idea, idea.Decision) {
	t.Helper()
	i, d := workedIdea(t, pa, project)
	if !d.NeedsHuman() {
		t.Fatalf("the decision does not wait for a person: %+v", d)
	}
	return i, d
}

func TestInboxShowsAnOpenDecisionWithItsVariantsToThoseWhoMayCloseIt(t *testing.T) {
	e, pa := listEnv(t)
	i, d := humanDecision(t, pa, shellProject)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/inbox")
	for _, want := range []string{
		"DEC-" + itoa(d.ID), "Where does the speech turn into text?", "Privacy and cost differ.",
		`name="kind" value="decision"`, `name="id" value="` + itoa(d.ID) + `"`, `name="option" value="A"`, `name="option" value="B"`,
		">On the device</button>", ">On the server</button>", "With a reason", `name="option"`, "Pick a variant", "Decide", "Why? (optional)",
		`data-ref="DEC-` + itoa(d.ID) + `"`, `href="/ui/x/shell/ideas/` + itoa(i.ID) + `#decision-` + itoa(d.ID) + `"`,
		`data-ref-card="DEC-` + itoa(d.ID) + `"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("inbox lacks %q", want)
		}
	}
	// The question is a "question": the filter finds it, and the counter counts it.
	_, asked := fetchPage(t, e.Owner, e.Base+"/ui/inbox?f=fragen")
	if !strings.Contains(asked, "DEC-"+itoa(d.ID)) {
		t.Error("the filter Questions does not show the decision")
	}
	_, tasks := fetchPage(t, e.Owner, e.Base+"/ui/inbox?f=aufgaben")
	if strings.Contains(tasks, "DEC-"+itoa(d.ID)) {
		t.Error("the filter Tasks shows a decision")
	}
	// A guest may read nothing here and a plain member may not close it: no row.
	for name, c := range map[string]*http.Client{"guest": e.Guest, "member": e.Member} {
		if _, other := fetchPage(t, c, e.Base+"/ui/inbox"); strings.Contains(other, "Where does the speech turn into text?") {
			t.Errorf("%s sees a decision they may not close", name)
		}
	}
}

func TestInboxDecisionIsTakenWithAClickAndWithAReason(t *testing.T) {
	e, pa := listEnv(t)
	_, d := humanDecision(t, pa, shellProject)
	form := func(opt, reason string) url.Values {
		return url.Values{"kind": {"decision"}, "id": {itoa(d.ID)}, "option": {opt}, "reason": {reason}}
	}
	// No CSRF token: refused, nothing decided.
	if resp := sameOriginPostForm(t, e.Owner, e.Base+"/ui/inbox/answer", form("A", "")); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("without a csrf token: %d", resp.StatusCode)
	}
	// A member who may not close it finds nothing; a guest neither.
	for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest} {
		if resp := tpPost(t, e, c, "/ui/inbox/answer", form("A", "")); resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusSeeOther {
			t.Fatalf("%s decided: %d", name, resp.StatusCode)
		}
	}
	// A session from a pasted token is no person in the browser.
	personal, _, err := e.St.CreateToken("alice", store.TokenSpec{Label: "bearer"})
	if err != nil {
		t.Fatal(err)
	}
	if resp := tpPost(t, e, login(t, e.Srv, personal), "/ui/inbox/answer", form("A", "")); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a pasted token decided: %d", resp.StatusCode)
	}
	if got, _ := pa.DecisionView(d.ID); got.State != idea.DecisionOpen {
		t.Fatalf("a refused answer closed the decision: %+v", got)
	}
	// A variant that does not exist.
	if resp := tpPost(t, e, e.Owner, "/ui/inbox/answer", form("Z", "")); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("an unknown variant: %d", resp.StatusCode)
	}
	// The composer without script: no choice goes back to the inbox with the reason, and decides nothing.
	noChoice := form("", "weil")
	noChoice.Set("next", "1")
	resp := tpPost(t, e, asBrowser(e.Owner), "/ui/inbox/answer", noChoice)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/inbox" {
		t.Fatalf("no choice: %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if _, page := followRefusal(t, e.Owner, e.Base, resp); !strings.Contains(page, "Pick one of the variants first.") || !strings.Contains(page, `"weil"`) {
		t.Error("the inbox does not say why nothing was decided, or the reason typed is lost")
	}
	tooLong := form("A", strings.Repeat("x", idea.MaxReasonLine+1))
	tooLong.Set("next", "1")
	if resp := tpPost(t, e, asBrowser(e.Owner), "/ui/inbox/answer", tooLong); resp.Header.Get("Location") != "/ui/inbox" {
		t.Fatalf("a reason that is too long: %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got, _ := pa.DecisionView(d.ID); got.State != idea.DecisionOpen {
		t.Fatalf("a failed answer closed the decision: %+v", got)
	}
	// With a reason from the composer: decided, with the choice and the reason as written.
	ok := form("B", "  Same quality\nfor everybody ")
	ok.Set("next", "1")
	resp = tpPost(t, e, e.Owner, "/ui/inbox/answer", ok)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/inbox" {
		t.Fatalf("with a reason: %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	got, _ := pa.DecisionView(d.ID)
	if got.State != idea.DecisionDecided || got.ChosenVariant != "B" || got.Reason != "Same quality for everybody" || got.ByAgent || got.KnowledgeID == 0 {
		t.Fatalf("decision after the answer: %+v", got)
	}
	// It left what waits, and a second answer finds it closed.
	if _, page := fetchPage(t, e.Owner, e.Base+"/ui/inbox"); strings.Contains(page, "Where does the speech turn into text?") {
		t.Error("a decided question still waits")
	}
	if resp := tpPost(t, e, e.Owner, "/ui/inbox/answer", form("A", "")); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a second answer: %d", resp.StatusCode)
	}
	if _, page := fetchPage(t, e.Owner, e.Base+"/ui/inbox?done=1"); !strings.Contains(page, "Where does the speech turn into text?") {
		t.Error("the finished decision is not in the done entries")
	}
}

func TestInboxDecisionQuickClickKeepsTheStandardReason(t *testing.T) {
	e, pa := listEnv(t)
	_, d := humanDecision(t, pa, shellProject)
	resp := tpPost(t, e, e.Owner, "/ui/inbox/answer", url.Values{"kind": {"decision"}, "id": {itoa(d.ID)}, "option": {"A"}})
	if out := body(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(out, `"On the device"`) {
		t.Fatalf("quick answer: %d %s", resp.StatusCode, out)
	}
	if got, _ := pa.DecisionView(d.ID); got.State != idea.DecisionDecided || got.ChosenVariant != "A" || !strings.Contains(got.Reason, "per Schnellantwort") {
		t.Fatalf("decision after the click: %+v", got)
	}
}

func TestInboxDecisionWithoutVariantsHasNoButtonsAndLinksToItsPage(t *testing.T) {
	e, pa := listEnv(t)
	i, _ := workedIdea(t, pa, shellProject)
	d, err := pa.OpenDecision(store.OpenDecisionInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Question: "Who writes the first draft?", Aspects: []string{idea.AspectProduct}, Actor: agentActor})
	if err != nil {
		t.Fatal(err)
	}
	if !d.NeedsHuman() {
		t.Skipf("the rule left this decision to an agent: %+v", d)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/inbox")
	if !strings.Contains(page, "Who writes the first draft?") || strings.Contains(page, `name="id" value="`+itoa(d.ID)+`"><input type="hidden" name="option"`) {
		t.Errorf("a decision without variants must show the question and no buttons")
	}
	if !strings.Contains(page, `href="/ui/x/shell/ideas/`+strconv.FormatInt(i.ID, 10)+`#decision-`+itoa(d.ID)+`"`) {
		t.Error("the row does not lead to the decision")
	}
}

func TestDecisionHoverCardCarriesQuestionStateAndLink(t *testing.T) {
	e, pa := listEnv(t)
	i, d := humanDecision(t, pa, shellProject)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/inbox")
	start := strings.Index(page, `<template data-ref-card="DEC-`+itoa(d.ID)+`"`)
	if start < 0 {
		t.Fatal("the owner gets no card for the decision")
	}
	card := page[start : start+strings.Index(page[start:], "</template>")]
	for _, want := range []string{`data-href="/ui/x/shell/ideas/` + itoa(i.ID) + `#decision-` + itoa(d.ID) + `"`, "DEC-" + itoa(d.ID), "Where does the speech turn into text?", "Privacy and cost differ.", "Open"} {
		if !strings.Contains(card, want) {
			t.Errorf("the card lacks %q: %s", want, card)
		}
	}
	// The address of the card leads to a page that shows the decision.
	if _, idea := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/ideas/"+itoa(i.ID)); !strings.Contains(idea, `id="decision-`+itoa(d.ID)+`"`) {
		t.Error("the page the card leads to has no anchor for the decision")
	}
}
