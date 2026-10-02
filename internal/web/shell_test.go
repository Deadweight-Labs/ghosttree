package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const (
	shellProject       = "github.com/x/shell"
	shellHiddenProject = "github.com/x/hidden"
)

// shellEnv ist eine Organisation mit Owner (alice), Member (anna), Guest
// (gina), Lead (lars) und Member mit Reviewer-Flag (rita). Das Projekt
// "hidden" hat nur alice.
type shellEnv struct {
	Base                                 string
	St                                   *store.Store
	Owner, Member, Guest, Lead, Reviewer *http.Client
}

func shellWebAll(t *testing.T) shellEnv {
	t.Helper()
	srv, st, _ := testWeb(t)
	for _, n := range []string{"anna", "gina", "lars", "rita"} {
		if _, err := st.AddPerson(n); err != nil {
			t.Fatal(err)
		}
	}
	org, err := st.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, remote := range []string{shellProject, shellHiddenProject} {
		if _, err := st.ClaimProject("person:1", remote, "alpha"); err != nil {
			t.Fatal(err)
		}
	}
	for _, account := range []string{"person:2", "person:3", "person:4", "person:5"} {
		code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
		if _, err := st.AcceptInvitation(account, code); err != nil {
			t.Fatal(err)
		}
	}
	for account, role := range map[string]string{"person:2": store.RoleMember, "person:3": store.RoleGuest, "person:4": store.RoleLead, "person:5": store.RoleMember} {
		if err := st.SetProjectRole("person:1", shellProject, account, role, account == "person:5", store.RoleViaCLI); err != nil {
			t.Fatal(err)
		}
	}
	return shellEnv{Base: srv.URL, St: st,
		Owner: loginInteractive(t, srv, st, "alice"), Member: loginInteractive(t, srv, st, "anna"), Guest: loginInteractive(t, srv, st, "gina"),
		Lead: loginInteractive(t, srv, st, "lars"), Reviewer: loginInteractive(t, srv, st, "rita")}
}

func shellWeb(t *testing.T) (base string, owner, member, guest *http.Client) {
	t.Helper()
	e := shellWebAll(t)
	return e.Base, e.Owner, e.Member, e.Guest
}

func fetchPage(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	status := resp.StatusCode
	return status, body(t, resp)
}

var navKeyRE = regexp.MustCompile(`data-nav="([a-z-]+)"`)

func navKeys(page string) map[string]bool {
	out := map[string]bool{}
	for _, m := range navKeyRE.FindAllStringSubmatch(page, -1) {
		out[m[1]] = true
	}
	return out
}

func TestShellNavigationFollowsTheRole(t *testing.T) {
	base, owner, member, guest := shellWeb(t)
	workspace := []string{"overview", "agents", "sessions", "rooms", "knowledge", "requests"}
	for _, tc := range []struct {
		name   string
		client *http.Client
		want   []string
		absent []string
	}{
		{"owner", owner, append(workspace, "admin-org", "admin-devices"), []string{"account-devices"}},
		{"member", member, append(workspace, "account-devices"), []string{"admin-org", "admin-devices"}},
		{"guest", guest, []string{"overview", "sessions", "knowledge", "requests"}, []string{"agents", "rooms", "admin-org", "admin-devices", "account-devices"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, page := fetchPage(t, tc.client, base+"/ui/overview")
			if status != http.StatusOK {
				t.Fatalf("status=%d", status)
			}
			keys := navKeys(page)
			for _, k := range tc.want {
				if !keys[k] {
					t.Errorf("navigation lacks %q: %v", k, keys)
				}
			}
			for _, k := range tc.absent {
				if keys[k] {
					t.Errorf("navigation shows %q to a %s", k, tc.name)
				}
			}
			if !strings.Contains(page, `data-nav="overview" aria-current="page"`) {
				t.Error("overview is not marked current on the overview page")
			}
		})
	}
}

