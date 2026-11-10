package web

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var groupHeadRE = regexp.MustCompile(`<div class="box-h"><h2 class="t">([^<]+)</h2>`)

func searchGroups(page string) []string {
	var out []string
	for _, m := range groupHeadRE.FindAllStringSubmatch(page, -1) {
		out = append(out, m[1])
	}
	return out
}

func searchEnv(t *testing.T) shellEnv {
	t.Helper()
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Needle request"})
	hidden := rqMake(t, e.St, rqSpec{Title: "Needle SECRET-REQ", Project: shellHiddenProject})
	knInsert(t, e.St, store.Knowledge{Title: "Needle lesson", Body: "body", Confidence: "trusted"})
	knInsert(t, e.St, store.Knowledge{Title: "Needle SECRET-LESSON", Body: "body", Confidence: "trusted", Scope: scope.Axes{Project: shellHiddenProject}})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "Needle task"})
	tpTask(t, e, shellHiddenProject, hidden.Request.ID, humantask.CreateInput{Title: "Needle SECRET-TASK"})
	pa := tpOwner(e)
	actor := pain.Actor{AccountID: 1, Person: "alice"}
	if _, err := pa.CreatePain(store.CreatePainInput{Project: shellProject, Title: "Needle pain", Actor: actor}); err != nil {
		t.Fatal(err)
	}
	if _, err := pa.CreatePain(store.CreatePainInput{Project: shellHiddenProject, Title: "Needle SECRET-PAIN", Actor: actor}); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestSearchGroupsFollowTheViewersRights(t *testing.T) {
	e := searchEnv(t)
	for name, c := range map[string]*http.Client{"owner": e.Owner, "member": e.Member, "guest": e.Guest} {
		code, page := fetchPage(t, c, e.Base+"/ui/search?q=Needle")
		if code != http.StatusOK {
			t.Fatalf("%s: status %d", name, code)
		}
		if name != "owner" && strings.Contains(page, "SECRET") {
			t.Errorf("%s: search shows an entry of a project without a role", name)
		}
		groups := searchGroups(page)
		switch name {
		case "guest":
			if strings.Join(groups, ",") != "Requests,Knowledge" {
				t.Errorf("guest groups = %v, want only Requests and Knowledge", groups)
			}
		default:
			if strings.Join(groups, ",") != "Requests,Knowledge,Tasks,Pains" {
				t.Errorf("%s groups = %v, want the fixed order Requests, Knowledge, Tasks, Pains", name, groups)
			}
		}
	}
	_, owner := fetchPage(t, e.Owner, e.Base+"/ui/search?q=Needle")
	if !strings.Contains(owner, "SECRET-REQ") || !strings.Contains(owner, "SECRET-PAIN") {
		t.Error("the owner of both projects finds neither")
	}
}

func TestSearchCountsHitsAndOffersMoreOnlyBehindTheShownOnes(t *testing.T) {
	e := ovEnv(t)
	for i := 0; i < 3; i++ {
		rqMake(t, e.St, rqSpec{Title: fmt.Sprintf("Needle %d", i)})
	}
	_, short := fetchPage(t, e.Member, e.Base+"/ui/search?q=N")
	if len(searchGroups(short)) != 0 || strings.Contains(short, "No matches") {
		t.Error("a one-character query must not search or answer")
	}
	_, few := fetchPage(t, e.Member, e.Base+"/ui/search?q=Needle")
	if strings.Contains(few, "More hits") || !strings.Contains(few, "3 hits") {
		t.Error("three hits state their count and offer no more")
	}
	for i := 3; i < 8; i++ {
		rqMake(t, e.St, rqSpec{Title: fmt.Sprintf("Needle %d", i)})
	}
	_, many := fetchPage(t, e.Member, e.Base+"/ui/search?q=Needle")
	if !strings.Contains(many, "8 hits") || strings.Count(many, `class="hit"`) != searchPerGroup {
		t.Errorf("eight hits must say 8 and show %d", searchPerGroup)
	}
	if !regexp.MustCompile(`<span class="nm">All</span><span class="counter">8</span>`).MatchString(many) ||
		!regexp.MustCompile(`<span class="counter">8</span></a>`).MatchString(many) {
		t.Error("the facets do not count 8 in All and in Requests")
	}
	if !strings.Contains(many, `class="row morelink" href="/ui/search?kind=requests&amp;q=Needle"`) {
		t.Error("the shown five do not lead to the rest")
	}
	_, kind := fetchPage(t, e.Member, e.Base+"/ui/search?q=Needle&kind=requests")
	if strings.Count(kind, `class="hit"`) != 8 || strings.Contains(kind, "morelink") {
		t.Error("a click on the facet must show all eight hits, not the same five")
	}
	_, none := fetchPage(t, e.Member, e.Base+"/ui/search?q=zzzzqq")
	if !strings.Contains(none, "No matches") {
		t.Error("no hits must say so in one line")
	}
}

