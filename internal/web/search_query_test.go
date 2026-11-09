package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// qualEnv legt zwei Wissenseinträge mit Entstehungskontext an (Bereich und Zeit),
// einen Issue, einen Pain und eine Idee desselben Verfassers.
func qualEnv(t *testing.T) shellEnv {
	t.Helper()
	e := ovEnv(t)
	storeCtx := originctx.Build("sess-a", "2026-10-01T10:00:00Z", nil, []string{"internal/store/a.go"})
	webCtx := originctx.Build("sess-b", "2026-11-02T10:00:00Z", nil, []string{"internal/web/b.go"})
	knInsert(t, e.St, store.Knowledge{Title: "Needle store lesson", Body: "body", Confidence: "trusted", OriginContext: storeCtx})
	knInsert(t, e.St, store.Knowledge{Title: "Needle web lesson", Body: "body", Confidence: "trusted", OriginContext: webCtx})
	rqMake(t, e.St, rqSpec{Title: "Needle request"})
	if _, err := tpOwner(e).CreatePain(store.CreatePainInput{Project: shellProject, Title: "Needle pain", Actor: pain.Actor{AccountID: 1, Person: "alice"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := tpOwner(e).CreateIdea(store.CreateIdeaInput{Project: shellProject, Title: "Needle idea", Actor: store.IdeaActorFor(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}, "")}); err != nil {
		t.Fatal(err)
	}
	return e
}

func searchPageOf(t *testing.T, c *http.Client, e shellEnv, q string) string {
	t.Helper()
	_, page := fetchPage(t, c, e.Base+"/ui/search?project="+url.QueryEscape(shellProject)+"&q="+url.QueryEscape(q))
	return page
}

// Jeder Treffer zeigt, wann er entstand und in welchem Hauptbereich (REQ-600 K5).
func TestSearchHitsShowTheDateAndTheMainAreaOfTheirOrigin(t *testing.T) {
	e := qualEnv(t)
	page := searchPageOf(t, e.Owner, e, "Needle")
	for _, want := range []string{
		`title="2026-10-01T10:00:00Z">1 Oct`, `title="2026-11-02T10:00:00Z">2 Nov`,
		`</svg>internal/store</span>`, `</svg>internal/web</span>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the hits lack %q", want)
		}
	}
	// Every kind shows a date, the ones without an origin context the one of their creation.
	for _, group := range []string{"Needle request", "Needle idea", "Needle pain"} {
		i := strings.Index(page, group)
		if i < 0 {
			t.Fatalf("no hit %q", group)
		}
		if !strings.Contains(page[i:min(i+700, len(page))], `class="hd"`) {
			t.Errorf("the hit %q shows no date", group)
		}
	}
	// The guest sees date and area of what they may read, nothing of the others.
	if g := searchPageOf(t, e.Guest, e, "Needle"); !strings.Contains(g, "internal/store") || strings.Contains(g, "Needle pain") {
		t.Errorf("guest hits: area missing or a pain shown")
	}
}

// Die Suche versteht die Qualifier aus internal/query (REQ-600 K8).
func TestSearchUnderstandsTheQualifiersOfTheSearchLanguage(t *testing.T) {
	e := qualEnv(t)
	cases := []struct {
		q          string
		has, hasNo []string
	}{
		{"Needle area:internal/store", []string{"Needle store lesson"}, []string{"Needle web lesson", "Needle request", "Needle pain"}},
		{"Needle -area:internal/store kind:knowledge", []string{"Needle web lesson"}, []string{"Needle store lesson"}},
		{"Needle since:2026-10-15 kind:knowledge", []string{"Needle web lesson"}, []string{"Needle store lesson"}},
		{"Needle until:2026-10-15 kind:knowledge", []string{"Needle store lesson"}, []string{"Needle web lesson"}},
		{"is:pain", []string{"Needle pain"}, []string{"Needle request", "Needle lesson", "Needle idea"}},
		{"is:idea Needle", []string{"Needle idea"}, []string{"Needle pain", "Needle request"}},
		{"author:alice kind:request,pain", []string{"Needle request", "Needle pain"}, []string{"Needle idea", "Needle lesson"}},
		{"author:nobody Needle", nil, []string{"Needle request", "Needle pain", "Needle idea", "Needle store lesson"}},
		{"is:open kind:request repo:x/shell", []string{"Needle request"}, []string{"Needle pain"}},
		{"is:open kind:request repo:x/hidden", nil, []string{"Needle request"}},
	}
	for _, c := range cases {
		page := searchPageOf(t, e.Owner, e, c.q)
		for _, w := range c.has {
			if !strings.Contains(page, w) {
				t.Errorf("%q lacks %q", c.q, w)
			}
		}
		for _, w := range c.hasNo {
			if strings.Contains(page, w) {
				t.Errorf("%q wrongly shows %q", c.q, w)
			}
		}
	}
}

func TestSearchSaysWhichQualifierIsUnknownAndWhichGuestsDoNotGet(t *testing.T) {
	e := qualEnv(t)
	page := searchPageOf(t, e.Owner, e, "Needle flavor:sweet")
	if !strings.Contains(page, "The qualifier flavor: is not known") || !strings.Contains(page, "area, author") || strings.Contains(page, "Needle request") {
		t.Errorf("an unknown qualifier is not named, or the search ran anyway")
	}
	if p := searchPageOf(t, e.Owner, e, "is:oen"); !strings.Contains(p, "cannot be read") {
		t.Errorf("an unknown value of a known qualifier is not reported")
	}
	if p := searchPageOf(t, e.Owner, e, `label:"x`); !strings.Contains(p, "cannot be read") {
		t.Errorf("an unclosed quotation mark is not reported")
	}
	for _, key := range []string{"package:3", "for:alice", "branch:feat/secret"} {
		g := searchPageOf(t, e.Guest, e, "Needle "+key)
		if want := "The qualifier " + key[:strings.Index(key, ":")+1] + " is not offered to guests"; !strings.Contains(g, want) {
			t.Errorf("guest with %s: no notice %q", key, want)
		}
		if strings.Contains(g, "Needle request") {
			t.Errorf("guest with %s: the search ran anyway", key)
		}
		if o := searchPageOf(t, e.Owner, e, "Needle "+key); strings.Contains(o, "not offered to guests") {
			t.Errorf("owner with %s is told it is a guest qualifier", key)
		}
	}
	// A guest may filter by the default branch.
	if g := searchPageOf(t, e.Guest, e, "Needle branch:main"); strings.Contains(g, "not offered to guests") {
		t.Errorf("guest with the default branch is refused")
	}
	// A kind that the page does not show in groups is said, not dropped silently.
	if p := searchPageOf(t, e.Owner, e, "kind:file Needle"); !strings.Contains(p, "file are searched with the API") {
		t.Errorf("a kind without a group is not said")
	}
	// A topic needs a project, and an unknown one is an error, not an empty answer.
	if p := searchPageOf(t, e.Owner, e, "topic:nope"); !strings.Contains(p, "no topic named nope") {
		t.Errorf("unknown topic: %s", p[strings.Index(p, "<main"):][:300])
	}
}

// Schnellfilter (REQ-600 K9): mitgelieferte und gespeicherte Suchen stehen neben der
// Suche und laufen dort; mehrere Zeilen werden zusammengelegt.
func TestQuickFiltersRunTheSuppliedSearchesAndJoinTheirLines(t *testing.T) {
	e := qualEnv(t)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/search?project="+url.QueryEscape(shellProject))
	for _, want := range []string{"Quick filters", "Waiting for me", "Done in a pull request", "Open on a branch", "About a folder", "Changed since", `name="pr"`, `name="preset" value="pr-ergebnis"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the quick filters lack %q", want)
		}
	}
	// "fuer-mich" has two lines; the hits of both stand in the groups.
	_, run := fetchPage(t, e.Owner, e.Base+"/ui/search?project="+url.QueryEscape(shellProject)+"&preset=fuer-mich")
	for _, want := range []string{"Needle request", "Needle pain", "Needle idea", "kind:request,pain,idea is:open author:@me"} {
		if !strings.Contains(run, want) {
			t.Errorf("the quick filter \"Waiting for me\" lacks %q", want)
		}
	}
	// A folder: the entries that came to be there.
	_, folder := fetchPage(t, e.Owner, e.Base+"/ui/search?project="+url.QueryEscape(shellProject)+"&preset=ordner&area=internal/store")
	if !strings.Contains(folder, "Needle store lesson") || strings.Contains(folder, "Needle web lesson") {
		t.Errorf("the quick filter for a folder does not narrow to it")
	}
	// A missing or wrong value says what is needed and runs nothing.
	for _, q := range []string{"preset=ordner", "preset=pr-ergebnis&pr=abc%20is:open", "preset=pr-ergebnis&commit=zz"} {
		_, bad := fetchPage(t, e.Owner, e.Base+"/ui/search?project="+url.QueryEscape(shellProject)+"&"+q)
		if !strings.Contains(bad, "needs a valid value for") || strings.Contains(bad, "Needle request") {
			t.Errorf("%s: no notice, or it ran", q)
		}
	}
	// A value that is one word but no date is caught by the search line itself.
	if _, bad := fetchPage(t, e.Owner, e.Base+"/ui/search?project="+url.QueryEscape(shellProject)+"&preset=seit&since=yesterday"); !strings.Contains(bad, "cannot be read") || strings.Contains(bad, "Needle request") {
		t.Errorf("a value that is no date is not reported")
	}
	if _, bad := fetchPage(t, e.Owner, e.Base+"/ui/search?project="+url.QueryEscape(shellProject)+"&preset=nope"); !strings.Contains(bad, "no quick filter of that name") {
		t.Errorf("an unknown quick filter is not reported")
	}
}

// Gespeichert wird für mich, das Projekt oder die Org, je nach Rolle; ein
// gespeicherter Schnellfilter läuft wie ein mitgelieferter (dessen Namen er nicht
// tragen darf).
func TestSavingASearchForMeTheProjectOrTheOrgFollowsTheRole(t *testing.T) {
	e := qualEnv(t)
	save := func(c *http.Client, name, scopeKind string) int {
		form := url.Values{"project": {shellProject}, "q": {"kind:knowledge Needle"}, "name": {name}, "scope": {scopeKind}, "back": {"/ui/search"}}
		return postForm(t, e, c, "/ui/searches", form).StatusCode
	}
	if code := save(e.Member, "my-view", "user"); code != http.StatusSeeOther {
		t.Fatalf("member saves for themselves -> %d", code)
	}
	if code := save(e.Member, "team-view", "project"); code != http.StatusSeeOther {
		t.Errorf("a member saves for the project -> %d", code)
	}
	if code := save(e.Member, "org-view", "org"); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("a member saves for the org -> %d, want it refused", code)
	}
	if code := save(e.Guest, "guest-team-view", "project"); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("a guest saves for the project -> %d, want it refused", code)
	}
	if code := save(e.Owner, "org-view", "org"); code != http.StatusSeeOther {
		t.Errorf("the owner of the org saves for it -> %d", code)
	}
	if code := save(e.Owner, "Odd", "everybody"); code != http.StatusBadRequest {
		t.Errorf("an unknown scope -> %d, want 400", code)
	}
	scopes := func(c *http.Client) string {
		_, p := fetchPage(t, c, e.Base+"/ui/search?project="+url.QueryEscape(shellProject)+"&q=Needle")
		return p
	}
	if p := scopes(e.Member); !strings.Contains(p, `value="project"`) || strings.Contains(p, `value="org"`) {
		t.Errorf("a member is offered the wrong places to save")
	}
	if p := scopes(e.Owner); !strings.Contains(p, `value="org"`) {
		t.Errorf("the org owner is not offered the org")
	}
	if p := scopes(e.Guest); strings.Contains(p, `action="/ui/searches"`) {
		t.Errorf("a guest is offered to save a search")
	}
	// The team view and the org view are quick filters for anyone in the project; mine for me only.
	_, other := fetchPage(t, e.Lead, e.Base+"/ui/search?project="+url.QueryEscape(shellProject))
	if !strings.Contains(other, "team-view") || !strings.Contains(other, "org-view") || strings.Contains(other, "my-view") {
		t.Errorf("the quick filters of another person are wrong")
	}
	_, mine := fetchPage(t, e.Member, e.Base+"/ui/search?project="+url.QueryEscape(shellProject)+"&preset=my-view")
	if !strings.Contains(mine, "Needle store lesson") || !strings.Contains(mine, "kind:knowledge Needle") {
		t.Errorf("a saved quick filter does not run")
	}
}

// Die PR- und die Commit-Seite bieten „Was wurde hier erledigt?“ an (REQ-599).
func TestPullAndCommitPagesOfferWhatWasDoneHere(t *testing.T) {
	e, _ := pullsEnv(t)
	_, pull := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/pulls/12")
	want := "/ui/search?pr=12&amp;preset=pr-ergebnis&amp;project=" + url.QueryEscape(shellProject)
	if !strings.Contains(pull, "What was done here?") || !strings.Contains(pull, want) {
		t.Errorf("the pull request page lacks the quick filter: want %q", want)
	}
	_, commit := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/commits/"+pullShaA)
	wantCommit := "/ui/search?commit=" + pullShaA + "&amp;preset=pr-ergebnis&amp;project=" + url.QueryEscape(shellProject)
	if !strings.Contains(commit, "What was done here?") || !strings.Contains(commit, wantCommit) {
		t.Errorf("the commit page lacks the quick filter: want %q", wantCommit)
	}
	// The link leads to the lines of the supplied search, with the PR or the commit put in.
	_, pr := fetchPage(t, e.Owner, e.Base+"/ui/search?project="+url.QueryEscape(shellProject)+"&preset=pr-ergebnis&pr=12")
	for _, line := range []string{"kind:criterion is:done pr:12", "kind:request is:done pr:12", "kind:knowledge pr:12"} {
		if !strings.Contains(pr, line) {
			t.Errorf("the PR view lacks the line %q", line)
		}
	}
	_, cm := fetchPage(t, e.Owner, e.Base+"/ui/search?project="+url.QueryEscape(shellProject)+"&preset=pr-ergebnis&commit="+pullShaA)
	if !strings.Contains(cm, "kind:knowledge commit:"+pullShaA) || strings.Contains(cm, "pr:{pr}") {
		t.Errorf("the commit view does not use the commit")
	}
	// The knowledge anchored at the commit is what was said there.
	if !strings.Contains(cm, "Anchored pitfall") {
		t.Errorf("the commit view does not show what was said at the commit")
	}
}
