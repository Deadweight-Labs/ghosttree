package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Aus einem Pain wird mit einem Knopf eine Lösungsidee, die der Pain „motiviert“;
// beide Seiten zeigen die Verbindung (REQ-606).
func TestAPainMakesAnIdeaFromTheButtonAndBothPagesShowIt(t *testing.T) {
	e := ovEnv(t)
	p, err := tpOwner(e).CreatePain(store.CreatePainInput{Project: shellProject, Title: "Writing takes too many steps", Body: "b", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	painPath := "/ui/x/shell/pains/" + itoa(p.Pain.ID)
	_, page := fetchPage(t, e.Member, e.Base+painPath)
	for _, want := range []string{`action="/ui/pains/` + itoa(p.Pain.ID) + `/idea"`, `name="csrf_token"`, `name="title"`, "Create idea"} {
		if !strings.Contains(page, want) {
			t.Errorf("the pain page lacks %q", want)
		}
	}
	resp := postForm(t, e, e.Member, "/ui/pains/"+itoa(p.Pain.ID)+"/idea", url.Values{"title": {"One field for everything"}, "body": {"Type once, sort later."}})
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/ui/x/shell/ideas/") || !strings.Contains(loc, "done=from_pain") {
		t.Fatalf("create idea -> %d %q", resp.StatusCode, loc)
	}
	detail, err := tpOwner(e).PainDetailView(p.Pain.ID)
	if err != nil || len(detail.Motivates) != 1 || detail.Motivates[0].Kind != "idea" || detail.Motivates[0].Title != "One field for everything" {
		t.Fatalf("the pain does not motivate the idea: %+v %v", detail.Motivates, err)
	}
	// The idea page says where it came from; the pain page names the idea.
	_, ideaPage := fetchPage(t, e.Owner, e.Base+loc)
	if !strings.Contains(ideaPage, "Arose from") || !strings.Contains(ideaPage, "Writing takes too many steps") || !strings.Contains(ideaPage, "created from the pain") {
		t.Errorf("the idea page does not show the pain it came from")
	}
	_, painPage := fetchPage(t, e.Owner, e.Base+painPath)
	if !strings.Contains(painPage, "One field for everything") || !strings.Contains(painPage, "IDEA-") || !strings.Contains(painPage, `href="`+strings.SplitN(loc, "?", 2)[0]+`"`) {
		t.Errorf("the pain page does not link the idea")
	}
}

func TestTheIdeaButtonIsOnlyForWhoMayWorkOnThePainAndNeedsTheTokenAndATitle(t *testing.T) {
	e := ovEnv(t)
	p, err := tpOwner(e).CreatePain(store.CreatePainInput{Project: shellProject, Title: "Slow start", Body: "b", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/ui/pains/" + itoa(p.Pain.ID) + "/idea"
	// A guest gets no form and no idea.
	_, guestPage := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/pains/"+itoa(p.Pain.ID))
	if strings.Contains(guestPage, path) {
		t.Error("a guest gets the form")
	}
	if resp := postForm(t, e, e.Guest, path, url.Values{"title": {"Let me"}}); resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
		t.Errorf("a guest makes an idea -> %d", resp.StatusCode)
	}
	// A stranger gets the answer of a pain that does not exist.
	if _, err := e.St.AddPerson("nora"); err != nil {
		t.Fatal(err)
	}
	stranger := loginInteractive(t, e.Srv, e.St, "nora")
	got := postForm(t, e, stranger, path, url.Values{"title": {"Sneaky"}})
	missing := postForm(t, e, stranger, "/ui/pains/9999/idea", url.Values{"title": {"Sneaky"}})
	if got.StatusCode != missing.StatusCode || got.StatusCode == http.StatusSeeOther {
		t.Errorf("stranger -> %d, a missing pain -> %d", got.StatusCode, missing.StatusCode)
	}
	// No token, no idea.
	resp := sameOriginPostForm(t, e.Member, e.Base+path, url.Values{"title": {"No token"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("without a token -> %d, want 403", resp.StatusCode)
	}
	// An empty title leads back with the reason and makes nothing.
	resp = postForm(t, e, e.Member, path, url.Values{"title": {"   "}})
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "fail=idea_invalid") {
		t.Errorf("an empty title -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if d, _ := tpOwner(e).PainDetailView(p.Pain.ID); len(d.Motivates) != 0 {
		t.Errorf("a refused request linked something: %+v", d.Motivates)
	}
}

// Nach dem abgenommenen Mock sind Ideen ein Segment der Eingänge neben Issues
// und Pains (Entscheidung #4315): eigene Adresse, eigene Zahl, Knopf „Daraus
// Issue machen“ an der Idee.
func TestIdeasAreASegmentOfTheInboxesWithTheirOwnAddressAndTheMakeIssueButton(t *testing.T) {
	e := ovEnv(t)
	i, err := tpOwner(e).CreateIdea(store.CreateIdeaInput{Project: shellProject, Title: "Weekly look back", Actor: store.IdeaActorFor(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}, "")})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := e.Owner.Get(e.Base + "/ui/x/shell/ideas")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusFound || !strings.Contains(loc, "is%3Aidea") {
		t.Fatalf("/ideas -> %d %q", resp.StatusCode, loc)
	}
	_, list := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues?q="+url.QueryEscape("is:idea is:open"))
	if !strings.Contains(list, "Weekly look back") || !strings.Contains(list, "Ideas") {
		t.Error("the idea segment does not list the idea")
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/ideas/"+itoa(i.ID))
	if !strings.Contains(page, "issues/new?derived_from=IDEA-"+itoa(i.ID)) || !strings.Contains(page, "Make an issue of it") {
		t.Error("the idea page lacks the button that makes an issue of it")
	}
}
