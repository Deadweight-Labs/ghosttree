package web

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const (
	leakMachine = "alices-laptop"
	leakIP      = "198.51.100.7"
)

func TestOverviewListsNoOpenDeviceLogins(t *testing.T) {
	srv, st, _ := testWeb(t)
	for _, n := range []string{"paul", "olga", "zed", "gus"} {
		if _, err := st.AddPerson(n); err != nil {
			t.Fatal(err)
		}
	}
	alpha, err := st.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, remote := range []string{shellProject, shellHiddenProject} {
		if _, err := st.ClaimProject("person:1", remote, "alpha"); err != nil {
			t.Fatal(err)
		}
	}
	code, _, _ := st.CreateInvitation("person:1", alpha.ID, "", store.OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectRole("person:1", shellProject, "person:2", store.RoleOwner, false, store.RoleViaCLI); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOrg("person:3", "Beta", "beta"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddAccount("root", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Device().Start(leakIP, leakMachine, leakIP); err != nil {
		t.Fatal(err)
	}
	clients := map[string]*http.Client{
		"first account / admin": loginInteractive(t, srv, st, "alice"),
		"project owner":         loginInteractive(t, srv, st, "paul"),
		"foreign org owner":     loginInteractive(t, srv, st, "olga"),
		"admin without roles":   loginInteractive(t, srv, st, "root"),
		"account without role":  loginInteractive(t, srv, st, "zed"),
	}
	for name, c := range clients {
		for _, path := range []string{"/ui/overview", "/ui/overview?connect=1"} {
			status, page := fetchPage(t, c, srv.URL+path)
			if status != http.StatusOK {
				t.Fatalf("%s %s = %d", name, path, status)
			}
			if strings.Contains(page, leakMachine) || strings.Contains(page, leakIP) || strings.Contains(page, "wants to connect") {
				t.Errorf("%s sees an open device login on %s", name, path)
			}
		}
	}
	// Mit einem Agenten steht die normale Übersicht da, auch ohne Maschinenname.
	ovAgent(t, st, shellProject, "claude:p", "pa", "person:2")
	for name, c := range clients {
		_, page := fetchPage(t, c, srv.URL+"/ui/overview")
		if strings.Contains(page, leakMachine) || strings.Contains(page, leakIP) {
			t.Errorf("%s sees an open device login with agents present", name)
		}
	}
	// Das Code-Feld bleibt, ohne Maschine, IP und Alter.
	_, page := fetchPage(t, clients["first account / admin"], srv.URL+"/ui/overview?connect=1")
	if !strings.Contains(page, `name="user_code"`) || !strings.Contains(page, `action="/ui/device"`) {
		t.Error("the connect view lacks the code field")
	}
}

func TestAdminWithoutRolesGetsTheOwnerSetup(t *testing.T) {
	srv, st, _ := testWeb(t)
	if _, err := st.AddAccount("root", "", true); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, loginInteractive(t, srv, st, "root"), srv.URL+"/ui/overview")
	if !strings.Contains(page, "Connect your first agent") {
		t.Errorf("an admin without roles gets no setup: %s", page)
	}
}

func TestOldestAccountWithoutAdminOrRoleGetsNoOwnerSetup(t *testing.T) {
	path := t.TempDir() + "/old.db"
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.AddPerson("alice"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	revokeAdmin(t, path, 1)
	_, page := fetchPage(t, loginInteractive(t, srv, st, "alice"), srv.URL+"/ui/overview")
	if strings.Contains(page, "Connect your first agent") || strings.Contains(page, "ctx login") {
		t.Errorf("the oldest account keeps owner treatment after losing admin: %s", page)
	}
}

// revokeAdmin nimmt einem Konto den Admin-Status am Store vorbei.
func revokeAdmin(t *testing.T, path string, id int) {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE persons SET is_admin=0 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
}

func TestGuestInOneProjectAndMemberInAnotherSeesOnlyWhatEachRoleAllows(t *testing.T) {
	e := ovEnv(t)
	if _, err := e.St.AddPerson("mia"); err != nil {
		t.Fatal(err)
	}
	account := "person:6"
	orgs, err := e.St.ListOrgs("person:1")
	if err != nil || len(orgs) == 0 {
		t.Fatal(err)
	}
	code, _, _ := e.St.CreateInvitation("person:1", orgs[0].ID, "", store.OrgMember, 0)
	if _, err := e.St.AcceptInvitation(account, code); err != nil {
		t.Fatal(err)
	}
	if err := e.St.SetProjectRole("person:1", shellProject, account, store.RoleGuest, false, store.RoleViaCLI); err != nil {
		t.Fatal(err)
	}
	if err := e.St.SetProjectRole("person:1", shellHiddenProject, account, store.RoleMember, false, store.RoleViaCLI); err != nil {
		t.Fatal(err)
	}
	ovAgent(t, e.St, shellProject, "claude:a", "AGENT-IN-A", account)
	ovAgent(t, e.St, shellHiddenProject, "claude:b", "AGENT-IN-B", account)
	c := loginInteractive(t, e.Srv, e.St, "mia")
	_, page := fetchPage(t, c, e.Base+"/ui/overview")
	if strings.Contains(page, "AGENT-IN-A") {
		t.Error("the guest role in A leaks A's agents")
	}
	if !strings.Contains(page, "AGENT-IN-B") {
		t.Error("the member role in B does not show B's agents")
	}
}

func TestOverviewPageSecurityHeaders(t *testing.T) {
	e := shellWebAll(t)
	for _, path := range []string{"/ui/overview", "/ui/requests", "/ui/coord", "/ui/orgs", "/ui/device"} {
		resp, err := e.Owner.Get(e.Base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		csp := resp.Header.Get("Content-Security-Policy")
		for _, want := range []string{"script-src 'self'", "frame-ancestors 'none'", "default-src 'none'", "base-uri 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s CSP %q lacks %q", path, csp, want)
			}
		}
		if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
			t.Errorf("%s CSP allows inline code: %q", path, csp)
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Referrer-Policy") != "strict-origin" {
			t.Errorf("%s lacks nosniff or Referrer-Policy: %v", path, resp.Header)
		}
	}
	resp, err := e.Owner.Get(e.Base + "/ui/overview")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("overview Cache-Control = %q", resp.Header.Get("Cache-Control"))
	}
	login, err := http.Get(e.Base + "/ui/login")
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	if login.Header.Get("Content-Security-Policy") == "" {
		t.Error("the login page has no CSP")
	}
}

