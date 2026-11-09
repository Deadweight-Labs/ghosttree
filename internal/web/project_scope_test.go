package web

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// remembering gibt dem eingeloggten Client ein Cookie-Glas, das Set-Cookie der
// Antworten behält wie ein Browser (der Test-Client hält sonst nur die Sitzung).
func remembering(t *testing.T, base string, c *http.Client) *http.Client {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(u, c.Jar.Cookies(u))
	return &http.Client{Jar: jar, CheckRedirect: c.CheckRedirect}
}

func projectCookieOf(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == projectCookie {
			return c
		}
	}
	return nil
}

// selectedOption is the project a page is narrowed to, as the header names it
// behind the page title; "" means all projects.
func selectedOption(page string) string {
	m := regexp.MustCompile(`<a class="scope"[^>]*title="([^"]*)"`).FindStringSubmatch(page)
	if m == nil {
		return ""
	}
	return m[1]
}

func TestExplicitProjectIsKeptInAnSessionCookie(t *testing.T) {
	base, owner, _, _ := shellWeb(t)
	resp, err := owner.Get(base + "/ui/knowledge?project=" + shellProject)
	if err != nil {
		t.Fatal(err)
	}
	c := projectCookieOf(resp)
	if c == nil || c.Value != shellProject {
		t.Fatalf("project cookie=%+v", c)
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge != 0 {
		t.Errorf("cookie attributes differ from the UI cookies: %+v", c)
	}
}

func TestSelectionSurvivesNavigation(t *testing.T) {
	base, owner, _, _ := shellWeb(t)
	c := remembering(t, base, owner)
	fetchPage(t, c, base+"/ui/knowledge?project="+shellProject)
	for _, path := range []string{"/ui/sessions", "/ui/agents", "/ui/coord", "/ui/knowledge"} {
		_, page := fetchPage(t, c, base+path)
		if got := selectedOption(page); got != shellProject {
			t.Errorf("GET %s shows %q selected, want %q", path, got, shellProject)
		}
	}
}

func TestExplicitProjectOverridesTheCookieAndAllProjectsClearsIt(t *testing.T) {
	base, owner, _, _ := shellWeb(t)
	c := remembering(t, base, owner)
	fetchPage(t, c, base+"/ui/knowledge?project="+shellProject)
	fetchPage(t, c, base+"/ui/knowledge?project="+shellHiddenProject)
	if _, page := fetchPage(t, c, base+"/ui/knowledge"); selectedOption(page) != shellHiddenProject {
		t.Errorf("explicit project did not replace the stored one: %q", selectedOption(page))
	}
	resp, err := c.Get(base + "/ui/knowledge?project=")
	if err != nil {
		t.Fatal(err)
	}
	if cookie := projectCookieOf(resp); cookie == nil || cookie.MaxAge >= 0 {
		t.Errorf("All projects must clear the cookie, got %+v", cookie)
	}
	if _, page := fetchPage(t, c, base+"/ui/knowledge"); selectedOption(page) != "" {
		t.Errorf("after All projects the selection is %q", selectedOption(page))
	}
}

func TestProjectWithoutAccessIsIgnoredAndNeverLeaked(t *testing.T) {
	base, _, member, _ := shellWeb(t)
	resp, err := member.Get(base + "/ui/knowledge?project=" + shellHiddenProject)
	if err != nil {
		t.Fatal(err)
	}
	if projectCookieOf(resp) != nil {
		t.Error("a project without a role was stored")
	}
	if page := body(t, resp); strings.Contains(page, shellHiddenProject) {
		t.Error("the page names a project the member has no role in")
	}
	// A cookie that outlived the access is ignored as well.
	req, _ := http.NewRequest(http.MethodGet, base+"/ui/knowledge", nil)
	req.AddCookie(&http.Cookie{Name: projectCookie, Value: shellHiddenProject})
	u, _ := url.Parse(base)
	for _, sc := range member.Jar.Cookies(u) {
		req.AddCookie(sc)
	}
	resp, err = (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	page := body(t, resp)
	if strings.Contains(page, shellHiddenProject) {
		t.Error("a stale cookie leaked a project name")
	}
	if selectedOption(page) != "" {
		t.Errorf("stale cookie did not fall back to all projects: %q", selectedOption(page))
	}
}

func TestRoomsListFollowsTheSelectedProject(t *testing.T) {
	e := shellWebAll(t)
	for _, remote := range []string{shellProject, shellHiddenProject} {
		if err := e.St.EnsureCoordRoom(store.CoordRoom{Key: "project:" + remote, Kind: store.RoomProject}); err != nil {
			t.Fatal(err)
		}
	}
	c := remembering(t, e.Base, e.Owner)
	shown, other := url.QueryEscape("project:"+shellProject), url.QueryEscape("project:"+shellHiddenProject)
	_, page := fetchPage(t, c, e.Base+"/ui/coord?project=")
	if !strings.Contains(page, shown) || !strings.Contains(page, other) {
		t.Fatalf("All projects must list both project rooms")
	}
	_, page = fetchPage(t, c, e.Base+"/ui/coord?project="+shellProject)
	if !strings.Contains(page, shown) || strings.Contains(page, other) {
		t.Error("the rooms list does not narrow to the selected project")
	}
	// The open room stays reachable whatever the selection is.
	_, page = fetchPage(t, c, e.Base+"/ui/coord?room="+other)
	if !strings.Contains(page, other) {
		t.Error("the open room vanished from the list")
	}
}

func TestSessionsAndKnowledgeFollowTheSelectedProject(t *testing.T) {
	e := seedSessions(t)
	ovKnowledge(t, e.St, shellProject, "Shell lesson", "trusted")
	ovKnowledge(t, e.St, shellHiddenProject, "Hidden lesson", "trusted")
	c := remembering(t, e.Base, e.Owner)
	fetchPage(t, c, e.Base+"/ui/knowledge?project="+shellProject)
	_, page := fetchPage(t, c, e.Base+"/ui/knowledge")
	if !strings.Contains(page, "Shell lesson") || strings.Contains(page, "Hidden lesson") {
		t.Error("knowledge ignores the stored selection")
	}
	_, page = fetchPage(t, c, e.Base+"/ui/sessions")
	if !strings.Contains(page, e.pid["anna-project"]) || strings.Contains(page, e.pid["hidden-project"]) {
		t.Error("sessions ignore the stored selection")
	}
	fetchPage(t, c, e.Base+"/ui/knowledge?project=")
	_, page = fetchPage(t, c, e.Base+"/ui/sessions")
	if !strings.Contains(page, e.pid["hidden-project"]) {
		t.Error("All projects does not widen the sessions list")
	}
}

func TestAccountPagesDoNotOfferTheProjectSelector(t *testing.T) {
	base, owner, member, _ := shellWeb(t)
	for _, c := range []*http.Client{owner, member} {
		for _, path := range []string{"/ui/orgs", "/ui/account/tokens", "/ui/device", "/ui/profile"} {
			status, page := fetchPage(t, c, base+path)
			if status != http.StatusOK {
				t.Errorf("GET %s status=%d", path, status)
				continue
			}
			if selectedOption(page) != "" {
				t.Errorf("GET %s names a project scope that filters nothing", path)
			}
		}
	}
	_, page := fetchPage(t, owner, base+"/ui/overview?project="+shellProject)
	if selectedOption(page) != shellProject {
		t.Errorf("the header does not name the project the page is narrowed to: %q", selectedOption(page))
	}
}

func TestLogoutForgetsTheSelection(t *testing.T) {
	base, owner, _, _ := shellWeb(t)
	c := remembering(t, base, owner)
	fetchPage(t, c, base+"/ui/knowledge?project="+shellProject)
	form := url.Values{"csrf_token": {renderedCSRFToken(t, c, base+"/ui/overview")}}
	resp := sameOriginPostForm(t, c, base+"/ui/logout", form)
	if cookie := projectCookieOf(resp); cookie == nil || cookie.MaxAge >= 0 {
		t.Errorf("logout must clear the project cookie, got %+v", cookie)
	}
}
