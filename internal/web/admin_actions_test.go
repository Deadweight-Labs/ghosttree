package web

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-680 (WP7): administrative actions ask first, say what they did, and stay in
// the organization the viewer works in.

// adminOrg is the organization "Alpha" with two projects, alice its owner and
// anna a member.
func adminOrg(t *testing.T) (base string, st *store.Store, alice, anna *http.Client, org store.Org) {
	t.Helper()
	base, st, alice, anna, org = orgWeb(t)
	for _, remote := range []string{"github.com/x/one", "github.com/x/two"} {
		if _, err := st.ClaimProject("person:1", remote, "alpha"); err != nil {
			t.Fatal(err)
		}
	}
	code, _, err := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	return base, st, alice, anna, org
}

// pageSpan is the part of a page from the first occurrence of from up to the next
// occurrence of until after it (the rest when there is none).
func pageSpan(t *testing.T, page, from, until string) string {
	t.Helper()
	i := strings.Index(page, from)
	if i < 0 {
		t.Fatalf("page has no %q", from)
	}
	rest := page[i+len(from):]
	if j := strings.Index(rest, until); j >= 0 {
		rest = rest[:j]
	}
	return from + rest
}

// postOrgLoc posts a form of the settings and returns where the answer leads.
func postOrgLoc(t *testing.T, c *http.Client, base, path string, form url.Values) string {
	t.Helper()
	resp := postOrg(t, c, base, path, form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("%s = %d", path, resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

func pageAt(t *testing.T, c *http.Client, base, loc string) string {
	t.Helper()
	resp, err := c.Get(base + loc)
	if err != nil {
		t.Fatal(err)
	}
	return body(t, resp)
}

// AC-680.1: owner or member, removing a member, revoking an invitation: a question
// with the name and the consequence comes first; one's own demotion warns more.
func TestMemberActionsAskBeforeTheyAct(t *testing.T) {
	base, st, alice, _, org := adminOrg(t)
	if _, _, err := st.CreateInvitation("person:1", org.ID, "pia@example.test", store.OrgMember, 0); err != nil {
		t.Fatal(err)
	}
	page := getOrgs(t, alice, base, "?org=alpha")
	for _, want := range []string{
		`<summary class="gbtn sm">Make owner</summary>`,
		"Make anna an owner of Alpha? Owners manage",
		`<summary class="gbtn sm danger">Remove</summary>`,
		"Remove anna from Alpha? They lose access to the projects",
		`<summary class="gbtn sm danger">Revoke</summary>`,
		"Revoke the invitation for pia@example.test? The link stops working at once",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("settings lack %q", want)
		}
	}
	// No button acts on a single click.
	for _, bare := range []string{`<button class="gbtn sm">Make owner</button>`, `<button class="gbtn sm danger">Remove</button>`, `<button class="gbtn sm danger">Revoke</button>`} {
		if strings.Contains(page, bare) {
			t.Errorf("a button acts without asking: %s", bare)
		}
	}
	if strings.Contains(page, "Make yourself a member") {
		t.Error("the only owner is offered to step down")
	}

	// With a second owner, stepping down is possible and warns especially.
	if err := st.SetOrgRole("person:1", org.ID, "person:2", store.OrgOwner); err != nil {
		t.Fatal(err)
	}
	page = getOrgs(t, alice, base, "?org=alpha")
	for _, want := range []string{
		"Make anna a member of Alpha? They can then no longer manage",
		"Make yourself a member of Alpha? You lose the owner rights at once",
		`<summary class="gbtn sm danger">Make member</summary>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("settings lack %q", want)
		}
	}
	if own := pageSpan(t, page, `id="m-person_1"`, `id="m-person_2"`); !strings.Contains(own, `Make yourself a member`) || strings.Contains(own, "Make anna a member") {
		t.Errorf("the own row does not carry the own warning: %s", own)
	}
	// Leaving asks, too.
	if !strings.Contains(pageSpan(t, page, `id="m-person_1"`, `id="m-person_2"`), "Leave") {
		t.Error("the own row cannot leave")
	}
}

// AC-680.2: the role is chosen when inviting, with a sentence for each.
func TestInviteAsksForTheRoleAndSaysWhatItAllows(t *testing.T) {
	base, st, alice, _, org := adminOrg(t)
	page := getOrgs(t, alice, base, "?org=alpha")
	// Without enforced visibility the guest stays in the choice, switched off
	// and with the reason (REQ-704, R12): a person sees that guests exist.
	if roles := pageSpan(t, page, `<fieldset class="irole">`, "</fieldset>"); !strings.Contains(roles, "Works in the shared projects of the organization") ||
		!strings.Contains(roles, `value="guest" data-role disabled`) || !strings.Contains(roles, "GHOSTTREE_ENFORCE_ACCESS=1") || strings.Contains(roles, `name="project"`) {
		t.Errorf("without enforced visibility the guest is offered switched off, with the reason: %s", roles)
	}

	st.SetAccessMode(store.AccessMode{Enforce: true})
	page = getOrgs(t, alice, base, "?org=alpha")
	roles := pageSpan(t, page, `<fieldset class="irole">`, "</fieldset>")
	for _, want := range []string{`name="role" value="member"`, `name="role" value="guest"`, "Works in the shared projects of the organization. Only owners manage members and settings.", "Sees one project only, nothing else of the organization.", `name="project"`} {
		if !strings.Contains(roles, want) {
			t.Errorf("role choice lacks %q: %s", want, roles)
		}
	}

	// A guest is invited into one project.
	loc := postOrgLoc(t, alice, base, "/ui/orgs/invite", url.Values{"org": {"alpha"}, "role": {"guest"}, "project": {"github.com/x/one"}, "days": {"3"}})
	if !strings.Contains(pageAt(t, alice, base, loc), "/join/") {
		t.Error("no guest link")
	}
	// A member is invited into the organization, whatever project the form carries.
	postOrgLoc(t, alice, base, "/ui/orgs/invite", url.Values{"org": {"alpha"}, "role": {"member"}, "project": {"github.com/x/one"}, "email": {"mia@example.test"}})
	invs, _ := st.ListInvitations("person:1", org.ID)
	var guest, member bool
	for _, inv := range invs {
		guest = guest || inv.ProjectRemote == "github.com/x/one" && inv.ProjectRole == store.RoleGuest
		member = member || inv.ProjectRemote == "" && inv.Role == store.OrgMember && inv.Email == "mia@example.test"
	}
	if !guest || !member {
		t.Errorf("invitations = %+v", invs)
	}
	// A guest needs a project, and owners are not made by a link.
	for name, form := range map[string]url.Values{
		"guest without project": {"org": {"alpha"}, "role": {"guest"}},
		"owner link":            {"org": {"alpha"}, "role": {"owner"}},
	} {
		if resp := postOrg(t, alice, base, "/ui/orgs/invite", form); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s = %d", name, resp.StatusCode)
		}
	}
	// The reviewer box says what it does.
	if page := getOrgs(t, alice, base, "?org=alpha"); !strings.Contains(page, "Can review knowledge") {
		t.Error("the reviewer box is not explained")
	}
}

// AC-680.3: the outcome names the person and the object, the section stays open
// and the changed row is marked; the note comes once.
func TestOutcomesNamePersonAndObjectAndKeepTheSectionOpen(t *testing.T) {
	base, st, alice, _, org := adminOrg(t)

	loc := postOrgLoc(t, alice, base, "/ui/orgs/member/role", url.Values{"org": {"alpha"}, "account": {"person:2"}, "role": {"owner"}})
	if !strings.HasSuffix(loc, "#m-person_2") {
		t.Errorf("the redirect does not point at the row: %s", loc)
	}
	page := pageAt(t, alice, base, loc)
	if !strings.Contains(page, `<div class="flash fly" role="status">`) || !strings.Contains(page, "anna is now an owner of Alpha.") {
		t.Errorf("no note naming the person: %s", pageSpan(t, page, `class="flash`, "</div>"))
	}
	if !strings.Contains(page, `class="row mrow hit" id="m-person_2"`) {
		t.Error("the changed member row is not marked")
	}
	if again := pageAt(t, alice, base, loc); strings.Contains(again, "anna is now an owner") || strings.Contains(again, `class="row mrow hit"`) {
		t.Error("the note comes back on a reload")
	}
	if page := pageAt(t, alice, base, "/ui/orgs?org=alpha&done=forged-text"); strings.Contains(page, "forged-text") {
		t.Error("a link brings its own text")
	}
	// The note is for the one who acted.
	loc = postOrgLoc(t, alice, base, "/ui/orgs/member/role", url.Values{"org": {"alpha"}, "account": {"person:2"}, "role": {"member"}})
	if page := pageAt(t, alice, base, loc); !strings.Contains(page, "anna is now a member of Alpha.") {
		t.Errorf("member note: %s", pageSpan(t, page, `class="flash`, "</div>"))
	}

	// A project role is changed in the person's row of the member list
	// (REQ-704): the address leads there and marks the line of that project.
	loc = postOrgLoc(t, alice, base, "/ui/orgs/project/role", url.Values{"org": {"alpha"}, "remote": {"github.com/x/two"}, "account": {"person:2"}, "role": {"lead"}})
	if !strings.HasSuffix(loc, "#pr-github_com_x_two-person_2") {
		t.Errorf("the answer does not lead to the changed role: %s", loc)
	}
	page = pageAt(t, alice, base, loc)
	if !strings.Contains(page, "anna is now Lead in github.com/x/two.") {
		t.Errorf("project role note: %s", pageSpan(t, page, `class="flash`, "</div>"))
	}
	if row := pageSpan(t, page, `id="m-person_2"`, `id="m-person_`); !strings.Contains(row, `class="mproj hit" id="pr-github_com_x_two-person_2"`) {
		t.Errorf("the changed role is not marked in the member's row: %s", row)
	}

	loc = postOrgLoc(t, alice, base, "/ui/orgs/project/visibility", url.Values{"org": {"alpha"}, "remote": {"github.com/x/one"}, "shared": {"0"}})
	if page := pageAt(t, alice, base, loc); !strings.Contains(page, "github.com/x/one is now private") {
		t.Errorf("visibility note: %s", pageSpan(t, page, `class="flash`, "</div>"))
	}

	invCode, inv, err := st.CreateInvitation("person:1", org.ID, "pia@example.test", store.OrgMember, 0)
	if err != nil || invCode == "" {
		t.Fatal(err)
	}
	loc = postOrgLoc(t, alice, base, "/ui/orgs/invite/revoke", url.Values{"org": {"alpha"}, "id": {strconv.FormatInt(inv.ID, 10)}})
	if page := pageAt(t, alice, base, loc); !strings.Contains(page, "The invitation for pia@example.test was revoked.") {
		t.Errorf("revoke note: %s", pageSpan(t, page, `class="flash`, "</div>"))
	}

	loc = postOrgLoc(t, alice, base, "/ui/orgs/member/remove", url.Values{"org": {"alpha"}, "account": {"person:2"}})
	if page := pageAt(t, alice, base, loc); !strings.Contains(page, "anna was removed from Alpha.") {
		t.Errorf("remove note: %s", pageSpan(t, page, `class="flash`, "</div>"))
	}
}

// AC-680.3: a saved label is named, too.
func TestLabelNotesNameTheLabel(t *testing.T) {
	e := ovEnv(t)
	post := func(path string, v url.Values) string {
		v.Set("csrf_token", renderedCSRFToken(t, e.Lead, e.Base+"/ui/overview"))
		v.Set("project", shellProject)
		resp := sameOriginPostForm(t, e.Lead, e.Base+path, v)
		resp.Body.Close()
		return resp.Header.Get("Location")
	}
	loc := post("/ui/labels/create", url.Values{"name": {"bug"}, "color": {"d73a4a"}})
	page := pageAt(t, e.Lead, e.Base, loc)
	if !strings.Contains(page, "Label “bug” created.") || !strings.Contains(page, `class="flash fly"`) || !strings.Contains(page, `class="row lrow hit"`) {
		t.Errorf("created: %s", pageSpan(t, page, `class="flash`, "</div>"))
	}
	list, _ := e.St.Access(store.Principal{ID: "person:4", Label: "lars"}).ListLabels(shellProject)
	var id int64
	for _, lb := range list {
		if lb.Name == "bug" {
			id = lb.ID
		}
	}
	loc = post("/ui/labels/"+strconv.FormatInt(id, 10)+"/update", url.Values{"name": {"defect"}, "color": {"d73a4a"}})
	if page := pageAt(t, e.Lead, e.Base, loc); !strings.Contains(page, "Label “defect” saved.") {
		t.Errorf("updated: %s", pageSpan(t, page, `class="flash`, "</div>"))
	}
	loc = post("/ui/labels/"+strconv.FormatInt(id, 10)+"/delete", url.Values{})
	if page := pageAt(t, e.Lead, e.Base, loc); !strings.Contains(page, "Label “defect” deleted.") {
		t.Errorf("deleted: %s", pageSpan(t, page, `class="flash`, "</div>"))
	}
}

// AC-680.4: the list of invitations has a heading, details that are told apart,
// and the true state of each.
func TestInvitationListStatesAndSeparatedDetails(t *testing.T) {
	base, st, alice, _, org := adminOrg(t)
	_, pending, _ := st.CreateInvitation("person:1", org.ID, "pia@example.test", store.OrgMember, 0)
	acceptCode, accepted, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	bob, err := st.AddPerson("bob")
	if err != nil || bob == "" {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:3", acceptCode); err != nil {
		t.Fatal(err)
	}
	_, revoked, _ := st.CreateInvitation("person:1", org.ID, "rev@example.test", store.OrgMember, 0)
	if err := st.RevokeInvitation("person:1", org.ID, revoked.ID); err != nil {
		t.Fatal(err)
	}
	_, expired, _ := st.CreateInvitation("person:1", org.ID, "old@example.test", store.OrgMember, 0)
	if _, err := st.DB().Exec(`UPDATE invitations SET expires_at='2020-01-01T00:00:00Z' WHERE id=?`, expired.ID); err != nil {
		t.Fatal(err)
	}
	loc := postOrgLoc(t, alice, base, "/ui/orgs/invite", url.Values{"org": {"alpha"}, "role": {"member"}, "days": {"7"}})
	page := pageAt(t, alice, base, loc)

	all, _ := st.ListInvitations("person:1", org.ID)
	open := 0
	for _, inv := range all {
		if inv.Status == "pending" {
			open++
		}
	}
	// The count is of the open invitations; the others are folded below
	// (REQ-704, P27: who accepted stands in the member list already).
	if !strings.Contains(page, `<span class="t">Invitations</span><span class="counter">`+strconv.Itoa(open)+`</span>`) || len(all) < 5 || open == len(all) {
		t.Error("the list has no heading with the count of open invitations")
	}
	past := pageSpan(t, page, `<details class="ipast">`, "</details>")
	if !strings.Contains(past, "Earlier invitations ("+strconv.Itoa(len(all)-open)+")") || !strings.Contains(past, `id="invite-`+strconv.FormatInt(accepted.ID, 10)+`"`) || strings.Contains(past, `id="invite-`+strconv.FormatInt(pending.ID, 10)+`"`) {
		t.Errorf("accepted, revoked and expired invitations are not folded away: %s", past)
	}
	row := func(id int64) string {
		return pageSpan(t, page, `id="invite-`+strconv.FormatInt(id, 10)+`"`, `class="row mrow" id="invite-`)
	}
	check := `m3.5 8.4 3 3 6-6.5`
	if r := row(pending.ID); !strings.Contains(r, `class="lb neu">Open</span><span>member</span><span>Expires <time`) || !strings.Contains(r, "Revoke</summary>") || strings.Contains(r, check) {
		t.Errorf("open: %s", r)
	}
	if r := row(accepted.ID); !strings.Contains(r, `class="lb ok">Accepted</span>`) || !strings.Contains(r, "Accepted by bob") || strings.Contains(r, "Revoke</summary>") || !strings.Contains(r, check) {
		t.Errorf("accepted: %s", r)
	}
	if r := row(revoked.ID); !strings.Contains(r, `class="lb out">Revoked</span>`) || !strings.Contains(r, `ist is-revoked`) || strings.Contains(r, check) || strings.Contains(r, "Revoke</summary>") || strings.Contains(r, "Expires") {
		t.Errorf("revoked: %s", r)
	}
	if r := row(expired.ID); !strings.Contains(r, `class="lb out">Expired</span>`) || !strings.Contains(r, `ist is-expired`) || !strings.Contains(r, "Expired <time") || strings.Contains(r, "Revoke</summary>") {
		t.Errorf("expired: %s", r)
	}
	// The link just made: every detail stands in a span of its own.
	if !strings.Contains(page, `</span><span>Works once</span></div>`) {
		t.Errorf("details run together: %s", pageSpan(t, page, "Invitation ready", "</div></div>"))
	}
}

// AC-680.5: "New repository" and "Team & devices" lead to the organization the
// viewer works in, not to the empty standard one; the organization shown is a
// choice with a check, not a primary button.
func TestOrgContextFollowsTheViewer(t *testing.T) {
	base, st, alice, _, _ := adminOrg(t)
	alice = withJar(t, alice, base)
	beta, err := st.CreateOrg("person:1", "Beta", "beta")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimProject("person:1", "github.com/y/b1", beta.Slug); err != nil {
		t.Fatal(err)
	}

	// Nothing chosen yet: the organization with projects, not the empty standard one.
	page := getOrgs(t, alice, base, "")
	if !strings.Contains(page, `<a href="/ui/orgs?org=alpha" aria-current="page">`) {
		t.Errorf("the empty standard organization is shown: %s", pageSpan(t, page, `class="orgsw"`, "</nav>"))
	}
	if strings.Contains(page, `gbtn sm pri" href="/ui/orgs?org=`) {
		t.Error("the organization shown looks like a primary button")
	}
	if !strings.Contains(pageSpan(t, page, `class="orgsw"`, "</nav>"), `aria-current="page"><svg`) {
		t.Error("the organization shown has no check")
	}
	for _, want := range []string{`data-new="repo" href="/ui/orgs?org=alpha#projects"`, `data-account="team" href="/ui/orgs?org=alpha"`} {
		if !strings.Contains(page, want) {
			t.Errorf("menu lacks %q", want)
		}
	}

	// From a repository of Beta: the menu of that page and, afterwards, every other page.
	repo := pageAt(t, alice, base, "/ui/y/b1")
	if !strings.Contains(repo, `data-new="repo" href="/ui/orgs?org=beta#projects"`) {
		t.Error("the plus menu in a repository does not lead to its organization")
	}
	inbox := pageAt(t, alice, base, "/ui/inbox")
	for _, want := range []string{`data-new="repo" href="/ui/orgs?org=beta#projects"`, `data-account="team" href="/ui/orgs?org=beta"`, `class="opt cur" href="/ui/beta" aria-current="true"`} {
		if !strings.Contains(inbox, want) {
			t.Errorf("after working in Beta the menu lacks %q", want)
		}
	}
	if page := getOrgs(t, alice, base, ""); !strings.Contains(page, `<a href="/ui/orgs?org=beta" aria-current="page">`) {
		t.Error("the settings do not open the organization worked in last")
	}
	// A named organization wins and is remembered.
	getOrgs(t, alice, base, "?org=alpha")
	if page := pageAt(t, alice, base, "/ui/inbox"); !strings.Contains(page, `data-account="team" href="/ui/orgs?org=alpha"`) {
		t.Error("the organization named last is not remembered")
	}
}

// withJar gives a client that keeps the cookies the server sets, as a browser does.
func withJar(t *testing.T, c *http.Client, base string) *http.Client {
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
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
