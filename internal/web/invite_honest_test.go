package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-683 (WP10 + WP11): an invitation says honestly whether a mail went out, stays
// at the members, lists every open invitation; a guest lands in the shared project;
// after signing in or joining the real organization is the active one.

// mailedOrg is an instance with a file mailbox, the organization "Werkstatt Keller"
// and alice, its owner, signed in.
func mailedOrg(t *testing.T) (*mailInstance, *http.Client) {
	t.Helper()
	m := newMailInstance(t, false)
	m.account("alice", "alice@example.test")
	if _, err := m.st.CreateOrg("person:1", "Werkstatt Keller", "werkstatt"); err != nil {
		t.Fatal(err)
	}
	return m, loginInteractive(t, m.srv, m.st, "alice")
}

func inviteForm(extra ...string) url.Values {
	v := url.Values{"org": {"werkstatt"}, "role": {"member"}}
	for i := 0; i+1 < len(extra); i += 2 {
		v.Set(extra[i], extra[i+1])
	}
	return v
}

var inviteLinkRE = regexp.MustCompile(`data-select>(https?://[^<]+)<`)

// AC-683.1: with an address the invitation goes out by mail, and the page says so.
func TestInvitationWithAnAddressSendsAMailAndTheBannerSaysSo(t *testing.T) {
	m, owner := mailedOrg(t)
	loc := postOrgLoc(t, owner, m.srv.URL, "/ui/orgs/invite", inviteForm("email", "jonas@werkstatt.test"))
	mails := m.mails("jonas@werkstatt.test")
	if len(mails) != 1 {
		t.Fatalf("mails for the invited address: %d", len(mails))
	}
	mailText := mails[0]
	for _, want := range []string{"Subject: alice invited you to ghosttree", "alice invited you to Werkstatt Keller", "/ui/login/code?code="} {
		if !strings.Contains(mailText, want) {
			t.Errorf("the mail lacks %q:\n%s", want, mailText)
		}
	}
	page := pageAt(t, owner, m.srv.URL, loc)
	if !strings.Contains(page, "Invitation sent to jonas@werkstatt.test") {
		t.Errorf("the page does not say that the mail went out: %s", pageSpan(t, page, `id="members"`, `id="projects"`))
	}
	link := inviteLinkRE.FindStringSubmatch(page)
	if link == nil {
		t.Fatalf("the link is gone from the page after the mail")
	}
	if !strings.Contains(mailText, strings.TrimPrefix(link[1], m.srv.URL)) {
		t.Errorf("the mail carries another link than the page: %s vs %s", mailText, link[1])
	}
	if strings.Contains(page, "No mail was sent") {
		t.Error("the page claims that no mail went out although one did")
	}
}

// AC-683.1: without an address, without mail on the server, or when sending fails,
// the page says that no mail went out and that the link has to be passed on.
func TestInvitationSaysWhenNoMailWentOut(t *testing.T) {
	// Mail on, but no address typed.
	m, owner := mailedOrg(t)
	loc := postOrgLoc(t, owner, m.srv.URL, "/ui/orgs/invite", inviteForm())
	page := pageAt(t, owner, m.srv.URL, loc)
	if !strings.Contains(page, "No mail was sent because no address was entered") || !strings.Contains(page, "Pass the link on") {
		t.Errorf("no address: %s", pageSpan(t, page, `id="members"`, `id="projects"`))
	}
	if files, _ := filepath.Glob(filepath.Join(m.dir, "*.eml")); len(files) != 0 {
		t.Errorf("a mail was written without an address: %v", files)
	}

	// No mail on this server.
	base, _, alice, _, org := orgWeb(t)
	loc = postOrgLoc(t, alice, base, "/ui/orgs/invite", url.Values{"org": {org.Slug}})
	page = pageAt(t, alice, base, loc)
	if !strings.Contains(page, "No mail was sent: this server cannot send mail. Pass the link on to the person you invite") {
		t.Errorf("mail off: %s", pageSpan(t, page, `id="members"`, `id="projects"`))
	}

	// Sending fails: the link stays, the failure is named.
	st, err := store.Open(filepath.Join(t.TempDir(), "fail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.AddAccount("alice", "alice@example.test", false)
	st.CreateOrg("person:1", "Werkstatt Keller", "werkstatt")
	srv := httptest.NewServer(New(st, WithMail(failingSender{}), WithPublicURL("https://gt.example.test"), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))))
	defer srv.Close()
	c := loginInteractive(t, srv, st, "alice")
	loc = postOrgLoc(t, c, srv.URL, "/ui/orgs/invite", inviteForm("email", "jonas@werkstatt.test"))
	page = pageAt(t, c, srv.URL, loc)
	if !strings.Contains(page, "The mail to jonas@werkstatt.test could not be sent") || inviteLinkRE.FindStringSubmatch(page) == nil {
		t.Errorf("failed mail: %s", pageSpan(t, page, `id="members"`, `id="projects"`))
	}
}

