package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	actordomain "github.com/Deadweight-Labs/ghosttree/internal/actor"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The page of a session opens as the ghosttree view (what the session left
// behind, as a timeline with the agent as its maker); the raw log is the second
// tab, and every link that points into the log opens it.
func TestTheSessionPageOpensAsTheGhosttreeViewAndTheRawLogIsItsOwnTab(t *testing.T) {
	e := seedSessions(t)
	sid := e.id["alice-private"]
	d, err := e.St.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Anchor the entries", Scope: scope.Axes{Project: shellProject}, Person: "alice"}, Criteria: []string{"Entries carry a commit"}})
	if err != nil {
		t.Fatal(err)
	}
	cid := d.Criteria[0].ID
	if err := e.St.SetCriterionState(cid, "met", requestdomain.Evidence{Kind: "commit", Ref: "abcdef1234567890", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := e.St.RecordActor(actordomain.Criterion, cid, actordomain.Actor{Kind: actordomain.Agent, Harness: "claude-code", SessionID: sid, AccountID: 1}); err != nil {
		t.Fatal(err)
	}
	path := "/ui/sessions/" + e.pid["alice-private"]
	code, page := e.get(t, e.Owner, path)
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	for _, want := range []string{"ghosttree view", "started the session", "Requirement 1", "Entries carry a commit", "abcdef1", "1 requirement", `href="` + path + `?view=raw"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the ghosttree view lacks %q", want)
		}
	}
	if strings.Contains(page, `class="flow"`) {
		t.Error("the ghosttree view carries the raw log")
	}
	for _, rawPath := range []string{path + "?view=raw", path + "?q=zebrafish", path + "?at=1", path + "?thinking=1"} {
		_, raw := e.get(t, e.Owner, rawPath)
		if !strings.Contains(raw, `class="flow"`) {
			t.Errorf("%s does not open the raw log", rawPath)
		}
	}
	_, back := e.get(t, e.Owner, path+"?view=ghosttree&q=zebrafish")
	if strings.Contains(back, `class="flow"`) {
		t.Error("an explicit view=ghosttree is overruled by the search")
	}
}

// What a viewer may not see of the entries stays out of the view: an issue of a
// project they have no role in, and for a guest the machine and the touched files.
func TestTheGhosttreeViewShowsAGuestNothingTheyMayNotSee(t *testing.T) {
	e := seedSessions(t)
	sid := e.id["anna-guests"]
	d, err := e.St.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Hidden work", Scope: scope.Axes{Project: shellHiddenProject}, Person: "alice"}, Criteria: []string{"Secret criterion"}})
	if err != nil {
		t.Fatal(err)
	}
	cid := d.Criteria[0].ID
	if err := e.St.SetCriterionState(cid, "met", requestdomain.Evidence{Kind: "test", Ref: "go test", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := e.St.RecordActor(actordomain.Criterion, cid, actordomain.Actor{Kind: actordomain.Agent, Harness: "claude-code", SessionID: sid, AccountID: 2}); err != nil {
		t.Fatal(err)
	}
	code, page := e.get(t, e.Guest, "/ui/sessions/"+e.pid["anna-guests"])
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if strings.Contains(page, "Secret criterion") || strings.Contains(page, "Hidden work") {
		t.Error("the guest sees an entry of a project they have no role in")
	}
	if strings.Contains(page, ">laptop<") {
		t.Error("the guest sees the machine")
	}
}

func pain13(t *testing.T, e shellEnv) pain.Pain {
	t.Helper()
	p, err := tpOwner(e).CreatePain(store.CreatePainInput{Project: shellProject, Title: "Slow start", Body: "Takes minutes.", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	return p.Pain
}

// The head of a pain has "Edit" and "Issue from this"; a guest has neither. The
// correction needs a reason, changes the title, and the history says so.
func TestThePainHeadEditsTheTitleWithAReasonAndLeadsToAnIssue(t *testing.T) {
	e := ovEnv(t)
	p := pain13(t, e)
	path := "/ui/x/shell/pains/" + itoa(p.ID)
	_, page := fetchPage(t, e.Owner, e.Base+path)
	for _, want := range []string{`action="/ui/pains/` + itoa(p.ID) + `/correct"`, "Issue from this", "/issues/new?derived_from=PAIN-" + itoa(p.ID)} {
		if !strings.Contains(page, want) {
			t.Errorf("the pain page lacks %q", want)
		}
	}
	_, guest := fetchPage(t, e.Guest, e.Base+path)
	if strings.Contains(guest, "/correct") || strings.Contains(guest, "Issue from this") {
		t.Error("a guest gets the buttons of the head")
	}
	post := "/ui/pains/" + itoa(p.ID) + "/correct"
	resp := postForm(t, e, e.Owner, post, url.Values{"title": {"Slow restart"}, "body": {"Takes minutes."}, "reason": {" "}})
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || !strings.Contains(loc, "fail=reason_required") {
		t.Fatalf("without a reason -> %d %q", resp.StatusCode, loc)
	}
	resp = postForm(t, e, e.Owner, post, url.Values{"title": {"Slow restart"}, "body": {"Takes minutes."}, "reason": {"Wrong word"}})
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || !strings.Contains(loc, "done=corrected") {
		t.Fatalf("correct -> %d %q", resp.StatusCode, loc)
	}
	got, err := tpOwner(e).PainDetailView(p.ID)
	if err != nil || got.Pain.Title != "Slow restart" || got.Pain.Body != "Takes minutes." {
		t.Fatalf("pain after the correction: %+v %v", got.Pain, err)
	}
	if resp := postForm(t, e, e.Guest, post, url.Values{"title": {"Mine now"}, "reason": {"x"}}); resp.StatusCode == http.StatusSeeOther && strings.Contains(resp.Header.Get("Location"), "done=corrected") {
		t.Error("a guest corrected the pain")
	}
}

// The same for an idea: edit with a reason, the old title struck through in the
// history, nothing for a guest.
func TestTheIdeaHeadEditsTitleAndTextAndTheHistoryKeepsTheOldWords(t *testing.T) {
	e := ovEnv(t)
	i, err := tpOwner(e).CreateIdea(store.CreateIdeaInput{Project: shellProject, Title: "Voice in the inbox", Body: "Say the answer.", Actor: store.IdeaActorFor(store.Principal{ID: "person:1", Label: "alice"}, "")})
	if err != nil {
		t.Fatal(err)
	}
	path := "/ui/x/shell/ideas/" + itoa(i.ID)
	_, page := fetchPage(t, e.Owner, e.Base+path)
	if !strings.Contains(page, `action="/ui/ideas/`+itoa(i.ID)+`/correct"`) {
		t.Error("the idea page has no edit panel")
	}
	_, guest := fetchPage(t, e.Guest, e.Base+path)
	if strings.Contains(guest, "/correct") {
		t.Error("a guest gets the panel")
	}
	post := "/ui/ideas/" + itoa(i.ID) + "/correct"
	resp := postForm(t, e, e.Owner, post, url.Values{"title": {"Voice in the inbox"}, "body": {"Say the answer."}, "reason": {"nothing"}})
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "fail=nothing_to_change") {
		t.Errorf("an unchanged idea -> %q", loc)
	}
	resp = postForm(t, e, e.Owner, post, url.Values{"title": {"Dictate answers"}, "body": {"Say the answer, then confirm."}, "reason": {"Sharper"}})
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "done=corrected") {
		t.Fatalf("correct -> %q", loc)
	}
	got, err := tpOwner(e).IdeaView(i.ID)
	if err != nil || got.Title != "Dictate answers" || got.Body != "Say the answer, then confirm." {
		t.Fatalf("idea after the correction: %+v %v", got, err)
	}
	_, after := fetchPage(t, e.Owner, e.Base+path)
	if !strings.Contains(after, "corrected title and text") || !strings.Contains(after, "<s>Voice in the inbox</s>") || !strings.Contains(after, "Sharper") {
		t.Error("the history does not say what changed")
	}
	if resp := postForm(t, e, e.Guest, post, url.Values{"title": {"Mine"}, "reason": {"x"}}); strings.Contains(resp.Header.Get("Location"), "done=corrected") {
		t.Error("a guest corrected the idea")
	}
}

// "Make an issue of this" fills the form from where the issue comes from: the
// title of a pain, and from the concept of an idea the title and the requirements
// (what the viewer may read, which the store checks).
func TestTheFormOfANewIssueIsFilledFromThePainOrTheConceptItComesFrom(t *testing.T) {
	e := ovEnv(t)
	p := pain13(t, e)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues/new?derived_from=PAIN-"+itoa(p.ID))
	if !strings.Contains(page, `value="Slow start"`) || !strings.Contains(page, "Takes minutes.") {
		t.Error("the form is not filled from the pain")
	}
	i, err := tpOwner(e).CreateIdea(store.CreateIdeaInput{Project: shellProject, Title: "Voice in the inbox", Body: "Say the answer.", Actor: store.IdeaActorFor(store.Principal{ID: "person:1", Label: "alice"}, "")})
	if err != nil {
		t.Fatal(err)
	}
	about, what := "The way from a voice note to the release.", "The page shows the summary."
	if _, _, err := tpOwner(e).PutIdeaConcept(i.ID, store.PutIdeaConceptInput{Patch: idea.ConceptPatch{About: &about, What: &what, Requirements: &[]string{"Variants sit side by side", "A decision names a reason"}}, Actor: idea.Actor{Person: "alice"}}); err != nil {
		t.Fatal(err)
	}
	_, page = fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues/new?derived_from=IDEA-"+itoa(i.ID))
	for _, want := range []string{`value="Voice in the inbox"`, about, what, `value="Variants sit side by side"`, `value="A decision names a reason"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the form lacks %q", want)
		}
	}
}
