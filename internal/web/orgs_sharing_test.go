package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/forge"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// fakeProver ersetzt GitHub: es weiß, welche Konten der Mensch verwaltet.
type fakeProver struct {
	configured bool
	accounts   []forge.Account
	codes      []string
}

func (f *fakeProver) Configured() bool { return f.configured }
func (f *fakeProver) AuthorizeURL(state, redirect string) string {
	return "https://forge.example/authorize?state=" + url.QueryEscape(state) + "&redirect_uri=" + url.QueryEscape(redirect)
}
func (f *fakeProver) Prove(_ context.Context, code, _ string) ([]forge.Account, error) {
	f.codes = append(f.codes, code)
	if code != "good" {
		return nil, forge.ErrNotConfigured
	}
	return f.accounts, nil
}

func sharingWeb(t *testing.T, prover forge.Prover) (string, *store.Store, *http.Client, *http.Client) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/web.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, n := range []string{"alice", "anna"} {
		if _, err := st.AddPerson(n); err != nil {
			t.Fatal(err)
		}
	}
	org, err := st.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:2", mustOrgInvite(t, st, org)); err != nil {
		t.Fatal(err)
	}
	opts := append([]Option{WithSourceProver(prover)}, testWebOptions...)
	srv := httptest.NewServer(New(st, opts...))
	t.Cleanup(srv.Close)
	return srv.URL, st, loginInteractive(t, srv, st, "alice"), loginInteractive(t, srv, st, "anna")
}