// AC-683.1 (guests): a guest link with an address goes out by mail, too.
func TestGuestInvitationWithAnAddressIsMailed(t *testing.T) {
	m, owner := mailedOrg(t)
	m.st.SetAccessMode(store.AccessMode{Enforce: true})
	if _, err := claimInOrg(m.st, "person:1", joinProject); err != nil {
		t.Fatal(err)
	}
	loc := postOrgLoc(t, owner, m.srv.URL, "/ui/orgs/invite", inviteForm("role", "guest", "project", joinProject, "email", "gast@werkstatt.test"))
	if mails := m.mails("gast@werkstatt.test"); len(mails) != 1 || !strings.Contains(mails[0], "/join/") {
		t.Fatalf("the guest got no mail with the link: %v", mails)
	}
	if page := pageAt(t, owner, m.srv.URL, loc); !strings.Contains(page, "Invitation sent to gast@werkstatt.test") {
		t.Errorf("the page does not say that the guest was mailed")
	}
	// The address field stays in the form when the guest is chosen.
	if css, _ := os.ReadFile("static/ui-settings.css"); strings.Contains(string(css), `label:has(input[name="email"]) { display: none; }`) {
		t.Error("the stylesheet hides the address field for guests")
	}
}

// AC-683.2: after inviting one stays at the members, and every open invitation is
// in the list with link, role, expiry and state; a new one replaces no old one.
func TestInvitingStaysAtTheMembersAndListsEveryOpenInvitation(t *testing.T) {
	m, owner := mailedOrg(t)
	loc1 := postOrgLoc(t, owner, m.srv.URL, "/ui/orgs/invite", inviteForm("email", "jonas@werkstatt.test"))
	loc2 := postOrgLoc(t, owner, m.srv.URL, "/ui/orgs/invite", inviteForm("email", "pia@werkstatt.test"))
	if !strings.HasSuffix(loc1, "#members") || !strings.HasSuffix(loc2, "#members") {
		t.Fatalf("an invitation leads away from the members: %q %q", loc1, loc2)
	}
	page := pageAt(t, owner, m.srv.URL, loc2)
	links := inviteLinkRE.FindAllStringSubmatch(page, -1)
	seen := map[string]bool{}
	for _, l := range links {
		seen[l[1]] = true
	}
	// Banner and list rows: the two links of the two invitations are both on the page.
	if len(seen) != 2 {
		t.Fatalf("the page shows %d different invitation links, want 2: %v", len(seen), seen)
	}
	list := pageSpan(t, page, `class="box-h"><span class="t">Invitations`, `id="projects"`)
	for _, want := range []string{"jonas@werkstatt.test", "pia@werkstatt.test", "Open", "member", "Mail sent to jonas@werkstatt.test", "Mail sent to pia@werkstatt.test"} {
		if !strings.Contains(list, want) {
			t.Errorf("the invitation list lacks %q: %s", want, list)
		}
	}
	if strings.Count(list, "Expires") < 2 {
		t.Errorf("not every open invitation names its expiry: %s", list)
	}
	// The banner is part of the members, not above the whole settings page.
	if bi, mi := strings.Index(page, "Invitation sent to pia@werkstatt.test"), strings.Index(page, `id="members"`); bi < mi {
		t.Error("the banner stands above the members section")
	}
}

// AC-683.2: a failed invitation also opens the members, not General.
func TestFailedInvitationOpensTheMembers(t *testing.T) {
	m, owner := mailedOrg(t)
	resp := postOrg(t, owner, m.srv.URL, "/ui/orgs/invite", inviteForm("email", "not an address"))
	page := body(t, resp)
	if resp.StatusCode == http.StatusSeeOther || !strings.Contains(page, `data-open="members"`) {
		t.Fatalf("a refused invitation does not open the members: %d %s", resp.StatusCode, pageSpan(t, page, `class="flash err"`, `</div>`))
	}
}

