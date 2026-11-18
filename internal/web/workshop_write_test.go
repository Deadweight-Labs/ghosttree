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

// REQ-709: a person works an idea out in the browser, without an agent: raw text, summary, concept,
// a question with variants, one more variant. Each form has the rules of the call an agent makes, the
// right of the role, the token against forgery, and writes as a person.

type wsPoster struct {
	t *testing.T
	e shellEnv
}

func (p wsPoster) post(c *http.Client, path string, form url.Values) *http.Response {
	p.t.Helper()
	form.Set("csrf_token", renderedCSRFToken(p.t, c, p.e.Base+"/ui/overview"))
	resp := sameOriginPostForm(p.t, c, p.e.Base+path, form)
	resp.Body.Close()
	return resp
}

func plainIdea(t *testing.T, pa *store.ProjectAccess, title string) idea.Idea {
	t.Helper()
	i, err := pa.CreateIdea(store.CreateIdeaInput{Project: shellProject, Title: title, Body: "A wish.", Actor: humanActor})
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestPersonWritesRawTextAndSummaryInTheBrowser(t *testing.T) {
	e, pa := listEnv(t)
	p := wsPoster{t, e}
	i := plainIdea(t, pa, "Dictate answers")
	path := "/ui/ideas/" + strconv.FormatInt(i.ID, 10) + "/text"
	// The form opens from the button and has the fields.
	_, page := fetchPage(t, e.Owner, e.Base+ideaPath(i)+"?panel=raw")
	for _, want := range []string{`id="ws-panel"`, `action="` + path + `"`, `name="part" value="raw"`, `name="body"`, `name="source"`, `name="csrf_token"`} {
		if !strings.Contains(page, want) {
			t.Errorf("raw panel lacks %q", want)
		}
	}
	if resp := p.post(e.Owner, path, url.Values{"part": {"raw"}, "body": {"Transcript of the call.\r\nSecond line."}, "source": {"Meeting 2026-11-18"}}); resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "wdone=text_raw") {
		t.Fatalf("raw -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp := p.post(e.Member, path, url.Values{"part": {"summary"}, "body": {"Answer by voice."}}); !strings.Contains(resp.Header.Get("Location"), "wdone=text_summary") {
		t.Fatalf("summary by a member -> %q", resp.Header.Get("Location"))
	}
	ws, err := pa.IdeaWorkshop(i.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := pa.IdeaText(i.ID, idea.PartRaw, 0)
	if err != nil || len(ws.Raw) != 1 || ws.Raw[0].ByAgent || ws.Raw[0].Source != "Meeting 2026-11-18" || !strings.Contains(raw.Body, "Second line.") || strings.Contains(raw.Body, "\r") {
		t.Errorf("raw text = %+v %+v (%v), want one version by a person with its source", ws.Raw, raw, err)
	}
	if ws.Summary == nil || ws.Summary.ByAgent || ws.Summary.Person == "" {
		t.Errorf("summary = %+v, want one by a person", ws.Summary)
	}
	_, done := fetchPage(t, e.Owner, e.Base+ideaPath(i)+"?wdone=text_raw")
	if !strings.Contains(done, "Raw text saved.") || !strings.Contains(done, "Answer by voice.") {
		t.Error("the saved texts are not shown")
	}
	// Empty and invalid text, a part that does not exist.
	if resp := p.post(e.Owner, path, url.Values{"part": {"raw"}, "body": {"  "}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("empty raw text -> %d, want 422", resp.StatusCode)
	}
	if resp := p.post(e.Owner, path, url.Values{"part": {"concept"}, "body": {"x"}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown part -> %d, want 400", resp.StatusCode)
	}
	// A guest, a pasted token and a request without the token write nothing.
	if resp := p.post(e.Guest, path, url.Values{"part": {"summary"}, "body": {"Guest wrote this."}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("guest -> %d, want 403", resp.StatusCode)
	}
	resp := sameOriginPostForm(t, e.Owner, e.Base+path, url.Values{"part": {"summary"}, "body": {"No token."}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("without csrf token -> %d, want 403", resp.StatusCode)
	}
	if ws, _ = pa.IdeaWorkshop(i.ID); ws.Summary == nil || ws.Summary.Body != "Answer by voice." {
		t.Errorf("a refused form changed the summary: %+v", ws.Summary)
	}
}

func TestPersonWritesAndEditsTheConceptSectionBySection(t *testing.T) {
	e, pa := listEnv(t)
	p := wsPoster{t, e}
	i := plainIdea(t, pa, "Voice")
	path := "/ui/ideas/" + strconv.FormatInt(i.ID, 10) + "/concept"
	// Without a concept: the step after the raw idea is to write it, as a link.
	_, page := fetchPage(t, e.Owner, e.Base+ideaPath(i))
	if !strings.Contains(page, `<a href="`+ideaPath(i)+`?panel=concept#ws-panel">Write the concept</a>`) {
		t.Error("the next step of a raw idea is not the link that opens the concept form")
	}
	_, page = fetchPage(t, e.Owner, e.Base+ideaPath(i)+"?panel=concept")
	for _, want := range []string{`action="` + path + `"`, `name="base" value="0"`, `name="about"`, `name="what"`, `name="how"`, `name="not"`, `name="questions"`, `name="requirements"`, "Requirements, one per line", "Save as a new version"} {
		if !strings.Contains(page, want) {
			t.Errorf("concept form lacks %q", want)
		}
	}
	form := url.Values{"base": {"0"}, "about": {"Answers by voice."}, "what": {"A microphone button."}, "how": {""}, "not": {"No wake word."}, "questions": {"Where does it run?"},
		"requirements": {"A spoken answer shows as text\r\n\r\n The microphone asks first \r\n"}, "message": {"first draft"}}
	if resp := p.post(e.Owner, path, form); resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "wdone=concept_saved") {
		t.Fatalf("concept -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	c, ok, err := pa.IdeaConcept(i.ID, 0)
	if err != nil || !ok {
		t.Fatalf("no concept: %v %v", ok, err)
	}
	if c.Revision != 1 || c.ByAgent || c.Concept.About != "Answers by voice." || c.Concept.Not != "No wake word." || len(c.Concept.Requirements) != 2 || c.Concept.Requirements[1] != "The microphone asks first" || c.Message != "first draft" {
		t.Errorf("concept = %+v", c)
	}
	// The form of an existing concept holds it, and the version it was read at.
	_, page = fetchPage(t, e.Owner, e.Base+ideaPath(i)+"?panel=concept")
	for _, want := range []string{`name="base" value="1"`, "A microphone button.", "A spoken answer shows as text\nThe microphone asks first", "Edit the concept"} {
		if !strings.Contains(page, want) {
			t.Errorf("edit form lacks %q", want)
		}
	}
	// A section can change on its own: the others stay (they come back in the form).
	form.Set("base", "1")
	form.Set("how", "In the reply field.")
	if resp := p.post(e.Member, path, form); !strings.Contains(resp.Header.Get("Location"), "wdone=concept_saved") {
		t.Fatalf("edit by a member -> %q", resp.Header.Get("Location"))
	}
	// The same text again is no new version; a stale base is refused with the text kept.
	form.Set("base", "2")
	if resp := p.post(e.Owner, path, form); !strings.Contains(resp.Header.Get("Location"), "wdone=concept_same") {
		t.Errorf("same text -> %q", resp.Header.Get("Location"))
	}
	form.Set("base", "1")
	form.Set("what", "Another idea.")
	if resp := p.post(e.Owner, path, form); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("stale base -> %d, want 422", resp.StatusCode)
	}
	if c, _, _ := pa.IdeaConcept(i.ID, 0); c.Revision != 2 || c.Concept.What != "A microphone button." {
		t.Errorf("a stale write got through: %+v", c)
	}
	if resp := p.post(e.Owner, path, url.Values{"base": {"x"}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("base is no number -> %d, want 400", resp.StatusCode)
	}
	if resp := p.post(e.Guest, path, form); resp.StatusCode != http.StatusForbidden {
		t.Errorf("guest -> %d, want 403", resp.StatusCode)
	}
	resp := sameOriginPostForm(t, e.Owner, e.Base+path, form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("without csrf token -> %d, want 403", resp.StatusCode)
	}
	// Too many requirements: refused with the rule.
	many := make([]string, idea.MaxRequirements+1)
	for n := range many {
		many[n] = "Requirement " + strconv.Itoa(n)
	}
	if resp := p.post(e.Owner, path, url.Values{"base": {"2"}, "requirements": {strings.Join(many, "\n")}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("too many requirements -> %d, want 422", resp.StatusCode)
	}
}

func TestPersonOpensADecisionWithVariantsForAndAgainstAndAddsOne(t *testing.T) {
	e, pa := listEnv(t)
	p := wsPoster{t, e}
	i := plainIdea(t, pa, "Where does speech turn into text")
	path := "/ui/ideas/" + strconv.FormatInt(i.ID, 10) + "/decisions"
	_, page := fetchPage(t, e.Owner, e.Base+ideaPath(i)+"?panel=decision")
	for _, want := range []string{`action="` + path + `"`, `name="question"`, `name="detail"`, `name="v1_title"`, `name="v1_pro"`, `name="v1_contra"`, `name="v2_title"`, `name="v4_contra"`, "Variant A", "Variant B", "More variants", "Open the decision"} {
		if !strings.Contains(page, want) {
			t.Errorf("decision form lacks %q", want)
		}
	}
	// A single variant is no choice; a question is needed; a variant needs a title.
	if resp := p.post(e.Owner, path, url.Values{"question": {"Where?"}, "v1_title": {"Device"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("one variant -> %d, want 422", resp.StatusCode)
	}
	if resp := p.post(e.Owner, path, url.Values{"question": {" "}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("no question -> %d, want 422", resp.StatusCode)
	}
	if resp := p.post(e.Owner, path, url.Values{"question": {"Where?"}, "v1_title": {"Device"}, "v2_pro": {"cheap"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a variant without a title -> %d, want 422", resp.StatusCode)
	}
	form := url.Values{"question": {"Where does the speech turn into text?"}, "detail": {"Privacy and cost differ."},
		"v1_title": {"On the device"}, "v1_text": {"The browser does it."}, "v1_pro": {"Nothing leaves the machine."}, "v1_contra": {"Quality depends on the browser."},
		"v2_title": {"On the server"}, "v2_pro": {"Same quality everywhere."}, "v2_contra": {"Costs capacity."}}
	resp := p.post(e.Member, path, form)
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "wdone=asked") {
		t.Fatalf("open -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	ds, err := pa.DecisionsOf(idea.SubjectIdea, i.ID)
	if err != nil || len(ds) != 1 {
		t.Fatalf("decisions = %v %v", ds, err)
	}
	d := ds[0]
	if d.Way != idea.WayHuman || d.OpenedByAgent || len(d.Variants) != 2 || d.Variants[0].Pro != "Nothing leaves the machine." || d.Variants[0].Contra != "Quality depends on the browser." || d.Variants[1].Label != "B" || d.Detail != "Privacy and cost differ." {
		t.Errorf("decision = %+v", d)
	}
	_, shown := fetchPage(t, e.Owner, e.Base+strings.SplitN(resp.Header.Get("Location"), "#", 2)[0])
	for _, want := range []string{"Decision opened.", "Where does the speech turn into text?", "Nothing leaves the machine.", "Costs capacity.", "Add a variant", `action="/ui/decisions/` + strconv.FormatInt(d.ID, 10) + `/variant"`} {
		if !strings.Contains(shown, want) {
			t.Errorf("the new decision on the page lacks %q", want)
		}
	}
	// One more variant for the open question; a guest cannot.
	vpath := "/ui/decisions/" + strconv.FormatInt(d.ID, 10) + "/variant"
	if resp := p.post(e.Guest, vpath, url.Values{"title": {"By hand"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("guest adds a variant -> %d, want 403", resp.StatusCode)
	}
	if resp := p.post(e.Owner, vpath, url.Values{"title": {" "}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("variant without title -> %d, want 422", resp.StatusCode)
	}
	if resp := p.post(e.Owner, vpath, url.Values{"title": {"Hybrid"}, "text": {"Both."}, "pro": {"Flexible."}, "contra": {"Two paths."}}); !strings.Contains(resp.Header.Get("Location"), "wdone=variant") {
		t.Fatalf("add variant -> %q", resp.Header.Get("Location"))
	}
	if got, _ := pa.DecisionView(d.ID); len(got.Variants) != 3 || got.Variants[2].Label != "C" || got.Variants[2].Pro != "Flexible." {
		t.Errorf("variants after adding = %+v", got.Variants)
	}
	// The way through: vote, decide; a closed question takes no variant.
	if resp := p.post(e.Member, "/ui/decisions/"+strconv.FormatInt(d.ID, 10)+"/vote", url.Values{"variant": {"C"}, "reason": {"both"}}); !strings.Contains(resp.Header.Get("Location"), "wdone=vote") {
		t.Fatalf("vote -> %q", resp.Header.Get("Location"))
	}
	if resp := p.post(e.Owner, "/ui/decisions/"+strconv.FormatInt(d.ID, 10)+"/close", url.Values{"variant": {"C"}, "reason": {"Flexible"}}); !strings.Contains(resp.Header.Get("Location"), "wdone=decided") {
		t.Fatalf("decide -> %q", resp.Header.Get("Location"))
	}
	if resp := p.post(e.Owner, vpath, url.Values{"title": {"Late"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("variant for a closed question -> %d, want 422", resp.StatusCode)
	}
	// No token, no change.
	resp = sameOriginPostForm(t, e.Owner, e.Base+path, form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("without csrf token -> %d, want 403", resp.StatusCode)
	}
}

// S11: the way of an idea has one step per phase, as a button; closing with a question open and approving a
// concept without requirements ask first.
func TestWorkshopOffersOneStepPerPhaseAndAsksBeforeTheOnesWithConsequences(t *testing.T) {
	e, pa := listEnv(t)
	p := wsPoster{t, e}
	i := plainIdea(t, pa, "One step at a time")
	phase := "/ui/ideas/" + strconv.FormatInt(i.ID, 10) + "/phase"
	if _, _, err := pa.PutIdeaConcept(i.ID, store.PutIdeaConceptInput{Actor: humanActor, Patch: idea.ConceptPatch{About: nStr("About.")}}); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Owner, e.Base+ideaPath(i))
	for _, want := range []string{`class="ws-next-form"`, `name="mode" value="discuss"`} {
		if !strings.Contains(page, want) {
			t.Errorf("concept phase lacks %q", want)
		}
	}
	if strings.Count(page, `name="mode" value="discuss"`) != 1 || strings.Contains(page, `name="mode" value="close"`) {
		t.Error("the concept phase offers the step twice or closes a discussion that never started")
	}
	p.post(e.Owner, phase, url.Values{"mode": {"discuss"}})
	_, page = fetchPage(t, e.Owner, e.Base+ideaPath(i))
	if !strings.Contains(page, `name="mode" value="close"`) || strings.Contains(page, `name="mode" value="approve"`) || strings.Contains(page, `name="mode" value="discuss"`) {
		t.Error("the discussion phase does not offer exactly closing")
	}
	// A question is open: closing asks, and the answer is needed.
	if _, err := pa.OpenDecision(store.OpenDecisionInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Question: "Which one?", Way: idea.WayHuman, Actor: humanActor}); err != nil {
		t.Fatal(err)
	}
	resp := p.post(e.Owner, phase, url.Values{"mode": {"close"}})
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "wask=close") || !strings.HasSuffix(loc, "#ws-ask") {
		t.Fatalf("close with an open question -> %q", loc)
	}
	_, asked := fetchPage(t, e.Owner, e.Base+strings.SplitN(loc, "#", 2)[0])
	if !strings.Contains(asked, `id="ws-ask"`) || !strings.Contains(asked, "1 decisions are still open") || !strings.Contains(asked, `name="confirm" value="1"`) {
		t.Error("the question before closing is not on the page")
	}
	if got, _ := pa.IdeaView(i.ID); got.Phase != idea.PhaseDiscussion {
		t.Fatalf("the unconfirmed close went through: %s", got.Phase)
	}
	p.post(e.Owner, phase, url.Values{"mode": {"close"}, "confirm": {"1"}})
	if got, _ := pa.IdeaView(i.ID); got.Phase != idea.PhaseDiscussed {
		t.Fatalf("the confirmed close did not go through: %s", got.Phase)
	}
	// Approving a concept without requirements asks, too, and keeps the version the person saw.
	_, page = fetchPage(t, e.Owner, e.Base+ideaPath(i))
	if !strings.Contains(page, `name="mode" value="approve"`) || !strings.Contains(page, `name="mode" value="reopen_discussion"`) || strings.Contains(page, `name="mode" value="close"`) {
		t.Error("the closed discussion does not offer approving and reopening")
	}
	resp = p.post(e.Owner, phase, url.Values{"mode": {"approve"}, "revision": {"1"}})
	loc = resp.Header.Get("Location")
	if !strings.Contains(loc, "wask=approve") || !strings.Contains(loc, "wrev=1") {
		t.Fatalf("approve without requirements -> %q", loc)
	}
	_, asked = fetchPage(t, e.Owner, e.Base+strings.SplitN(loc, "#", 2)[0])
	if !strings.Contains(asked, "has no requirements") || !strings.Contains(asked, `name="revision" value="1"`) {
		t.Error("the question before approving is not on the page")
	}
	if got, _ := pa.IdeaView(i.ID); got.Phase != idea.PhaseDiscussed {
		t.Fatalf("approved without asking: %s", got.Phase)
	}
	p.post(e.Owner, phase, url.Values{"mode": {"approve"}, "revision": {"1"}, "confirm": {"1"}})
	if got, _ := pa.IdeaView(i.ID); got.Phase != idea.PhaseApproved {
		t.Fatalf("confirmed approval: %s", got.Phase)
	}
	// Once approved and with no question open, the next step is the issue.
	ds, _ := pa.DecisionsOf(idea.SubjectIdea, i.ID)
	if _, err := pa.CloseDecision(store.CloseDecisionInput{ID: ds[0].ID, Outcome: idea.DecisionDropped, Reason: "Not needed.", Actor: humanActor}); err != nil {
		t.Fatal(err)
	}
	_, page = fetchPage(t, e.Owner, e.Base+ideaPath(i))
	if !strings.Contains(page, `<a href="/ui/x/shell/issues/new?derived_from=IDEA-`+strconv.FormatInt(i.ID, 10)+`">Make an issue of it</a>`) {
		t.Error("an approved idea does not lead on to the issue")
	}
	// A member may start a discussion but neither close nor approve; the asks do not bypass that.
	if resp := p.post(e.Member, phase, url.Values{"mode": {"withdraw"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("member withdraws -> %d, want 403", resp.StatusCode)
	}
}

// P25 and N14: what an agent did is shown as the agent's, and a question an agent may decide says so without
// shutting out the person.
func TestWorkshopNamesTheAgentWhereAnAgentActed(t *testing.T) {
	e, pa := listEnv(t)
	i := plainIdea(t, pa, "Who did it")
	d, err := pa.OpenDecision(store.OpenDecisionInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Question: "Which colour?", Actor: agentActor,
		Variants: []idea.VariantInput{{Title: "Blue"}, {Title: "Green"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !d.OpenedByAgent || d.Way != idea.WayAgent {
		t.Fatalf("decision = opened by agent %v, way %s", d.OpenedByAgent, d.Way)
	}
	_, page := fetchPage(t, e.Owner, e.Base+ideaPath(i))
	if strings.Contains(page, "The agent decides") || !strings.Contains(page, "An agent may decide") {
		t.Error("the way of a question the rule leaves to an agent is not told right")
	}
	asked := page[strings.Index(page, `asked-decision-`+strconv.FormatInt(d.ID, 10)):]
	asked = asked[:strings.Index(asked, "</div>")+200]
	if strings.Contains(asked, ">claude-mainex<") || !strings.Contains(asked, "claude-mainex’s") {
		t.Errorf("the question an agent asked stands under a person's name: %s", asked)
	}
	// The agent decides: the card and the timeline say the agent did.
	if _, err := pa.CloseDecision(store.CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDecided, Variant: "A", Reason: "Fits.", Actor: agentActor}); err != nil {
		t.Fatal(err)
	}
	_, page = fetchPage(t, e.Owner, e.Base+ideaPath(i))
	card := page[strings.Index(page, `id="decision-`+strconv.FormatInt(d.ID, 10)+`"`):]
	card = card[:strings.Index(card, "</article>")]
	if !strings.Contains(card, "claude-mainex’s agent") {
		t.Errorf("the card does not say that the agent decided: %s", card)
	}
	decided := page[strings.Index(page, `decided-`+strconv.FormatInt(d.ID, 10)):]
	decided = decided[:strings.Index(decided, "</div>")+200]
	if !strings.Contains(decided, "claude-mainex’s") {
		t.Errorf("the timeline names the person for what the agent decided: %s", decided)
	}
}
