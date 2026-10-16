package web

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var groupHeadRE = regexp.MustCompile(`<h2 class="g-group-head"><span>([^<]+)(?:</span|<span)`)

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

func TestSearchNeedsTwoCharactersShowsNoTotalsAndOffersAllOnlyBehindFiveHits(t *testing.T) {
	e := ovEnv(t)
	for i := 0; i < 3; i++ {
		rqMake(t, e.St, rqSpec{Title: fmt.Sprintf("Needle %d", i)})
	}
	_, short := fetchPage(t, e.Member, e.Base+"/ui/search?q=N")
	if len(searchGroups(short)) != 0 || strings.Contains(short, "No matches") {
		t.Error("a one-character query must not search or answer")
	}
	_, few := fetchPage(t, e.Member, e.Base+"/ui/search?q=Needle")
	if strings.Contains(few, "Show all") {
		t.Error("three hits offer Show all")
	}
	for i := 3; i < 6; i++ {
		rqMake(t, e.St, rqSpec{Title: fmt.Sprintf("Needle %d", i)})
	}
	_, many := fetchPage(t, e.Member, e.Base+"/ui/search?q=Needle")
	if !strings.Contains(many, "Show all") || strings.Count(many, `class="g-row g-hit"`) != searchPerGroup {
		t.Errorf("six hits must show %d and offer Show all", searchPerGroup)
	}
	if regexp.MustCompile(`(?i)\b\d+ (matches|results|hits)`).MatchString(many) {
		t.Error("the search page states a total")
	}
	_, none := fetchPage(t, e.Member, e.Base+"/ui/search?q=zzzzqq")
	if !strings.Contains(none, "No matches") {
		t.Error("no hits must say so in one line")
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

func TestTaskCardsShowPathAndCheckAsCommandsAndTheWholeDescription(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	tail := "and then run make release-check twice"
	desc := "Create the restricted key in the dashboard. " + strings.Repeat("Keep the scope as small as it goes. ", 12) + tail
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Kind: "file", Title: "Put the dump in place", Description: desc,
		Location: "/srv/arkon/import/2026-10/schema-dump.sql", CheckHint: "test -s /srv/arkon/import/2026-10/schema-dump.sql\nsha256sum -c schema-dump.sha256"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "Nothing to point at"})
	for _, path := range []string{"/ui/tasks", "/ui/overview"} {
		_, page := fetchPage(t, e.Member, e.Base+path)
		if strings.Count(page, `class="g-card clay"`) != 2 {
			t.Fatalf("%s: want two cards", path)
		}
		if strings.Count(page, `class="g-cmd"`) != 3 {
			t.Errorf("%s: want one path and two check lines in a trough, got %d", path, strings.Count(page, `class="g-cmd"`))
		}
		if !strings.Contains(page, "/srv/arkon/import/2026-10/schema-dump.sql</code>") || !strings.Contains(page, "sha256sum -c schema-dump.sha256</code>") {
			t.Errorf("%s: path or check line is not whole", path)
		}
		if !strings.Contains(page, tail) {
			t.Errorf("%s: the description is cut", path)
		}
		if strings.Contains(page, `<p class="tk-desc">`) || strings.Contains(page, "…</p>") {
			t.Errorf("%s: still the old truncated description", path)
		}
		if !strings.Contains(page, "<b>Ship it</b>") {
			t.Errorf("%s: the card does not say which request it is for", path)
		}
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/tasks")
	cards := strings.Split(page, `<li class="g-card clay"`)
	if len(cards) != 3 || strings.Contains(cards[2], `class="g-cmd"`) && strings.Contains(cards[2], "Nothing to point at") {
		t.Error("a task without a place must not get a trough")
	}
}

func TestDoneTasksSitBehindOneLine(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	task := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "Already done"})
	open := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "Still open"})
	_ = open
	resp := tpPost(t, e, e.Member, "/ui/tasks/done", url.Values{"id": {fmt.Sprint(task.ID)}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("done status %d", resp.StatusCode)
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/tasks")
	if !strings.Contains(page, `<summary>Show done (1)</summary>`) || strings.Count(page, `class="g-card clay"`) != 1 {
		t.Error("the finished task is not behind its line, or the open one is missing")
	}
}

func TestPainsAreGroupedByStateWithoutIdsInTheList(t *testing.T) {
	e := ovEnv(t)
	pa := tpOwner(e)
	actor := pain.Actor{AccountID: 1, Person: "alice"}
	mk := func(title string) int64 {
		d, err := pa.CreatePain(store.CreatePainInput{Project: shellProject, Title: title, Actor: actor})
		if err != nil {
			t.Fatal(err)
		}
		return d.Pain.ID
	}
	mk("Still burning")
	mit := mk("Half solved")
	done := mk("Gone again")
	ev := pain.Evidence{Kind: "commit", Ref: "abc123"}
	if _, err := pa.SetPainState(mit, "mitigated", "Dry run exists for arkon, not for pixelworks.", ev, "", actor); err != nil {
		t.Fatal(err)
	}
	if _, err := pa.SetPainState(done, "resolved", "", ev, "", actor); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/pains")
	if got := strings.Join(searchGroups(page), ","); got != "Active,Mitigated" {
		t.Errorf("groups = %q", got)
	}
	for _, want := range []string{"Closed (1)", "Dry run exists for arkon", "Still burning", "Gone again"} {
		if !strings.Contains(page, want) {
			t.Errorf("pains page lacks %q", want)
		}
	}
	if strings.Contains(page, ">PAIN-") {
		t.Error("the list shows the pain number as text")
	}
	if strings.Count(page, `class="g-dot is-`) != 3 {
		t.Error("every pain has one status dot")
	}
}

func TestReviewGroupsByProjectAndFoldsFromTheFifthEntry(t *testing.T) {
	e := ovEnv(t)
	for i := 0; i < 5; i++ {
		knInsert(t, e.St, store.Knowledge{Type: "pitfall", Title: fmt.Sprintf("Proposal %d", i), Body: "Check the lock.", Confidence: "staged"})
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/review")
	if got := strings.Join(searchGroups(page), ","); !strings.HasPrefix(got, "shell") {
		t.Errorf("groups = %q, want the project", got)
	}
	if !strings.Contains(page, "2 more in shell") {
		t.Error("the fourth and fifth card are not behind one line")
	}
	if strings.Count(page, `class="g-card g-card--tight clay"`) != 5 {
		t.Error("all five cards must be in the page, the last two folded")
	}
	if strings.Contains(page, `class="kn-card`) {
		t.Error("the old card is still there")
	}
}

func TestDetailPagesHaveNoProjectSelectorThatCouldSayTheWrongProject(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Somewhere"})
	for _, path := range []string{fmt.Sprintf("/ui/requests/%d", req.Request.ID), "/ui/search?q=zz"} {
		_, page := fetchPage(t, e.Owner, e.Base+path)
		if path[:10] == "/ui/reques" && strings.Contains(page, `id="shell-project"`) {
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
		"/ui/review":   {`data-tab="search"`, `data-tab="review"`, `data-tab="context"`},
		"/ui/orgs":     {`data-tab="team"`, `data-tab="devices"`},
	} {
		_, p := fetchPage(t, e.Owner, e.Base+path)
		for _, w := range want {
			if !strings.Contains(p, w) {
				t.Errorf("%s lacks %s", path, w)
			}
		}
	}
}

func TestGermanTasksAndSearchSpeakGerman(t *testing.T) {
	srv, token := testWebLang(t, "de")
	c := login(t, srv, token)
	_, tasks := fetchPage(t, c, srv.URL+"/ui/tasks")
	if !strings.Contains(tasks, "Nichts offen für dich") {
		t.Errorf("empty tasks page is not German: %s", tasks)
	}
	_, search := fetchPage(t, c, srv.URL+"/ui/search?q=zzzzqq")
	if !strings.Contains(search, "Keine Treffer") || !strings.Contains(search, "Alles durchsuchen") {
		t.Error("search page is not German")
	}
	if !strings.Contains(search, "Team &amp; Geräte") && !strings.Contains(search, "Team &#38; Geräte") {
		t.Error("the account menu lacks Team & Geräte")
	}
}
