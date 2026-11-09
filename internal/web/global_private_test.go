package web

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Wissen und Aufträge ohne Projekt gehören ihrem Autor (alice): Mitglied, Gast
// und Lead sehen sie in keiner Seite und kommen auch über die Adresse nicht heran.
func TestGlobalEntriesAreHiddenFromEveryoneButTheirAuthorInTheWeb(t *testing.T) {
	e := ovEnv(t)
	kid, err := e.St.InsertKnowledge(store.Knowledge{Type: "note", Title: "Alices private lesson", Body: "b", Person: "alice", AuthorID: 1, Confidence: "trusted"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.St.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Alices private wish", Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	pages := []string{"/ui", "/ui/knowledge", "/ui/search?q=private", "/ui/knowledge/" + strconv.FormatInt(kid, 10), rqPath(d.Request.ID, "")}
	for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest, "lead": e.Lead, "reviewer": e.Reviewer} {
		for _, p := range pages {
			_, page := fetchPage(t, c, e.Base+p)
			if strings.Contains(page, "Alices private") {
				t.Errorf("%s sees the global entry on %s", name, p)
			}
		}
		if code, _ := fetchPage(t, c, e.Base+"/ui/knowledge/"+strconv.FormatInt(kid, 10)); code != 404 {
			t.Errorf("%s opens the global entry: %d", name, code)
		}
		if code, _ := fetchPage(t, c, e.Base+rqPath(d.Request.ID, "")); code != 404 {
			t.Errorf("%s opens the global request: %d", name, code)
		}
	}
	_, list := fetchPage(t, e.Owner, e.Base+"/ui/knowledge")
	if !strings.Contains(list, "Alices private lesson") {
		t.Error("the author lost his entry")
	}
	_, reqs := fetchPage(t, e.Owner, e.Base+"/ui/search?q=private+wish")
	if !strings.Contains(reqs, "Alices private wish") {
		t.Error("the author lost his request")
	}
}

// A request without a project has no repository: its page is the issue page on
// the new base, without the repository band, with its actions under
// /ui/requests/{id}, and without comments (a comment needs a project).
func TestAnIssueWithoutAProjectUsesTheIssuePageWithoutARepository(t *testing.T) {
	e := ovEnv(t)
	d, err := e.St.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Alices loose wish", Description: "No project yet.", Person: "alice"}, Criteria: []string{"One"}})
	if err != nil {
		t.Fatal(err)
	}
	status, page := fetchPage(t, e.Owner, e.Base+rqPath(d.Request.ID, ""))
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	for _, want := range []string{"Alices loose wish", "No project yet.", `class="dgrid"`, `action="/ui/requests/` + strconv.FormatInt(d.Request.ID, 10) + `/close"`, `href="/ui/requests/` + strconv.FormatInt(d.Request.ID, 10) + `?panel=edit#edit"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(page, `class="rband`) || strings.Contains(page, `data-val="comment"`) || strings.Contains(page, "clay") {
		t.Error("the page shows a repository band, a comment mode or the old surface")
	}
}
