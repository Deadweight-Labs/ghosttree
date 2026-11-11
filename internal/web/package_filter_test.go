package web

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

func TestParsePackageTermReadsANumberAndThePKGForm(t *testing.T) {
	for val, want := range map[string]int64{"7": 7, "PKG-7": 7, "pkg-12": 12} {
		if n, ok := parsePackageTerm(val); !ok || n != want {
			t.Errorf("%q = %d %v, want %d", val, n, ok, want)
		}
	}
	for _, val := range []string{"", "0", "-3", "x", "PKG-", "pkg-x", "7a"} {
		if n, ok := parsePackageTerm(val); ok {
			t.Errorf("%q read as %d", val, n)
		}
	}
}

// pullsWithSecondPackage adds a package without a pull request that holds
// another issue, to the environment of the pull request tests, and returns its
// number (the environment already holds a package of a hidden project).
func pullsWithSecondPackage(t *testing.T) (shellEnv, string) {
	t.Helper()
	e, _ := pullsEnv(t)
	other := rqMake(t, e.St, rqSpec{Title: "Other issue", Criteria: []string{"One"}})
	d, err := e.St.CreatePackage(requestdomain.PackageInput{Project: shellProject, Branch: "feat/z", Name: "No pull request", Person: "alice",
		Members: []requestdomain.PackageMember{{RequestID: other.Request.ID, Role: requestdomain.RolePflicht}}})
	if err != nil {
		t.Fatal(err)
	}
	return e, strconv.FormatInt(d.ID, 10)
}

// REQ-599 AC-8: the work package is a filter of the lists, next to the branch.
func TestIssueListFiltersByWorkPackageAndOffersItInAMenu(t *testing.T) {
	e, two := pullsWithSecondPackage(t)
	list := func(c string) string {
		_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues?q="+url.QueryEscape(c))
		return page
	}
	first := list("package:1")
	mustContain(t, "package:1", first, "Pull request tab")
	mustNotContain(t, "package:1", first, "Other issue")
	if p := list("package:PKG-1"); !strings.Contains(p, "Pull request tab") || strings.Contains(p, "Other issue") {
		t.Error("package:PKG-1 does not read like package:1")
	}
	if p := list("package:" + two); !strings.Contains(p, "Other issue") || strings.Contains(p, "Pull request tab") {
		t.Error("package:2 shows the wrong issues")
	}
	// A package that does not exist, and the number of a closed list, narrow to nothing and say so.
	none := list("package:99")
	mustNotContain(t, "package:99", none, "Pull request tab", "Other issue", "Not supported yet")
	mustContain(t, "package:99", none, "No matches")
	// The branch and the package together keep what is in both.
	if p := list("branch:feat/x package:" + two); strings.Contains(p, "Pull request tab") || strings.Contains(p, "Other issue") {
		t.Error("branch and package together did not intersect")
	}
	// A word that is no number is reported like every other filter that is not understood.
	mustContain(t, "package:x", list("package:x"), "Not supported yet", "<code>package:x</code>")
	// The dropdown writes the word into the search text and marks the chosen package.
	mustContain(t, "menu", first, "Filter by Package", "#1 Git-native", "#"+two+" No pull request", "Any package", "package%3A"+two, `aria-current="true"`)
	// The tab counts what the package holds, not the whole project.
	if !regexp.MustCompile(`(?s)class="tab on".*?<span class="n">1</span> Open`).MatchString(first) {
		t.Errorf("the open tab does not count what the package holds:\n%s", first)
	}
}

func TestPullListFiltersByWorkPackageThroughItsPullRequest(t *testing.T) {
	e, two := pullsWithSecondPackage(t)
	list := func(q string) string {
		_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/pulls?q="+url.QueryEscape(q))
		return page
	}
	one := list("is:pr is:all package:1")
	mustContain(t, "package:1", one, "feat(web): Pull requests tab")
	mustNotContain(t, "package:1", one, "fix(store): count guests", "docs: old plan")
	// A package without a pull request, one that does not exist, and one of the
	// hidden project give the same empty list.
	for _, q := range []string{"is:pr is:all package:" + two, "is:pr is:all package:99"} {
		page := list(q)
		mustNotContain(t, q, page, "feat(web): Pull requests tab", "fix(store): count guests", "SECRET-PULL")
		mustContain(t, q, page, "No pull request matches this search.")
	}
	// Only packages with a pull request are offered.
	menu := list("is:pr")
	mustContain(t, "pull menu", menu, "Filter by Package", "#1 Git-native", "package%3A1")
	mustNotContain(t, "pull menu", menu, "#"+two+" No pull request")
}

// The package names stay hidden from a guest, and the filter tells a guest nothing a
// missing number would not.
func TestPackageFilterShowsAGuestNoHiddenNameAndNoOracle(t *testing.T) {
	e, _ := pullsWithSecondPackage(t)
	get := func(path string) string {
		_, page := fetchPage(t, e.Guest, e.Base+path)
		return page
	}
	page := get("/ui/x/shell/issues?q=" + url.QueryEscape("package:1"))
	mustNotContain(t, "guest issues", page, "Git-native", "No pull request", "feat/x", "feat/z")
	pulls := get("/ui/x/shell/pulls?q=" + url.QueryEscape("is:pr is:all package:1"))
	mustNotContain(t, "guest pulls", pulls, "Git-native", "feat/z")
	// A number of a package of the hidden project is as unknown as a missing one.
	same := func(n string) string {
		page := stripCSRF(get("/ui/x/shell/issues?q=package%3A" + n))
		return strings.NewReplacer("package%3A"+n, "Q", "package:"+n, "Q").Replace(page)
	}
	if same("99") != same("98") {
		t.Error("two numbers that name nothing are answered differently")
	}
}
