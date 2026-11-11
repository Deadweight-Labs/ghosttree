package web

import (
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/query"
)

// REQ-599 AC-10 and REQ-628 AC-1: the list under the search field (/ui/search/suggest).

var (
	suggestHitRE  = regexp.MustCompile(`<li role="option" class="sg"><a class="sg-a" href="([^"]*)"`)
	suggestQualRE = regexp.MustCompile(`class="sg sg-q" data-insert="([^"]*)"`)
	suggestAllRE  = regexp.MustCompile(`class="sg sg-all"><a class="sg-a" href="([^"]*)"`)
)

func suggestFor(t *testing.T, x exactEnv, c *http.Client, q string, extra ...string) string {
	t.Helper()
	u := x.Base + "/ui/search/suggest?q=" + url.QueryEscape(q)
	for _, e := range extra {
		u += "&" + e
	}
	code, body := fetchPage(t, c, u)
	if code != http.StatusOK {
		t.Fatalf("suggest %q = %d", q, code)
	}
	return body
}

func suggestHits(body string) []string {
	var out []string
	for _, m := range suggestHitRE.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func suggestInserts(body string) []string {
	var out []string
	for _, m := range suggestQualRE.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

// A number is answered with the object it names, first, and then the line that
// searches for it: every form the search page understands, with its rights.
func TestSuggestShowsTheObjectANumberNamesFirst(t *testing.T) {
	x := seedExact(t)
	n := itoa(x.N)
	for _, tc := range []struct{ q, want string }{
		{"#" + n, x.ReqHref},
		{"REQ-" + n, x.ReqHref},
		{"pr:" + n, x.PullHref},
		{"IDEA-" + itoa(x.Idea), x.IdeaHref},
		{"PAIN-" + itoa(x.Pain), x.PainHref},
		{"TASK-" + itoa(x.Task), x.TaskHref},
	} {
		body := suggestFor(t, x, x.Owner, tc.q)
		if hits := suggestHits(body); len(hits) == 0 || hits[0] != tc.want {
			t.Errorf("%q: the first row is %v, want %s", tc.q, hits, tc.want)
		}
		if m := suggestAllRE.FindStringSubmatch(body); m == nil || !strings.HasPrefix(m[1], "/ui/search?q=") {
			t.Errorf("%q: the last row does not search for the line:\n%s", tc.q, body)
		}
		// The hit comes before the search for all of it.
		if strings.Index(body, `class="sg sg-all"`) < strings.Index(body, tc.want) {
			t.Errorf("%q: the search for all of it comes before the object", tc.q)
		}
	}
	// An ambiguous #N lists every object with the number once.
	hits := suggestHits(suggestFor(t, x, x.Owner, "#"+n))
	if !slices.Contains(hits, x.ReqHref) || !slices.Contains(hits, x.PullHref) {
		t.Errorf("#%s lists %v, want the request and the pull request", n, hits)
	}
	seen := map[string]bool{}
	for _, h := range hits {
		if seen[h] {
			t.Errorf("#%s lists %s twice", n, h)
		}
		seen[h] = true
	}
}

// What the viewer may not read is as unknown as a number that does not exist.
func TestSuggestShowsOnlyWhatTheViewerMayRead(t *testing.T) {
	x := seedExact(t)
	hidden := []string{"REQ-" + itoa(x.HiddenReq), "IDEA-" + itoa(x.HiddenIdea), "pr:99"}
	for _, q := range hidden {
		if len(suggestHits(suggestFor(t, x, x.Owner, q))) == 0 {
			t.Errorf("owner: %q suggests nothing", q)
		}
		for name, c := range map[string]*http.Client{"member": x.Member, "guest": x.Guest} {
			body := suggestFor(t, x, c, q)
			if strings.Contains(body, "SECRET") || strings.Contains(body, "/hidden/") {
				t.Errorf("%s: %q gives away the hidden project:\n%s", name, q, body)
			}
			if len(suggestHits(body)) != 0 {
				t.Errorf("%s: %q suggests %v", name, q, suggestHits(body))
			}
		}
	}
	// A guest has no tasks and pains in the search, so no row leads to them.
	for _, q := range []string{"TASK-" + itoa(x.Task), "PAIN-" + itoa(x.Pain)} {
		if got := suggestHits(suggestFor(t, x, x.Guest, q)); len(got) != 0 {
			t.Errorf("guest: %q suggests %v", q, got)
		}
	}
	// The repository of the page is only used when the viewer reads it.
	body := suggestFor(t, x, x.Guest, "branch:", "project="+url.QueryEscape(shellHiddenProject))
	if strings.Contains(body, "data-insert") {
		t.Errorf("a guest is offered branches:\n%s", body)
	}
}

// A word being typed is completed: first the key, then its value, and what is
// already complete is not offered again.
func TestSuggestCompletesTheQualifierBeingTyped(t *testing.T) {
	x := seedExact(t)
	got := suggestInserts(suggestFor(t, x, x.Owner, "deploy is"))
	if !slices.Equal(got, []string{"is:"}) {

		t.Errorf("deploy is: %v, want [is:]", got)
	}
	if got := suggestInserts(suggestFor(t, x, x.Owner, "deploy b")); !slices.Equal(got, []string{"branch:"}) {
		t.Errorf("deploy b: %v, want [branch:]", got)
	}
	if got := suggestInserts(suggestFor(t, x, x.Owner, "is:o")); !slices.Equal(got, []string{"is:open"}) {
		t.Errorf("is:o: %v, want [is:open]", got)
	}
	if got := suggestInserts(suggestFor(t, x, x.Owner, "-is:")); !slices.Equal(got, []string{"-is:open", "-is:closed", "-is:done"}) {
		t.Errorf("-is: %v: a negated qualifier keeps its minus", got)
	}
	// A value that is typed in full is not offered again, and a quoted word is text.
	if got := suggestInserts(suggestFor(t, x, x.Owner, "is:open")); len(got) != 0 {
		t.Errorf("is:open: %v", got)
	}
	if got := suggestInserts(suggestFor(t, x, x.Owner, `"is:o`)); len(got) != 0 {
		t.Errorf(`"is:o: %v`, got)
	}
	// After a space the next key is offered.
	if got := suggestInserts(suggestFor(t, x, x.Owner, "is:open ")); len(got) == 0 || got[0] != "is:" {
		t.Errorf("after a space: %v", got)
	}
	if got := suggestInserts(suggestFor(t, x, x.Owner, "kind:p")); !slices.Equal(got, []string{"kind:pain"}) {
		t.Errorf("kind:p: %v", got)
	}
	if got := suggestInserts(suggestFor(t, x, x.Owner, "author:")); !slices.Equal(got, []string{"author:@me"}) {
		t.Errorf("author: %v", got)
	}
}

// Everything that is offered is something the search understands, and a guest
// is not offered what a guest may not use.
func TestSuggestOffersOnlyWhatTheSearchLanguageKnows(t *testing.T) {
	x := seedExact(t)
	known := map[string]bool{}
	for _, k := range query.KnownKeys() {
		known[k] = true
	}
	for _, k := range suggestKeys {
		if !known[k] {
			t.Errorf("%q is offered but the search does not know it", k)
		}
	}
	body := suggestFor(t, x, x.Owner, "x ")
	owner := suggestInserts(body)
	if len(owner) != suggestValuesMax {
		t.Errorf("a new word is offered %d keys, want %d: %v", len(owner), suggestValuesMax, owner)
	}
	for _, k := range []string{"branch:", "package:", "for:"} {
		if got := suggestInserts(suggestFor(t, x, x.Guest, k[:len(k)-1])); slices.Contains(got, k) {
			t.Errorf("a guest is offered %s", k)
		}
	}
	// The kinds a guest does not search are not offered as kind: values.
	if got := suggestInserts(suggestFor(t, x, x.Guest, "kind:")); slices.Contains(got, "kind:pain") || slices.Contains(got, "kind:task") || !slices.Contains(got, "kind:request") {
		t.Errorf("a guest is offered the kinds %v", got)
	}
	if got := suggestInserts(suggestFor(t, x, x.Owner, "kind:")); !slices.Contains(got, "kind:pain") {
		t.Errorf("the owner is not offered kind:pain: %v", got)
	}
}

// The branches, packages and labels of the page's repository come with the rights
// of the viewer; without a repository there are none to offer.
func TestSuggestOffersTheValuesOfTheRepositoryOfThePage(t *testing.T) {
	x := seedExact(t)
	project := "project=" + url.QueryEscape(shellProject)
	if got := suggestInserts(suggestFor(t, x, x.Owner, "branch:", project)); !slices.Contains(got, "branch:main") {
		t.Errorf("branch: offers %v, want the default branch", got)
	}
	if got := suggestInserts(suggestFor(t, x, x.Owner, "branch:")); len(got) != 0 {
		t.Errorf("branch: without a repository offers %v", got)
	}
	// A repository the viewer may not read counts as not given.
	if got := suggestInserts(suggestFor(t, x, x.Member, "branch:", "project="+url.QueryEscape(shellHiddenProject))); len(got) != 0 {
		t.Errorf("the member is offered the branches of a hidden repository: %v", got)
	}
}

func TestSuggestIsEmptyWithoutAnythingTyped(t *testing.T) {
	x := seedExact(t)
	if body := suggestFor(t, x, x.Owner, "  "); strings.TrimSpace(body) != "" {
		t.Errorf("an empty line suggests:\n%s", body)
	}
	// One character is no search, but it can be the start of a key.
	body := suggestFor(t, x, x.Owner, "i")
	if strings.Contains(body, "sg-all") || !strings.Contains(body, `data-insert="is:"`) {
		t.Errorf("one character:\n%s", body)
	}
}

// The field of the header and of the search page carry the address of the list,
// the header the repository of the page.
func TestSearchFieldsAskForSuggestions(t *testing.T) {
	x := seedExact(t)
	_, page := fetchPage(t, x.Owner, x.Base+"/ui/search?q=deploy")
	if strings.Count(page, `data-suggest="/ui/search/suggest"`) != 2 {
		t.Errorf("the header and the search page do not both ask for suggestions:\n%.400s", page)
	}
	_, repo := fetchPage(t, x.Owner, x.Base+"/ui/x/shell")
	if !strings.Contains(repo, `data-suggest-project="`+shellProject+`"`) {
		t.Errorf("the header on a repository page does not name the repository")
	}
}