func TestShellNavigationRevealsNothingToAGuest(t *testing.T) {
	base, _, _, guest := shellWeb(t)
	for _, path := range []string{"/ui/overview", "/ui/requests", "/ui/knowledge"} {
		_, page := fetchPage(t, guest, base+path)
		for _, leak := range []string{
			`href="/ui/coord"`, `href="/ui/account/tokens"`,
			`href="/ui/review"`, `href="/ui/context"`,
			shellHiddenProject, // Projekt ohne Rolle des Gastes
		} {
			if strings.Contains(page, leak) {
				t.Errorf("GET %s shows a guest %q", path, leak)
			}
		}
		if strings.Contains(page, "nav-count") || regexp.MustCompile(`data-nav="[a-z-]+"[^>]*>[^<]*\d`).MatchString(page) {
			t.Errorf("GET %s navigation carries a counter", path)
		}
		if !strings.Contains(page, shellProject) {
			t.Errorf("GET %s lacks the guest's own project in the selector", path)
		}
	}
}

func TestShellNavigationHasNoCounters(t *testing.T) {
	base, owner, _, _ := shellWeb(t)
	_, page := fetchPage(t, owner, base+"/ui/overview")
	nav := page[strings.Index(page, `<nav`):strings.Index(page, `</nav>`)]
	if regexp.MustCompile(`>\s*\d+\s*<`).MatchString(nav) {
		t.Fatalf("navigation shows a number: %s", nav)
	}
}

func TestShellProjectSelectorListsOnlyProjectsWithARole(t *testing.T) {
	base, owner, member, _ := shellWeb(t)
	_, page := fetchPage(t, owner, base+"/ui/overview")
	if !strings.Contains(page, shellProject) || !strings.Contains(page, shellHiddenProject) {
		t.Error("owner selector must list both projects")
	}
	_, page = fetchPage(t, member, base+"/ui/overview")
	if !strings.Contains(page, shellProject) || strings.Contains(page, shellHiddenProject) {
		t.Error("member selector must list only the project with a role")
	}
}

func TestShellProjectSelectionIsKept(t *testing.T) {
	base, owner, _, _ := shellWeb(t)
	_, page := fetchPage(t, owner, base+"/ui/knowledge?project="+shellProject)
	if !regexp.MustCompile(`<option value="` + regexp.QuoteMeta(shellProject) + `" selected`).MatchString(page) {
		t.Error("selector does not show the chosen project")
	}
}

func TestShellHeaderHasSearchAndAccountMenu(t *testing.T) {
	base, owner, _, guest := shellWeb(t)
	for name, c := range map[string]*http.Client{"owner": owner, "guest": guest} {
		_, page := fetchPage(t, c, base+"/ui/overview")
		for _, want := range []string{
			`role="search"`, `action="/ui/knowledge"`, `name="q"`,
			`method="post" action="/ui/logout"`, `name="csrf_token"`,
			`data-account="join"`, // Join with a code im Kontomenü
		} {
			if !strings.Contains(page, want) {
				t.Errorf("%s header lacks %q", name, want)
			}
		}
	}
	_, page := fetchPage(t, owner, base+"/ui/overview")
	if !strings.Contains(page, "alice") {
		t.Error("account menu lacks the account name")
	}
}

func TestShellRoleLabels(t *testing.T) {
	base, owner, member, guest := shellWeb(t)
	for label, c := range map[string]*http.Client{"Owner": owner, "Member": member, "Guest": guest} {
		_, page := fetchPage(t, c, base+"/ui/overview")
		if !strings.Contains(page, `class="account-role">`+label+`<`) {
			t.Errorf("account menu lacks role %q", label)
		}
	}
}

func TestEveryBrowserPageUsesTheShell(t *testing.T) {
	base, owner, _, _ := shellWeb(t)
	for _, path := range []string{
		"/ui/overview", "/ui/requests", "/ui/knowledge", "/ui/review", "/ui/sessions",
		"/ui/coord", "/ui/context", "/ui/orgs", "/ui/account/tokens", "/ui/device",
	} {
		status, page := fetchPage(t, owner, base+path)
		if status != http.StatusOK {
			t.Errorf("GET %s status=%d", path, status)
			continue
		}
		if !strings.Contains(page, `class="shell"`) || !strings.Contains(page, `class="shell-nav"`) {
			t.Errorf("GET %s is not inside the shell", path)
		}
		if strings.Count(page, "<main") != 1 || strings.Count(page, "</main>") != 1 {
			t.Errorf("GET %s has unbalanced main elements", path)
		}
		if strings.Count(page, "<div") != strings.Count(page, "</div>") {
			t.Errorf("GET %s has unbalanced div elements", path)
		}
	}
}