// A page after the first: "More hits" of a narrowed page adds a step (n=), and a
// count beyond the cap says so instead of stating a number it did not count.
func TestSearchMoreHitsPagesAndTheCountStopsAtTheCap(t *testing.T) {
	e := ovEnv(t)
	for i := 0; i < searchKindPage+3; i++ {
		rqMake(t, e.St, rqSpec{Title: fmt.Sprintf("Haystack %02d", i)})
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/search?q=Haystack&kind=requests")
	if n := strings.Count(page, `class="hit"`); n != searchKindPage {
		t.Errorf("a narrowed page shows %d hits, want %d", n, searchKindPage)
	}
	if !strings.Contains(page, "23 hits") || !strings.Contains(page, `href="/ui/search?kind=requests&amp;q=Haystack&amp;n=40"`) {
		t.Error("the narrowed page does not count 23 or does not lead to the next step")
	}
	_, page = fetchPage(t, e.Member, e.Base+"/ui/search?q=Haystack&kind=requests&n=40")
	if n := strings.Count(page, `class="hit"`); n != searchKindPage+3 || strings.Contains(page, "morelink") {
		t.Errorf("the second step shows %d hits (want all 23, no further link)", n)
	}
	for _, bad := range []string{"-5", "0", "x", "100000"} {
		code, page := fetchPage(t, e.Member, e.Base+"/ui/search?q=Haystack&kind=requests&n="+bad)
		if code != http.StatusOK || strings.Count(page, `class="hit"`) < searchKindPage || strings.Count(page, `class="hit"`) > searchCountCap {
			t.Errorf("n=%s: status %d, %d hits", bad, code, strings.Count(page, `class="hit"`))
		}
	}
}

// A guest is counted only what he may read: the hit of a hidden project is in
// neither the number nor the list.
func TestSearchCountsOnlyWhatTheViewerMayRead(t *testing.T) {
	e := searchEnv(t)
	_, owner := fetchPage(t, e.Owner, e.Base+"/ui/search?q=Needle&kind=requests")
	_, guest := fetchPage(t, e.Guest, e.Base+"/ui/search?q=Needle&kind=requests")
	if !strings.Contains(owner, "2 hits") {
		t.Errorf("the owner of both projects is not counted both requests")
	}
	if !strings.Contains(guest, "1 hit") || strings.Contains(guest, "2 hits") || strings.Contains(guest, "SECRET") {
		t.Errorf("the guest is counted what he may not read: %s", guest)
	}
}

func TestSearchHonoursTheSelectedProjectAndNeverReachesAHiddenOne(t *testing.T) {
	e := searchEnv(t)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/search?q=Needle&project="+shellHiddenProject)
	if strings.Contains(page, "Needle request") || !strings.Contains(page, "SECRET-REQ") {
		t.Error("the selected project does not narrow the search")
	}
	_, page = fetchPage(t, e.Member, e.Base+"/ui/search?q=Needle&project="+shellHiddenProject)
	if strings.Contains(page, "SECRET") {
		t.Error("a member reaches a hidden project through the project parameter")
	}
}

func TestReviewListsEveryProposalAsOneOpenableRow(t *testing.T) {
	e := ovEnv(t)
	for i := 0; i < 5; i++ {
		knInsert(t, e.St, store.Knowledge{Type: "pitfall", Title: fmt.Sprintf("Proposal %d", i), Body: "Check the lock.", Confidence: "staged"})
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/review")
	if strings.Count(page, `<details class="rvd">`) != 5 {
		t.Error("all five proposals must be rows of the one list")
	}
	for i := 0; i < 5; i++ {
		if !strings.Contains(page, fmt.Sprintf("Proposal %d", i)) {
			t.Errorf("proposal %d is missing", i)
		}
	}
	if strings.Contains(page, `class="kn-card`) || strings.Contains(page, "g-card--tight") {
		t.Error("the old card is still there")
	}
}

func TestDetailPagesHaveNoProjectSelectorThatCouldSayTheWrongProject(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Somewhere"})
	for _, path := range []string{fmt.Sprintf("/ui/requests/%d", req.Request.ID), "/ui/search?q=zz"} {
		_, page := fetchPage(t, e.Owner, e.Base+path)
		if path[:10] == "/ui/reques" && strings.Contains(page, `class="scope"`) {
			t.Errorf("%s shows a project selector", path)
		}
	}
}

func TestNavigationHasFiveEntriesAndTabsForTheRest(t *testing.T) {
	e := ovEnv(t)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/overview")
	nav := page[strings.Index(page, `<nav class="shell-nav"`):]
	nav = nav[:strings.Index(nav, `</nav>`)]
	if n := strings.Count(nav, `data-nav="`); n != 5 {
		t.Errorf("navigation has %d entries, want 5: %s", n, nav)
	}
	for path, want := range map[string][]string{
		"/ui/agents":   {`data-tab="agents"`, `data-tab="sessions"`},
		"/ui/sessions": {`data-tab="agents"`, `data-tab="sessions"`},
		"/ui/review":   {`data-seg="entries"`, `data-seg="review"`, `data-seg="context"`},
		"/ui/orgs":     {`class="box snav"`, `href="/ui/account/tokens"`},
	} {
		_, p := fetchPage(t, e.Owner, e.Base+path)
		for _, w := range want {
			if !strings.Contains(p, w) {
				t.Errorf("%s lacks %s", path, w)
			}
		}
	}
}

func TestGermanInboxAndSearchSpeakGerman(t *testing.T) {
	srv, token := testWebLang(t, "de")
	c := login(t, srv, token)
	_, inbox := fetchPage(t, c, srv.URL+"/ui/inbox")
	if !strings.Contains(inbox, "Nichts wartet auf dich.") || !strings.Contains(inbox, "Erledigte zeigen") {
		t.Errorf("empty inbox is not German: %s", inbox)
	}
	_, search := fetchPage(t, c, srv.URL+"/ui/search?q=zzzzqq")
	if !strings.Contains(search, "Keine Treffer") || !strings.Contains(search, "Alles durchsuchen") {
		t.Error("search page is not German")
	}
	if !strings.Contains(search, "Team &amp; Maschinen") && !strings.Contains(search, "Team &#38; Maschinen") {
		t.Error("the account menu lacks Team & Maschinen")
	}
}