var (
	scriptTagRE = regexp.MustCompile(`<script[^>]*>`)
	inlineAttrs = regexp.MustCompile(`(?i)(\sstyle=|<style|\son[a-z]+=|javascript:)`)
)

func TestUIPagesWorkUnderTheCSP(t *testing.T) {
	e := shellWebAll(t)
	fillVisible(t, e.St)
	for _, path := range []string{"/ui/overview", "/ui/overview?connect=1", "/ui/requests", "/ui/knowledge", "/ui/coord", "/ui/orgs", "/ui/device", "/ui/account/tokens", "/ui/sessions", "/ui/review"} {
		_, page := fetchPage(t, e.Owner, e.Base+path)
		for _, tag := range scriptTagRE.FindAllString(page, -1) {
			if !strings.Contains(tag, "src=") {
				t.Errorf("%s has an inline script: %s", path, tag)
			}
		}
		if m := inlineAttrs.FindString(page); m != "" {
			t.Errorf("%s has inline style or handler %q", path, m)
		}
	}
	login, err := http.Get(e.Base + "/ui/login")
	if err != nil {
		t.Fatal(err)
	}
	if page := body(t, login); inlineAttrs.MatchString(page) {
		t.Error("the login page has inline style or handlers")
	}
}

func TestProjectSelectorShowsTheShortNameAndKeepsTheRemote(t *testing.T) {
	e := shellWebAll(t)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/overview")
	want := `<option value="` + shellProject + `" title="` + shellProject + `">shell</option>`
	if !strings.Contains(page, want) && !strings.Contains(page, strings.Replace(want, `">shell`, `" selected>shell`, 1)) {
		t.Errorf("project option lacks the short name with the remote as value and title: %s", page)
	}
}

func TestKnowledgeKeepPagesPastHiddenEntries(t *testing.T) {
	st := mustStore(t)
	a := &app{store: st}
	ovKnowledge(t, st, shellProject, "old visible", "trusted")
	for i := 0; i < 230; i++ {
		ovKnowledge(t, st, shellProject, "hidden "+strconv.Itoa(i), "trusted")
	}
	rows, err := a.knowledgeKeep(store.KnowledgeWindow{}, 3, func(k store.Knowledge) bool { return k.Title == "old visible" })
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Title != "old visible" {
		t.Errorf("hidden entries pushed the visible one out of the window: %v", rows)
	}
}
