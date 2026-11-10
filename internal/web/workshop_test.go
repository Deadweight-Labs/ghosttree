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

var (
	agentActor = idea.Actor{AccountID: 1, Person: "claude-mainex", Agent: true}
	humanActor = idea.Actor{AccountID: 1, Person: "alice"}
)

// workedIdea ist eine Idee, an der ein Agent eine Kurzfassung, ein Konzept und eine
// Frage mit zwei Varianten angelegt hat; die Frage wartet auf einen Menschen.
func workedIdea(t *testing.T, pa *store.ProjectAccess, project string) (idea.Idea, idea.Decision) {
	t.Helper()
	i, err := pa.CreateIdea(store.CreateIdeaInput{Project: project, Title: "Answer the inbox by voice", Body: "Talk instead of typing.", Actor: humanActor})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := pa.PutIdeaText(i.ID, store.PutIdeaTextInput{Part: idea.PartRaw, Body: "Long transcript of the call about voice.", Source: "Meeting 2026-11-08", Actor: agentActor}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pa.PutIdeaText(i.ID, store.PutIdeaTextInput{Part: idea.PartSummary, Body: "Answer questions by speaking; the text lands in the thread.", Actor: agentActor}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pa.PutIdeaConcept(i.ID, store.PutIdeaConceptInput{Actor: agentActor, Patch: idea.ConceptPatch{
		About: nStr("Voice answers in the inbox."), What: nStr("A microphone button next to the reply field."), Not: nStr("No wake word."),
		Requirements: &[]string{"A spoken answer shows up as text in the thread", "The microphone asks before it listens"}}}); err != nil {
		t.Fatal(err)
	}
	d, err := pa.OpenDecision(store.OpenDecisionInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Question: "Where does the speech turn into text?", Detail: "Privacy and cost differ.",
		Aspects: []string{idea.AspectProduct}, Actor: agentActor, Variants: []idea.VariantInput{
			{Title: "On the device", Text: "The browser does it.", Pro: "Nothing leaves the machine.", Contra: "Quality depends on the browser."},
			{Title: "On the server", Text: "A model on our server does it.", Pro: "The same quality everywhere.", Contra: "Costs capacity."}}})
	if err != nil {
		t.Fatal(err)
	}
	i, _ = pa.IdeaView(i.ID)
	return i, d
}

func ideaPath(i idea.Idea) string { return "/ui/x/shell/ideas/" + strconv.FormatInt(i.ID, 10) }

func TestIdeaWithoutWorkshopShowsNoWorkshop(t *testing.T) {
	e, pa := listEnv(t)
	i, err := pa.CreateIdea(store.CreateIdeaInput{Project: shellProject, Title: "Plain wish", Actor: humanActor})
	if err != nil {
		t.Fatal(err)
	}
	if _, page := fetchPage(t, e.Owner, e.Base+ideaPath(i)); strings.Contains(page, `id="workshop"`) {
		t.Error("an idea nobody worked out shows a workshop")
	}
}

func TestWorkshopShowsTheConceptAndTheVariantsSideBySideWithTheRightFormsPerRole(t *testing.T) {
	e, pa := listEnv(t)
	i, d := workedIdea(t, pa, shellProject)
	code, owner := fetchPage(t, e.Owner, e.Base+ideaPath(i))
	if code != http.StatusOK {
		t.Fatalf("idea page = %d", code)
	}
	for _, want := range []string{`id="workshop"`, "Workshop", "Concept", "Version 1", "Voice answers in the inbox.", "A microphone button next to the reply field.", "No wake word.", "Requirements",
		"A spoken answer shows up as text in the thread", "Summary", "Answer questions by speaking", "Raw version, 1 in all", "Meeting 2026-11-08",
		"DEC-" + strconv.FormatInt(d.ID, 10), "Where does the speech turn into text?", "Privacy and cost differ.", "A person decides", "1 open", "On the device", "On the server", "Nothing leaves the machine.", "Quality depends on the browser.", "For", "Against",
		`class="ws-vars"`, `type="radio" name="variant" value="A"`, `type="radio" name="variant" value="B"`, `action="/ui/decisions/` + strconv.FormatInt(d.ID, 10) + `/close"`,
		`formaction="/ui/decisions/` + strconv.FormatInt(d.ID, 10) + `/vote"`, "Decide", "Vote", "Start the discussion", `action="/ui/ideas/` + strconv.FormatInt(i.ID, 10) + `/phase"`, "Approve version 1", "Raw idea", "In discussion"} {
		if !strings.Contains(owner, want) {
			t.Errorf("owner's workshop lacks %q", want)
		}
	}
	// A member may vote but not decide; nobody who may not work gets a form at all.
	_, member := fetchPage(t, e.Member, e.Base+ideaPath(i))
	if !strings.Contains(member, `formaction="/ui/decisions/`+strconv.FormatInt(d.ID, 10)+`/vote"`) || strings.Contains(member, ">Decide<") || strings.Contains(member, "Approve version") {
		t.Error("a member does not get exactly the vote")
	}
	_, guest := fetchPage(t, e.Guest, e.Base+ideaPath(i))
	for _, leak := range []string{`/ui/decisions/`, `/phase"`, `type="radio"`} {
		if strings.Contains(guest, leak) {
			t.Errorf("a guest gets %q", leak)
		}
	}
	if !strings.Contains(guest, "On the device") || !strings.Contains(guest, "On the server") {
		t.Error("a guest cannot compare the variants")
	}
	if code, _ := fetchPage(t, e.Member, e.Base+"/ui/x/hidden/ideas/"+strconv.FormatInt(i.ID, 10)); code != http.StatusNotFound {
		t.Errorf("idea under a foreign project = %d", code)
	}
}

func TestMemberVotesAndTheOwnerDecidesAndBothShowInTheTimeline(t *testing.T) {
	e, pa := listEnv(t)
	i, d := workedIdea(t, pa, shellProject)
	post := func(c *http.Client, path string, form url.Values) *http.Response {
		form.Set("csrf_token", renderedCSRFToken(t, c, e.Base+"/ui/overview"))
		resp := sameOriginPostForm(t, c, e.Base+path, form)
		resp.Body.Close()
		return resp
	}
	id := strconv.FormatInt(d.ID, 10)
	if resp := post(e.Member, "/ui/decisions/"+id+"/vote", url.Values{"variant": {"A"}, "reason": {"Nothing leaves the machine"}}); resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "wdone=vote") {
		t.Fatalf("vote -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, page := fetchPage(t, e.Owner, e.Base+ideaPath(i)+"?wdone=vote")
	for _, want := range []string{"Vote saved.", "1 for it", "would take A for", "Nothing leaves the machine", `checked`} {
		if want == "checked" {
			continue
		}
		if !strings.Contains(page, want) {
			t.Errorf("after the vote the page lacks %q", want)
		}
	}
	// The vote of the member is preselected for the member, and only for them.
	if _, mine := fetchPage(t, e.Member, e.Base+ideaPath(i)); !strings.Contains(mine, `value="A" checked`) {
		t.Error("the member's own vote is not preselected")
	}
	// A member may not close it.
	if resp := post(e.Member, "/ui/decisions/"+id+"/close", url.Values{"variant": {"B"}, "reason": {"I say so"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a member closes the question -> %d, want 403", resp.StatusCode)
	}
	// The owner needs a reason and a choice.
	if resp := post(e.Owner, "/ui/decisions/"+id+"/close", url.Values{"variant": {"B"}, "reason": {" "}}); !strings.Contains(resp.Header.Get("Location"), "wfail=reason_required") {
		t.Errorf("no reason -> %q", resp.Header.Get("Location"))
	}
	if _, failed := fetchPage(t, e.Owner, e.Base+ideaPath(i)+"?wfail=reason_required"); !strings.Contains(failed, "Say why.") {
		t.Error("the refusal is not shown")
	}
	if resp := post(e.Owner, "/ui/decisions/"+id+"/close", url.Values{"reason": {"Because"}}); !strings.Contains(resp.Header.Get("Location"), "wfail=variant_required") && !strings.Contains(resp.Header.Get("Location"), "wfail=answer_required") {
		t.Errorf("no choice -> %q", resp.Header.Get("Location"))
	}
	if resp := post(e.Owner, "/ui/decisions/"+id+"/close", url.Values{"variant": {"B"}, "reason": {"Same quality everywhere"}}); resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "wdone=decided") {
		t.Fatalf("decide -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, after := fetchPage(t, e.Owner, e.Base+ideaPath(i)+"?wdone=decided")
	for _, want := range []string{"Decision saved.", "Decided", "Chosen", "Same quality everywhere", "alice", "decided", `class="ws-var chosen"`, "Open again"} {
		if !strings.Contains(after, want) {
			t.Errorf("after deciding the page lacks %q", want)
		}
	}
	if strings.Contains(after, `type="radio" name="variant"`) {
		t.Error("a closed question still offers a choice")
	}
	// Reopening needs a reason; then the question is open and shows the choice again.
	if resp := post(e.Owner, "/ui/decisions/"+id+"/reopen", url.Values{"reason": {"New facts"}}); !strings.Contains(resp.Header.Get("Location"), "wdone=reopen") {
		t.Errorf("reopen -> %q", resp.Header.Get("Location"))
	}
	if _, again := fetchPage(t, e.Owner, e.Base+ideaPath(i)); !strings.Contains(again, `type="radio"`) {
		t.Error("the reopened question does not offer a choice")
	}
}

func TestWorkshopStepsRefuseWhoMayNotAndWhoCannotSeeTheIdea(t *testing.T) {
	e, pa := listEnv(t)
	i, d := workedIdea(t, pa, shellProject)
	hidden, hd := workedIdea(t, pa, shellHiddenProject)
	post := func(c *http.Client, path string, form url.Values) *http.Response {
		form.Set("csrf_token", renderedCSRFToken(t, c, e.Base+"/ui/overview"))
		resp := sameOriginPostForm(t, c, e.Base+path, form)
		resp.Body.Close()
		return resp
	}
	did := strconv.FormatInt(d.ID, 10)
	// A guest of the project reads but takes no step.
	for _, p := range []string{"/ui/decisions/" + did + "/vote", "/ui/decisions/" + did + "/close", "/ui/decisions/" + did + "/reopen"} {
		if resp := post(e.Guest, p, url.Values{"variant": {"A"}, "reason": {"x"}}); resp.StatusCode != http.StatusForbidden {
			t.Errorf("guest %s -> %d, want 403", p, resp.StatusCode)
		}
	}
	if resp := post(e.Guest, "/ui/ideas/"+strconv.FormatInt(i.ID, 10)+"/phase", url.Values{"mode": {"discuss"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("guest starts the discussion -> %d", resp.StatusCode)
	}
	// Whoever cannot read the idea gets the answer of a number that is none.
	hid := strconv.FormatInt(hd.ID, 10)
	for _, p := range []string{"/ui/decisions/" + hid + "/vote", "/ui/decisions/" + hid + "/close", "/ui/decisions/" + hid + "/reopen"} {
		if resp := post(e.Member, p, url.Values{"variant": {"A"}, "reason": {"x"}}); resp.StatusCode != http.StatusNotFound {
			t.Errorf("member %s on a hidden idea -> %d, want 404", p, resp.StatusCode)
		}
	}
	if resp := post(e.Member, "/ui/ideas/"+strconv.FormatInt(hidden.ID, 10)+"/phase", url.Values{"mode": {"discuss"}}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("member steps on a hidden idea -> %d, want 404", resp.StatusCode)
	}
	if resp := post(e.Member, "/ui/decisions/99999/vote", url.Values{"variant": {"A"}, "reason": {"x"}}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown decision -> %d", resp.StatusCode)
	}
	if resp := post(e.Owner, "/ui/ideas/"+strconv.FormatInt(i.ID, 10)+"/phase", url.Values{"mode": {"fly"}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown mode -> %d", resp.StatusCode)
	}
	// A token alone is no person: the forms are shut for a pasted token.
	token, _, err := e.St.CreateDeviceToken("person:1", "mainex")
	if err != nil {
		t.Fatal(err)
	}
	pasted := login(t, e.Srv, token)
	resp := sameOriginPostForm(t, pasted, e.Base+"/ui/decisions/"+did+"/close", url.Values{"variant": {"A"}, "reason": {"x"}, "csrf_token": {renderedCSRFToken(t, pasted, e.Base+"/ui/overview")}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("pasted token decides -> %d, want 403", resp.StatusCode)
	}
	if got, _ := pa.DecisionView(d.ID); got.State != idea.DecisionOpen {
		t.Errorf("the refused steps closed the question: %s", got.State)
	}
	if _, page := fetchPage(t, pasted, e.Base+ideaPath(i)); strings.Contains(page, `/ui/decisions/`) {
		t.Error("a pasted token sees the forms")
	}
}

func TestOwnerStartsTheDiscussionApprovesTheConceptAndTakesItBack(t *testing.T) {
	e, pa := listEnv(t)
	i, _ := workedIdea(t, pa, shellProject)
	path := "/ui/ideas/" + strconv.FormatInt(i.ID, 10) + "/phase"
	post := func(c *http.Client, form url.Values) *http.Response {
		form.Set("csrf_token", renderedCSRFToken(t, c, e.Base+"/ui/overview"))
		resp := sameOriginPostForm(t, c, e.Base+path, form)
		resp.Body.Close()
		return resp
	}
	if resp := post(e.Member, url.Values{"mode": {"discuss"}}); resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "wdone=discuss") {
		t.Fatalf("discuss -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got, _ := pa.IdeaView(i.ID); got.Phase != idea.PhaseDiscussion {
		t.Fatalf("phase = %s", got.Phase)
	}
	// A member may not approve; the owner must name the version they read.
	if resp := post(e.Member, url.Values{"mode": {"approve"}, "revision": {"1"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("member approves -> %d, want 403", resp.StatusCode)
	}
	if resp := post(e.Owner, url.Values{"mode": {"approve"}, "revision": {"zero"}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("approve without a number -> %d", resp.StatusCode)
	}
	if _, _, err := pa.PutIdeaConcept(i.ID, store.PutIdeaConceptInput{Actor: agentActor, Patch: idea.ConceptPatch{How: nStr("Through the browser API.")}}); err != nil {
		t.Fatal(err)
	}
	if resp := post(e.Owner, url.Values{"mode": {"approve"}, "revision": {"1"}}); !strings.Contains(resp.Header.Get("Location"), "wfail=concept_changed") {
		t.Errorf("approving a version that changed -> %q", resp.Header.Get("Location"))
	}
	if _, failed := fetchPage(t, e.Owner, e.Base+ideaPath(i)+"?wfail=concept_changed"); !strings.Contains(failed, "The concept changed while you were reading.") {
		t.Error("the stale approval is not told")
	}
	_, page := fetchPage(t, e.Owner, e.Base+ideaPath(i))
	if !strings.Contains(page, `name="revision" value="2"`) || !strings.Contains(page, "Approve version 2") {
		t.Error("the form does not offer the newest version")
	}
	if resp := post(e.Owner, url.Values{"mode": {"approve"}, "revision": {"2"}}); !strings.Contains(resp.Header.Get("Location"), "wdone=approve") {
		t.Fatalf("approve -> %q", resp.Header.Get("Location"))
	}
	if got, _ := pa.IdeaView(i.ID); got.Phase != idea.PhaseApproved {
		t.Fatalf("phase = %s", got.Phase)
	}
	_, approved := fetchPage(t, e.Owner, e.Base+ideaPath(i)+"?wdone=approve")
	for _, want := range []string{"Concept approved.", "Approved", "Take the approval back"} {
		if !strings.Contains(approved, want) {
			t.Errorf("after approving the page lacks %q", want)
		}
	}
	if resp := post(e.Owner, url.Values{"mode": {"withdraw"}}); !strings.Contains(resp.Header.Get("Location"), "wdone=withdraw") {
		t.Errorf("withdraw -> %q", resp.Header.Get("Location"))
	}
	if got, _ := pa.IdeaView(i.ID); got.Phase != idea.PhaseDiscussion {
		t.Errorf("phase after withdrawing = %s", got.Phase)
	}
}