func TestProjectVisibilityIsASwitchOnTheOrgPage(t *testing.T) {
	base, st, alice, anna := sharingWeb(t, nil)
	if _, err := st.ClaimProject("person:1", "github.com/x/one", "alpha"); err != nil {
		t.Fatal(err)
	}
	page := getOrgs(t, alice, base, "?org=alpha")
	if !strings.Contains(page, "Private") || !strings.Contains(page, "Share with the org") || !strings.Contains(page, `action="/ui/orgs/project/visibility"`) {
		t.Fatalf("owner page lacks the visibility switch: %s", page)
	}
	if resp := postOrg(t, alice, base, "/ui/orgs/project/visibility", url.Values{"org": {"alpha"}, "remote": {"github.com/x/one"}, "shared": {"1"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("share: %d", resp.StatusCode)
	}
	if got := st.ProjectRole("github.com/x/one", "person:2"); got.Role != store.RoleMember {
		t.Fatalf("anna must work in the shared project: %+v", got)
	}
	page = getOrgs(t, alice, base, "?org=alpha")
	if !strings.Contains(page, "Shared with the org") || !strings.Contains(page, "Make private") || !strings.Contains(page, "Default role") {
		t.Fatalf("shared state not shown: %s", page)
	}
	// Anna sees the shared project and cannot switch it.
	annaPage := getOrgs(t, anna, base, "?org=alpha")
	if strings.Contains(annaPage, `action="/ui/orgs/project/visibility"`) {
		t.Fatal("a member got the switch")
	}
	if resp := postOrg(t, anna, base, "/ui/orgs/project/visibility", url.Values{"org": {"alpha"}, "remote": {"github.com/x/one"}, "shared": {"0"}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a member switching the project: %d", resp.StatusCode)
	}
	if !st.ProjectShared("github.com/x/one") {
		t.Fatal("a refused switch changed the project")
	}
	if resp := postOrg(t, alice, base, "/ui/orgs/project/visibility", url.Values{"org": {"alpha"}, "remote": {"github.com/x/one"}, "shared": {"0"}}); resp.StatusCode != http.StatusSeeOther || st.ProjectShared("github.com/x/one") {
		t.Fatalf("private again: %d", resp.StatusCode)
	}
}

func TestGitHubShowsNotSetUpWhileTheCredentialsAreMissing(t *testing.T) {
	for name, prover := range map[string]forge.Prover{"none": nil, "unconfigured": &fakeProver{}} {
		base, _, alice, _ := sharingWeb(t, prover)
		page := getOrgs(t, alice, base, "?org=alpha")
		if !strings.Contains(page, "Not set up") || strings.Contains(page, `action="/ui/orgs/source/connect"`) {
			t.Fatalf("%s: the page must say GitHub is not set up: %s", name, page)
		}
		resp := postOrg(t, alice, base, "/ui/orgs/source/connect", url.Values{"org": {"alpha"}})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(body(t, resp), "GitHub is not set up on this server yet.") {
			t.Fatalf("%s: connecting without credentials: %d", name, resp.StatusCode)
		}
	}
}

func TestConnectingGitHubProvesAdminClaimsThePrefixAndListsRepos(t *testing.T) {
	prover := &fakeProver{configured: true, accounts: []forge.Account{{Login: "deadweight-labs", Repos: []string{"github.com/deadweight-labs/ghosttree", "github.com/deadweight-labs/infra"}}}}
	base, st, alice, anna := sharingWeb(t, prover)
	org, _ := st.OrgByRef("alpha")
	page := getOrgs(t, alice, base, "?org=alpha")
	if !strings.Contains(page, `action="/ui/orgs/source/connect"`) || !strings.Contains(page, "Connect GitHub") {
		t.Fatalf("owner lacks the connect button: %s", page)
	}
	if strings.Contains(getOrgs(t, anna, base, "?org=alpha"), `action="/ui/orgs/source/connect"`) {
		t.Fatal("a member can connect sources")
	}
	if resp := postOrg(t, anna, base, "/ui/orgs/source/connect", url.Values{"org": {"alpha"}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member connect: %d", resp.StatusCode)
	}
	resp := postOrg(t, alice, base, "/ui/orgs/source/connect", url.Values{"org": {"alpha"}})
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "https://forge.example/authorize?state=") {
		t.Fatalf("connect must send the browser to the forge: %d %q", resp.StatusCode, loc)
	}
	u, _ := url.Parse(loc)
	state := u.Query().Get("state")
	if !strings.HasSuffix(u.Query().Get("redirect_uri"), "/ui/orgs/source/callback") {
		t.Fatalf("redirect: %s", loc)
	}
	// Ein anderer Browser kann den Ablauf nicht abschließen.
	if r, _ := anna.Get(base + "/ui/orgs/source/callback?code=good&state=" + state); !strings.Contains(r.Header.Get("Location"), "source_failed") {
		t.Fatalf("a foreign callback must fail: %q", r.Header.Get("Location"))
	}
	back, err := alice.Get(base + "/ui/orgs/source/callback?code=good&state=" + state)
	if err != nil {
		t.Fatal(err)
	}
	pickLoc := back.Header.Get("Location")
	if !strings.Contains(pickLoc, "pick=") || len(prover.codes) != 1 {
		t.Fatalf("callback: %q %v", pickLoc, prover.codes)
	}
	// Der state ist verbraucht.
	if r, _ := alice.Get(base + "/ui/orgs/source/callback?code=good&state=" + state); !strings.Contains(r.Header.Get("Location"), "source_failed") {
		t.Fatal("the state must work once")
	}
	pick := pickLoc[strings.Index(pickLoc, "pick=")+5:]
	page = getOrgs(t, alice, base, pickLoc[len("/ui/orgs"):])
	if !strings.Contains(page, "Choose a GitHub organization") || !strings.Contains(page, "github.com/deadweight-labs") {
		t.Fatalf("the proven organizations are not offered: %s", page)
	}
	// Ein Login, den GitHub nicht bestätigt hat, wird nicht verbunden.
	if r := postOrg(t, alice, base, "/ui/orgs/source/add", url.Values{"org": {"alpha"}, "pick": {pick}, "login": {"someone-else"}, "visibility": {"shared"}}); !strings.Contains(r.Header.Get("Location"), "source_failed") {
		t.Fatalf("unproven login: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	if srcs, _ := st.ListSources("person:1", org.ID); len(srcs) != 0 {
		t.Fatalf("an unproven source was connected: %+v", srcs)
	}
	r := postOrg(t, alice, base, "/ui/orgs/source/add", url.Values{"org": {"alpha"}, "pick": {pick}, "login": {"deadweight-labs"}, "visibility": {"shared"}})
	if r.StatusCode != http.StatusSeeOther || !strings.Contains(r.Header.Get("Location"), "notice=source_added") {
		t.Fatalf("add: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	prefixes, _ := st.ListOrgPrefixes("person:1", org.ID)
	if len(prefixes) != 1 || prefixes[0].Prefix != "github.com/deadweight-labs" || prefixes[0].Visibility != "shared" {
		t.Fatalf("prefix: %+v", prefixes)
	}
	page = getOrgs(t, alice, base, "?org=alpha")
	for _, want := range []string{"github.com/deadweight-labs", "2 repos", "0 as projects", "Create projects", "Disconnect"} {
		if !strings.Contains(page, want) {
			t.Fatalf("sources section lacks %q: %s", want, page)
		}
	}
	if r := postOrg(t, alice, base, "/ui/orgs/source/import", url.Values{"org": {"alpha"}, "login": {"deadweight-labs"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("import: %d", r.StatusCode)
	}
	if !st.ProjectShared("github.com/deadweight-labs/infra") || st.ProjectRole("github.com/deadweight-labs/infra", "person:2").Role != store.RoleMember {
		t.Fatal("imported repos are shared projects anna works in")
	}
	// Ein frisch geklontes Repo derselben GitHub-Org landet beim ersten Kontakt in der Org.
	if p, err := st.EnsureProject("person:2", "github.com/deadweight-labs/brand-new"); err != nil || p.OrgID != org.ID || !p.Shared {
		t.Fatalf("first contact: %+v %v", p, err)
	}
	// Das Zugriffstoken oder Geheimnis taucht nirgends in der Seite auf.
	if strings.Contains(page, "shh") {
		t.Fatal("a secret reached the page")
	}
}

func TestGuestsAreNamedAndGuestInvitesNeedEnforcement(t *testing.T) {
	base, st, alice, _ := sharingWeb(t, nil)
	if _, err := st.ClaimProject("person:1", "github.com/x/one", "alpha"); err != nil {
		t.Fatal(err)
	}
	if page := getOrgs(t, alice, base, "?org=alpha"); strings.Contains(page, `id="guests"`) {
		t.Fatal("guest links are offered without enforcement")
	}
	st.SetAccessMode(store.AccessMode{Enforce: true})
	org, _ := st.OrgByRef("alpha")
	code, _, err := st.CreateProjectInvitation("person:1", org.ID, "github.com/x/one", store.RoleGuest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPerson("gus"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:3", code); err != nil {
		t.Fatal(err)
	}
	page := getOrgs(t, alice, base, "?org=alpha")
	if !strings.Contains(page, `id="guests"`) || !strings.Contains(page, "gus") {
		t.Fatalf("guest section: %s", page)
	}
}