func TestShellMarksTheCurrentSection(t *testing.T) {
	base, owner, _, _ := shellWeb(t)
	for path, key := range map[string]string{
		"/ui/overview": "overview", "/ui/sessions": "sessions", "/ui/coord": "rooms",
		"/ui/knowledge": "knowledge", "/ui/review": "knowledge", "/ui/context": "knowledge",
		"/ui/requests": "requests", "/ui/orgs": "admin-org", "/ui/account/tokens": "admin-devices",
	} {
		_, page := fetchPage(t, owner, base+path)
		if !strings.Contains(page, `data-nav="`+key+`" aria-current="page"`) {
			t.Errorf("GET %s does not mark %s current", path, key)
		}
		if n := len(regexp.MustCompile(`data-nav="[a-z-]+" aria-current="page"`).FindAllString(page, -1)); n != 1 {
			t.Errorf("GET %s marks %d navigation items current", path, n)
		}
	}
}

func TestKnowledgeReviewAndContextAreForReviewersLeadsAndOwnersOnly(t *testing.T) {
	e := shellWebAll(t)
	for name, c := range map[string]*http.Client{"owner": e.Owner, "lead": e.Lead, "reviewer": e.Reviewer} {
		_, page := fetchPage(t, c, e.Base+"/ui/review")
		if !navKeys(page)["knowledge-review"] || !navKeys(page)["knowledge-context"] {
			t.Errorf("%s lacks review and agent context entries", name)
		}
	}
	for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest} {
		_, page := fetchPage(t, c, e.Base+"/ui/knowledge")
		for _, k := range []string{"knowledge-review", "knowledge-context", "knowledge-search"} {
			if navKeys(page)[k] {
				t.Errorf("%s sees %s", name, k)
			}
		}
	}
}

func TestAllProjectsIsOfferedToOwnersOnly(t *testing.T) {
	e := shellWebAll(t)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/overview")
	if !strings.Contains(page, `<option value="" selected>All projects`) {
		t.Error("owner lacks All projects")
	}
	for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest, "lead": e.Lead} {
		_, page := fetchPage(t, c, e.Base+"/ui/overview")
		if strings.Contains(page, "All projects") {
			t.Errorf("%s is offered All projects", name)
		}
		if strings.Contains(page, `value="" disabled`) || !regexp.MustCompile(`<option value="`+regexp.QuoteMeta(shellProject)+`" selected`).MatchString(page) {
			t.Errorf("%s selector does not preselect the first project", name)
		}
	}
}

func TestEveryShellPageHasLogoutWithCSRFAndMarksDetailPages(t *testing.T) {
	e := shellWebAll(t)
	detail, err := e.St.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Nav", Scope: scope.Axes{Project: shellProject}}, Criteria: []string{"c"}})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := e.St.UpsertSession(store.Session{Harness: "codex", ExternalID: "nav", Scope: scope.Axes{Project: shellProject}})
	if err != nil {
		t.Fatal(err)
	}
	for path, key := range map[string]string{
		"/ui/overview": "overview", "/ui/requests": "requests", "/ui/requests/" + strconv.FormatInt(detail.Request.ID, 10): "requests",
		"/ui/sessions": "sessions", "/ui/sessions/" + strconv.FormatInt(sid, 10): "sessions",
		"/ui/knowledge": "knowledge", "/ui/coord": "rooms", "/ui/orgs": "admin-org", "/ui/account/tokens": "admin-devices",
	} {
		_, page := fetchPage(t, e.Owner, e.Base+path)
		if !strings.Contains(page, `data-nav="`+key+`" aria-current="page"`) {
			t.Errorf("GET %s does not mark %s current", path, key)
		}
		if !regexp.MustCompile(`method="post" action="/ui/logout"><input type="hidden" name="csrf_token" value="[0-9a-f]{16,}"`).MatchString(page) {
			t.Errorf("GET %s lacks the logout form with a CSRF token", path)
		}
	}
}

func TestShellFooterShowsThePublicHostNotTheRequestHost(t *testing.T) {
	st := mustStore(t)
	if _, err := st.AddPerson("alice"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st, WithPublicURL("https://gt.example.test")))
	defer srv.Close()
	c := loginInteractive(t, srv, st, "alice")
	_, page := fetchPage(t, c, srv.URL+"/ui/overview")
	if !strings.Contains(page, `<p class="shell-host">gt.example.test</p>`) {
		t.Errorf("footer does not show the public host: %s", page)
	}
	srv2, _, tok := testWeb(t)
	_, page = fetchPage(t, login(t, srv2, tok), srv2.URL+"/ui/overview")
	if !strings.Contains(page, `<p class="shell-host">`+strings.TrimPrefix(srv2.URL, "http://")+`</p>`) {
		t.Error("without a public URL the footer shows the request host")
	}
}

