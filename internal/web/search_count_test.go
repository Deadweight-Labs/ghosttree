package web

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// needles makes requests "Needle NNN": before features, then bugs bugs, then
// after features. Requests are listed newest first, so the bugs lie behind the
// newest `after` features and a read that stops early has not seen all of them.
func needles(t *testing.T, e shellEnv, before, bugs, after int) {
	t.Helper()
	n := 0
	for _, step := range []struct {
		count int
		typ   string
	}{{before, "feature"}, {bugs, "bug"}, {after, "feature"}} {
		for i := 0; i < step.count; i++ {
			rqMake(t, e.St, rqSpec{Title: fmt.Sprintf("Needle %03d", n), Type: step.typ})
			n++
		}
	}
}

func counterOf(page, label string) string {
	m := regexp.MustCompile(`<span class="nm">` + regexp.QuoteMeta(label) + `</span><span class="counter">([^<]*)</span>`).FindStringSubmatch(page)
	if m == nil {
		return ""
	}
	return html.UnescapeString(m[1])
}

// A qualifier drops hits after the read, so a read that was cut at its limit
// proves only a lower bound: three bugs among the newest 200 candidates are "3+",
// not "3", while a search that read everything states its number as it is.
func TestSearchCountOfAQualifiedSearchSaysPlusWhenTheReadWasCut(t *testing.T) {
	e := ovEnv(t)
	needles(t, e, 100, 3, 102)
	for _, target := range []string{"/ui/search?q=Needle+type:bug", "/ui/search?q=Needle+type:bug&kind=requests"} {
		_, page := fetchPage(t, e.Member, e.Base+target)
		if got := counterOf(page, "Requests"); got != "3+" {
			t.Errorf("%s: the Requests facet reads %q, want 3+", target, got)
		}
		if got := counterOf(page, "All"); got != "3+" {
			t.Errorf("%s: the All facet reads %q, want 3+", target, got)
		}
	}
	_, kind := fetchPage(t, e.Member, e.Base+"/ui/search?q=Needle+type:bug&kind=requests")
	if !strings.Contains(kind, "More than 3 hits") || strings.Count(kind, `class="hit"`) != 3 {
		t.Errorf("the list does not say that there are more than 3 hits:\n%s", kind)
	}
}

func TestSearchCountOfAQualifiedSearchReadInFullHasNoPlus(t *testing.T) {
	e := ovEnv(t)
	needles(t, e, 3, 2, 1)
	_, page := fetchPage(t, e.Member, e.Base+"/ui/search?q=Needle+type:bug&kind=requests")
	if got := counterOf(page, "Requests"); got != "2" || !strings.Contains(page, "2 hits") || strings.Contains(page, "More than") {
		t.Errorf("a search that read everything reads %q:\n%s", got, page)
	}
}

// The lines of a quick filter overlap: each reads as far as the count needs and
// the union is counted, with a plus when a line was cut.
func TestSearchCountOfAQuickFilterWithSeveralLinesIsTheUnion(t *testing.T) {
	e := ovEnv(t)
	for i := 0; i < 12; i++ {
		rqMake(t, e.St, rqSpec{Title: fmt.Sprintf("Needle %02d", i)})
	}
	for i := 0; i < 9; i++ {
		rqMake(t, e.St, rqSpec{Title: fmt.Sprintf("Haystack %02d", i)})
	}
	pa := e.St.Access(store.Principal{ID: "person:1", Label: "alice"})
	if _, err := pa.SavePreset(shellProject, "user", "both", "", []string{"Needle", "Haystack"}); err != nil {
		t.Fatal(err)
	}
	base := e.Base + "/ui/search?preset=both&project=" + url.QueryEscape(shellProject)
	_, page := fetchPage(t, e.Owner, base)
	if got := counterOf(page, "Requests"); got != "21" || counterOf(page, "All") != "21" {
		t.Errorf("the two lines have 21 hits together, the page reads %q (All %q)", got, counterOf(page, "All"))
	}
	if !strings.Contains(page, "21 hits") || strings.Count(page, `class="hit"`) != searchPerGroup || !strings.Contains(page, "morelink") {
		t.Errorf("the page does not state 21, show %d hits and lead on:\n%s", searchPerGroup, page)
	}
	_, kind := fetchPage(t, e.Owner, base+"&kind=requests")
	if strings.Count(kind, `class="hit"`) != searchKindPage || !strings.Contains(kind, "&amp;n=40") {
		t.Errorf("the narrowed page of the filter shows %d hits, want %d and a step to 40", strings.Count(kind, `class="hit"`), searchKindPage)
	}
}

func TestSearchCountOfAQuickFilterWithACutLineSaysPlus(t *testing.T) {
	e := ovEnv(t)
	needles(t, e, 100, 3, 102)
	pa := e.St.Access(store.Principal{ID: "person:1", Label: "alice"})
	if _, err := pa.SavePreset(shellProject, "user", "cut", "", []string{"Needle", "Needle type:bug"}); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/search?preset=cut&project="+url.QueryEscape(shellProject))
	if got := counterOf(page, "Requests"); got != "100+" {
		t.Errorf("the first line has 205 hits, the page reads %q, want 100+", got)
	}
}

// "More hits" never points at the page it is on: the last step has no link of
// its own, and leads to the issue list only when there is exactly one repository
// that list could belong to.
func TestSearchMoreHitsAtTheLastStepLeadsToTheIssueListOrNowhere(t *testing.T) {
	e := searchEnv(t)
	needles(t, e, 100, 3, 102)
	project := url.QueryEscape(shellProject)
	more := regexp.MustCompile(`<a class="row morelink" href="([^"]*)"`)

	// Narrowed to one repository: the list of that repository, every state.
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/search?q=Needle&kind=requests&n=100&project="+project)
	if n := strings.Count(page, `class="hit"`); n != searchCountCap {
		t.Errorf("the last step shows %d hits, want %d", n, searchCountCap)
	}
	if !strings.Contains(page, "206 hits") {
		t.Errorf("the exact count of requests is lost at the last step")
	}
	if m := more.FindStringSubmatch(page); m == nil || html.UnescapeString(m[1]) != "/ui/x/shell/issues?q=Needle+is%3Aall" {
		t.Errorf("the link of the last step is %v, want the issue list of the repository", m)
	}
	// Two repositories and none chosen: no list holds the whole search, no link.
	_, page = fetchPage(t, e.Owner, e.Base+"/ui/search?q=Needle&kind=requests&n=100")
	if m := more.FindStringSubmatch(page); m != nil {
		t.Errorf("a search of two repositories leads to %q at the last step", m[1])
	}
	// A step before the last still goes on with n=.
	_, page = fetchPage(t, e.Owner, e.Base+"/ui/search?q=Needle&kind=requests&n=80")
	if m := more.FindStringSubmatch(page); m == nil || !strings.Contains(m[1], "&amp;n=100") {
		t.Errorf("the step after 80 is %v, want n=100", m)
	}
	// Whoever sees one repository only gets its list without choosing it.
	_, page = fetchPage(t, e.Member, e.Base+"/ui/search?q=Needle&kind=requests&n=100")
	if m := more.FindStringSubmatch(page); m == nil || !strings.Contains(m[1], "/issues?q=Needle") {
		t.Errorf("the member of one repository gets %v at the last step", m)
	}
	// A line that names a state keeps it.
	if got := (&app{}).issueListHref(nil, searchLine{project: shellProject}, "Needle is:open", ""); !strings.Contains(got, "is%3Aopen") || strings.Contains(got, "is%3Aall") {
		t.Errorf("a line with is:open links to %q", got)
	}
}
