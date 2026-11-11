package web

import (
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestRepoPageShowsBreadcrumbAndTabs(t *testing.T) {
	e := ovEnv(t)
	rqMake(t, e.St, rqSpec{Title: "Visible request"})
	for name, c := range map[string]*http.Client{"owner": e.Owner, "member": e.Member, "guest": e.Guest} {
		status, page := fetchPage(t, c, e.Base+"/ui/x/shell")
		if status != http.StatusOK {
			t.Fatalf("%s: GET /ui/x/shell status=%d", name, status)
		}
		for _, want := range []string{`class="hcrumb"`, `class="org" href="/ui/x"`, `<a href="/ui/x/shell">shell</a>`, `data-tab="code" class="sel"><a href="/ui/x/shell" aria-current="page"`,
			`data-tab="issues"><a href="/ui/x/shell/issues"`, `data-tab="knowledge"><a href="/ui/x/shell/knowledge"`, `href="https://github.com/x/shell"`} {
			if !strings.Contains(page, want) {
				t.Errorf("%s: repo page lacks %q", name, want)
			}
		}
		if hasRooms := strings.Contains(page, `data-ic="rooms"`) || strings.Contains(page, `data-tab="rooms"`); hasRooms == (name == "guest") {
			t.Errorf("%s: rooms tab present=%v", name, hasRooms)
		}
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell")
	if !strings.Contains(page, `<span>Issues</span><span class="counter">1</span>`) {
		t.Error("the issues tab lacks the number of open requests")
	}
}

func TestRepoTabsRenderTheExistingPagesForThatProject(t *testing.T) {
	e := ovEnv(t)
	rqMake(t, e.St, rqSpec{Title: "Visible request"})
	rqMake(t, e.St, rqSpec{Title: "SECRET-REQ", Project: shellHiddenProject})
	for tab, current := range map[string]string{"issues": "issues", "knowledge": "knowledge", "sessions": "sessions"} {
		status, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/"+tab)
		if status != http.StatusOK {
			t.Errorf("tab %s status=%d", tab, status)
			continue
		}
		if !strings.Contains(page, `data-tab="`+current+`"`) || !strings.Contains(page, `<a href="/ui/x/shell/`+current+`" aria-current="page"`) {
			t.Errorf("tab %s is not marked current", tab)
		}
		if strings.Contains(page, `class="g-tabs"`) {
			t.Errorf("tab %s shows the old tab row under the repository tabs", tab)
		}
		if strings.Contains(page, "SECRET-REQ") {
			t.Errorf("tab %s shows a request of another project", tab)
		}
	}
	_, issues := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues?project="+url.QueryEscape(shellHiddenProject))
	if strings.Contains(issues, "SECRET-REQ") || !strings.Contains(issues, "Visible request") {
		t.Error("a ?project= in the query overrides the repository of the address")
	}
}

func TestRepoRedirectsToDetailPagesAndRooms(t *testing.T) {
	e := ovEnv(t)
	for path, want := range map[string]string{
		"/ui/x/shell/rooms": "/ui/coord?room=project%3Agithub.com%2Fx%2Fshell",
	} {
		resp, err := e.Owner.Get(e.Base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != want {
			t.Errorf("%s -> %d %q, want 302 %q", path, resp.StatusCode, resp.Header.Get("Location"), want)
		}
	}
}

func TestRepoOfAHiddenProjectAnswersLikeAnUnknownOne(t *testing.T) {
	e := ovEnv(t)
	rqMake(t, e.St, rqSpec{Title: "SECRET-REQ", Project: shellHiddenProject})
	for name, c := range map[string]*http.Client{"guest": e.Guest, "member": e.Member} {
		for _, tab := range []string{"", "/issues", "/pains", "/knowledge", "/sessions", "/rooms", "/issues/1"} {
			hs, hp := fetchPage(t, c, e.Base+"/ui/x/hidden"+tab)
			us, up := fetchPage(t, c, e.Base+"/ui/x/nothere"+tab)
			if hs != http.StatusNotFound || us != http.StatusNotFound {
				t.Errorf("%s: %q hidden=%d unknown=%d, want 404 for both", name, tab, hs, us)
			}
			if hp != up || strings.Contains(hp, "SECRET") || strings.Contains(hp, "x/hidden") || strings.Contains(hp, "/hidden") {
				t.Errorf("%s: %q hidden and unknown project answer differently: %q vs %q", name, tab, hp, up)
			}
		}
	}
	if status, _ := fetchPage(t, e.Owner, e.Base+"/ui/x/hidden"); status != http.StatusOK {
		t.Errorf("owner cannot open the hidden project: %d", status)
	}
}

func TestOrgAddressLeadsToTheTeamPageOrIs404(t *testing.T) {
	e := ovEnv(t)
	if status, page := fetchPage(t, e.Member, e.Base+"/ui/alpha"); status != http.StatusOK || !strings.Contains(page, `class="rband`) || !strings.Contains(page, "ui-org.css") {
		t.Errorf("the organisation page: %d", status)
	}
	for path, want := range map[string]int{"/ui/x": http.StatusFound, "/ui/nothere": http.StatusNotFound} {
		resp, err := e.Member.Get(e.Base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want || (want == http.StatusFound && resp.Header.Get("Location") != "/ui/orgs") {
			t.Errorf("%s -> %d %q", path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if status, _ := fetchPage(t, e.Guest, e.Base+"/ui/hidden"); status != http.StatusNotFound {
		t.Errorf("an owner segment the guest cannot see answers %d", status)
	}
}

// A fixed route always wins over the repository wildcards; an owner that is
// called like one is reached through the host form instead.
func TestFixedRoutesAreNeverShadowedByRepositories(t *testing.T) {
	e := ovEnv(t)
	for pattern := range webRoutes {
		_, path, _ := strings.Cut(pattern, " ")
		rest, ok := strings.CutPrefix(path, "/ui/")
		if !ok {
			continue
		}
		first, _, _ := strings.Cut(rest, "/")
		if first != "" && !strings.HasPrefix(first, "{") && !reservedSegment(first) {
			t.Errorf("%s: first segment %q is not reserved", pattern, first)
		}
	}
	for _, path := range []string{"/ui/orgs", "/ui/coord", "/ui/inbox", "/ui/knowledge", "/ui/profile"} {
		status, page := fetchPage(t, e.Owner, e.Base+path)
		if status != http.StatusOK || (path != "/ui/orgs" && strings.Contains(page, `class="rband`)) {
			t.Errorf("%s status=%d or taken by a repository", path, status)
		}
	}
	if got := repoHref("github.com/requests/app"); got != "/ui/github.com/requests/app" {
		t.Errorf("repoHref for an owner called like a route = %q", got)
	}
	if got := repoHref("gitlab.com/o/r"); got != "/ui/gitlab.com/o/r" {
		t.Errorf("repoHref for another host = %q", got)
	}
	if got := repoHref("github.com/x/shell"); got != "/ui/x/shell" {
		t.Errorf("repoHref = %q", got)
	}
	if status, _ := fetchPage(t, e.Owner, e.Base+"/ui/github.com/x/shell"); status != http.StatusOK {
		t.Errorf("host form status=%d", status)
	}
}

func TestRepoCodeTabListsTheTopOfTheGhostTree(t *testing.T) {
	e := ovEnv(t)
	for _, g := range []struct{ path, kind, desc string }{
		{"internal", "dir", "All server code."}, {"internal/store/a.go", "file", "Store."}, {"README.md", "file", "Product page."}, {"go.mod", "file", ""},
	} {
		if _, err := e.St.PutGhostFile(ghostFile(shellProject, g.path, g.kind, g.desc)); err != nil {
			t.Fatal(err)
		}
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/x/shell")
	for _, want := range []string{"internal/", "All server code.", "README.md", "Product page.", "go.mod", "Write description"} {
		if !strings.Contains(page, want) {
			t.Errorf("code tab lacks %q", want)
		}
	}
	if strings.Contains(page, "a.go") {
		t.Error("code tab shows a file below the top level")
	}
}

func ghostFile(project, path, kind, desc string) store.GhostFile {
	return store.GhostFile{Project: project, Path: path, Kind: kind, Description: desc}
}