// A pending invitation whose link the server forgot (restart) says so.
func TestPendingInvitationWithoutALinkSaysSo(t *testing.T) {
	m, owner := mailedOrg(t)
	org, err := m.st.OrgByRef("werkstatt")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.st.CreateInvitation("person:1", org.ID, "", store.OrgMember, time.Hour); err != nil {
		t.Fatal(err)
	}
	page := getOrgs(t, owner, m.srv.URL, "?org=werkstatt")
	if !strings.Contains(page, "The link is not shown any more") {
		t.Errorf("an invitation without a link says nothing: %s", pageSpan(t, page, `Invitations`, `id="projects"`))
	}
}

// guestJoin: a guest link of the project, accepted by a new account through the
// join page. The answer says where the guest lands.
func guestInvite(t *testing.T) (srv *httptest.Server, st *store.Store, owner *http.Client, org store.Org, code string) {
	t.Helper()
	srv, st, owner, org, _ = joinWeb(t)
	return srv, st, owner, org, projectInvite(t, st, org, store.RoleGuest)
}

// AC-683.3: a new guest lands in the shared project, not at "Connect your computer".
func TestNewGuestLandsInTheSharedProject(t *testing.T) {
	srv, _, _, _, code := guestInvite(t)
	resp := sameOriginPostForm(t, anonClient(), srv.URL+"/ui/login/code", url.Values{"code": {code}, "name": {"gast"}, "join": {"1"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != repoHref(joinProject) {
		t.Fatalf("a new guest lands at %q, want %q", resp.Header.Get("Location"), repoHref(joinProject))
	}
}

// AC-683.3: an existing account that accepts a guest link lands in the project, too.
func TestSignedInGuestLandsInTheSharedProject(t *testing.T) {
	srv, st, _, org, _ := joinWeb(t)
	st.AddPerson("anna")
	anna := loginInteractive(t, srv, st, "anna")
	code := projectInvite(t, st, org, store.RoleGuest)
	page := body(t, getURL(t, anna, srv.URL+"/join/"+code))
	csrf := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(page)[1]
	resp := sameOriginPostForm(t, anna, srv.URL+"/join/"+code+"/accept", url.Values{"csrf_token": {csrf}, "confirm_account": {"anna"}})
	resp.Body.Close()
	if resp.Header.Get("Location") != repoHref(joinProject) {
		t.Fatalf("a guest lands at %q, want %q", resp.Header.Get("Location"), repoHref(joinProject))
	}
}

// AC-683.3: the organization page names the guest membership, an empty repo shows
// a guest no agent command, and the invitation list names the guest with the role.
func TestGuestSeesTheirMembershipNoCommandsAndIsListedByName(t *testing.T) {
	srv, st, owner, org, code := guestInvite(t)
	resp := sameOriginPostForm(t, anonClient(), srv.URL+"/ui/login/code", url.Values{"code": {code}, "name": {"gast"}, "join": {"1"}})
	resp.Body.Close()
	guest := anonClient()
	guest.Jar = cookieJar{cookies: resp.Cookies()}

	page := body(t, getURL(t, guest, srv.URL+"/ui/orgs"))
	if strings.Contains(page, "You are not in an organization yet") {
		t.Error("a guest is told that they are in no organization")
	}
	for _, want := range []string{"Guest in Alpha", joinProject} {
		if !strings.Contains(page, want) {
			t.Errorf("the organization page of a guest lacks %q: %s", want, plainText(page))
		}
	}

	repo := body(t, getURL(t, guest, srv.URL+repoHref(joinProject)))
	if strings.Contains(repo, "context_remember") || strings.Contains(repo, "describe_file") {
		t.Errorf("an agent command in the empty repo of a guest: %s", pageSpan(t, repo, "ct-empty", "</div>"))
	}
	if ownerRepo := body(t, getURL(t, owner, srv.URL+repoHref(joinProject))); !strings.Contains(ownerRepo, "describe_file") {
		t.Fatal("the owner no longer sees the command (the test would prove nothing)")
	}

	invites := getOrgs(t, owner, srv.URL, "?org="+org.Slug)
	row := pageSpan(t, invites, `class="row mrow" id="invite-`, `class="acts"`)
	if !strings.Contains(row, `<b class="ov-name">gast</b>`) || !strings.Contains(row, "Guest") {
		t.Errorf("the accepted guest is not listed by name with the role Guest: %s", row)
	}
	_ = st
}

// AC-683.4: after signing in the real organization is active, not the empty
// placeholder the account was given; the welcome card names it; the account menu
// names the role in it.
func TestRealOrganizationIsActiveAfterSignIn(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ctx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	bootstrap, _, err := st.EnsureBootstrapCode()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "bootstrap-code")
	os.WriteFile(file, []byte(bootstrap), 0o600)
	srv := httptest.NewServer(New(st, WithBootstrapFile(file)))
	defer srv.Close()
	resp := sameOriginPostForm(t, noRedirect(), srv.URL+"/ui/login/code", url.Values{"code": {bootstrap}, "name": {"mara"}})
	resp.Body.Close()
	if _, err := st.CreateOrg("person:1", "Werkstatt Keller", "werkstatt"); err != nil {
		t.Fatal(err)
	}
	// A new sign-in: no cookie remembers an organization.
	mara := loginInteractive(t, srv, st, "mara")
	page := body(t, getURL(t, mara, srv.URL+"/ui/inbox"))
	if !strings.Contains(page, "You are in Werkstatt Keller") || strings.Contains(page, "You are in Default") {
		t.Errorf("the welcome card names the wrong organization: %s", pageSpan(t, page, `class="welcome"`, `</section>`))
	}
	if !strings.Contains(page, `href="/ui/orgs?org=werkstatt#invitations">Invite people`) {
		t.Errorf("the card does not offer the next step in Werkstatt Keller")
	}
	// A step that is done is not listed again.
	org, _ := st.OrgByRef("werkstatt")
	if _, _, err := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, time.Hour); err != nil {
		t.Fatal(err)
	}
	if page := body(t, getURL(t, mara, srv.URL+"/ui/inbox")); strings.Contains(page, "Invite people") {
		t.Errorf("a done step stays on the card: %s", pageSpan(t, page, `class="welcome"`, `</section>`))
	}
	if cur := pageSpan(t, page, `class="org-list"`, `</div>`); !regexp.MustCompile(`class="opt cur"[^>]*>.*?<span>Werkstatt Keller</span>`).MatchString(cur) {
		t.Errorf("the active organization of the menu is not Werkstatt Keller: %s", cur)
	}
}

// AC-683.4: the account menu names the role in the active organization, not the
// highest role anywhere.
func TestAccountMenuNamesTheRoleInTheActiveOrganization(t *testing.T) {
	srv, st, _ := testWeb(t)
	alpha, err := st.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPerson("anna"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimProject("person:1", "github.com/x/one", "alpha"); err != nil {
		t.Fatal(err)
	}
	code, _, err := st.CreateInvitation("person:1", alpha.ID, "", store.OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	// Anna owns a second, empty organization of her own.
	if _, err := st.CreateOrg("person:2", "Ablage", "ablage"); err != nil {
		t.Fatal(err)
	}
	fresh := loginInteractive(t, srv, st, "anna")
	page := body(t, getURL(t, fresh, srv.URL+"/ui/inbox"))
	if got := regexp.MustCompile(`class="account-role">([^<]*)<`).FindStringSubmatch(page); got == nil || got[1] != "Member" {
		t.Errorf("the account menu names %v, want Member (the role in Alpha)", got)
	}
}

// AC-683.4: joining sets the joined organization as the active one.
func TestJoiningMakesTheJoinedOrganizationActive(t *testing.T) {
	srv, st, _, org, _ := joinWeb(t)
	st.AddPerson("anna")
	anna := loginInteractive(t, srv, st, "anna")
	code := projectInvite(t, st, org, store.RoleMember)
	page := body(t, getURL(t, anna, srv.URL+"/join/"+code))
	csrf := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(page)[1]
	resp := sameOriginPostForm(t, anna, srv.URL+"/join/"+code+"/accept", url.Values{"csrf_token": {csrf}, "confirm_account": {"anna"}})
	resp.Body.Close()
	var got string
	for _, c := range resp.Cookies() {
		if c.Name == orgCookie {
			got = c.Value
		}
	}
	if got != org.Slug {
		t.Errorf("after joining Alpha the remembered organization is %q", got)
	}
}