func TestShellNavigationCollapsesOnNarrowScreens(t *testing.T) {
	e := shellWebAll(t)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/overview")
	if !strings.Contains(page, `data-shell-toggle`) || !strings.Contains(page, `aria-expanded="false"`) || !strings.Contains(page, `class="shell-collapsible"`) {
		t.Error("shell lacks the navigation toggle")
	}
	resp, err := http.Get(e.Base + "/static/shell.css")
	if err != nil {
		t.Fatal(err)
	}
	css := body(t, resp)
	if !strings.Contains(css, "@media (max-width: 900px)") || !strings.Contains(css, ".js .shell:not([data-open]) .shell-collapsible") {
		t.Error("shell.css lacks the narrow-screen rules")
	}
}

func TestFontsAreCachedLongOtherStaticFilesAreNot(t *testing.T) {
	srv, _, _ := testWeb(t)
	resp, err := http.Get(srv.URL + "/static/fonts/plex-1.1-2.5/IBMPlexSans-Regular-Latin1.woff2")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "max-age=31536000") || !strings.Contains(cc, "immutable") {
		t.Errorf("font Cache-Control=%q", cc)
	}
	resp, err = http.Get(srv.URL + "/static/shell.css")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Error("stylesheets must not be immutable")
	}
}

func TestRootRedirectsToTheApp(t *testing.T) {
	srv, _, _ := testWeb(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/" {
		t.Fatalf("GET / = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, err = client.Get(srv.URL + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /nope = %d, want 404", resp.StatusCode)
	}
}

func TestFaviconIsServed(t *testing.T) {
	srv, _, _ := testWeb(t)
	for _, path := range []string{"/favicon.ico", "/static/favicon.svg"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		ct := resp.Header.Get("Content-Type")
		text := body(t, resp)
		if resp.StatusCode != http.StatusOK || ct != "image/svg+xml" || !strings.Contains(text, "<svg") {
			t.Errorf("GET %s = %d %q", path, resp.StatusCode, ct)
		}
	}
}

func TestRoomsPathRedirectsToCoordination(t *testing.T) {
	base, owner, _, _ := shellWeb(t)
	owner.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := owner.Get(base + "/ui/rooms?room=x")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/coord?room=x" {
		t.Fatalf("GET /ui/rooms = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestShellPagesLoadSelfHostedFonts(t *testing.T) {
	base, owner, _, _ := shellWeb(t)
	_, page := fetchPage(t, owner, base+"/ui/requests")
	if !strings.Contains(page, `href="/static/tokens.css"`) || !strings.Contains(page, `rel="icon"`) {
		t.Error("shell lacks tokens stylesheet or favicon link")
	}
	if strings.Contains(page, "http://") || strings.Contains(page, "https://") {
		t.Error("shell references an external host")
	}
	srv := httptest.NewServer(New(mustStore(t)))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/static/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	css := body(t, resp)
	for _, want := range []string{"--color-accent: #2f6b4a", "--color-text-muted: #58645d", "--font-sans", "--font-mono", "--radius-", "--space-", "--focus-ring", "IBM Plex Sans", "IBM Plex Mono", "/static/fonts/plex-1.1-2.5/IBMPlexSans-Regular-Latin1.woff2"} {
		if !strings.Contains(css, want) {
			t.Errorf("tokens.css lacks %q", want)
		}
	}
	for _, f := range []string{"IBMPlexSans-Regular-Latin1.woff2", "IBMPlexSans-Medium-Latin1.woff2", "IBMPlexSans-SemiBold-Latin1.woff2", "IBMPlexMono-Regular-Latin1.woff2", "IBMPlexMono-SemiBold-Latin1.woff2", "OFL-IBM-Plex-Sans.txt", "OFL-IBM-Plex-Mono.txt"} {
		resp, err := http.Get(srv.URL + "/static/fonts/plex-1.1-2.5/" + f)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("font file %s = %d", f, resp.StatusCode)
		}
	}
}

func mustStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}
