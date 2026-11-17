package web

// REQ-686 (usability run 2: N11, N12, N20, N24, N29): the account pages say what
// is the case, the answer to an action stands at its form, a device session says
// what it is, and the account menu has no double entries.

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// part is the text of page between the first open and the next closing after it.
func part(t *testing.T, page, open, closing string) string {
	t.Helper()
	i := strings.Index(page, open)
	if i < 0 {
		t.Fatalf("page has no %q:\n%s", open, page)
	}
	rest := page[i:]
	j := strings.Index(rest, closing)
	if j < 0 {
		t.Fatalf("page has no %q after %q", closing, open)
	}
	return rest[:j]
}

// ---------------------------------------------------------------- AC-686.1

func TestProfileSaysThatTheAddressIsConfirmed(t *testing.T) {
	m := newMailInstance(t, false)
	m.account("mara", "mara@werkstatt.test")
	c := m.signInByCode("mara@werkstatt.test")
	page := body(t, getURL(t, c, m.srv.URL+"/ui/profile"))
	row := part(t, page, `<div class="row prow"><span>Email</span>`, "</div>")
	if !strings.Contains(row, "mara@werkstatt.test") || !strings.Contains(row, `class="lb ok"`) || !strings.Contains(row, "Confirmed") {
		t.Fatalf("the address row does not say it is confirmed: %s", row)
	}
}

func TestUsedConfirmationLinkOfAConfirmedAddressSaysSoAndOtherDeadLinksStayDead(t *testing.T) {
	m := newMailInstance(t, false)
	m.account("mara", "mara@werkstatt.test")
	c := m.signInByCode("mara@werkstatt.test")
	csrf := csrfFromProfile(t, m, c)

	// The address is confirmed with the code of the mail; the link of the same mail is spent.
	m.post(c, "/ui/profile/email", url.Values{"csrf_token": {csrf}, "email": {"mara.keller@werkstatt.test"}}).Body.Close()
	code, link, _ := m.lastMail("mara.keller@werkstatt.test")
	resp := m.post(c, "/ui/mail/confirm/code", url.Values{"email": {"mara.keller@werkstatt.test"}, "code": {code}})
	if page := body(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(page, "Address confirmed") {
		t.Fatalf("confirming with the code: %d %s", resp.StatusCode, page)
	}
	anon := noRedirect()
	resp = getURL(t, anon, link)
	page := body(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "Address already confirmed") || !strings.Contains(page, "m***@werkstatt.test") || strings.Contains(page, "does not work") {
		t.Fatalf("spent link of a confirmed address: %d %s", resp.StatusCode, page)
	}
	token := link[strings.Index(link, "?t=")+3:]
	resp = m.post(anon, "/ui/mail/confirm/link", url.Values{"t": {token}})
	if page := body(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(page, "Address already confirmed") {
		t.Fatalf("posting the spent link: %d %s", resp.StatusCode, page)
	}

	// A link that a newer mail replaced confirmed nothing: it stays what it was.
	m.post(c, "/ui/profile/email", url.Values{"csrf_token": {csrf}, "email": {"keller@werkstatt.test"}}).Body.Close()
	_, first, _ := m.lastMail("keller@werkstatt.test")
	m.post(c, "/ui/profile/email", url.Values{"csrf_token": {csrf}, "email": {"keller@werkstatt.test"}}).Body.Close()
	resp = getURL(t, anon, first)
	if page := body(t, resp); resp.StatusCode != http.StatusGone || !strings.Contains(page, "This link does not work") || strings.Contains(page, "already confirmed") {
		t.Fatalf("replaced link: %d %s", resp.StatusCode, page)
	}
	// A link nobody ever made says nothing about any address.
	resp = getURL(t, anon, m.srv.URL+"/ui/mail/confirm/link?t=0000")
	if page := body(t, resp); resp.StatusCode != http.StatusGone || strings.Contains(page, "already confirmed") {
		t.Fatalf("unknown link: %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------- AC-686.2

func TestPasswordAnswersStandAtThePasswordForm(t *testing.T) {
	m := newMailInstance(t, true)
	m.account("mara", "mara@werkstatt.test")
	c := m.signInByCode("mara@werkstatt.test")
	csrf := csrfFromProfile(t, m, c)

	// A refusal stands in the form it belongs to, not in the card at the left.
	resp := m.post(c, "/ui/profile/password", url.Values{"csrf_token": {csrf}, "password": {"brand new pw"}, "password2": {"brand new px"}})
	page := body(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("mismatch: %d", resp.StatusCode)
	}
	form := part(t, page, `<form class="dform pwform"`, "</form>")
	if !strings.Contains(form, "The two passwords are not the same") {
		t.Fatalf("the refusal is not in the password form: %s", form)
	}
	if aside := part(t, page, `<aside class="pfside">`, "</aside>"); strings.Contains(aside, "passwords are not") {
		t.Fatalf("the refusal stands in the card at the left: %s", aside)
	}

	// So does the confirmation.
	resp = m.post(c, "/ui/profile/password", url.Values{"csrf_token": {csrf}, "password": {"brand new pw"}, "password2": {"brand new pw"}})
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/ui/profile?notice=password_saved") {
		t.Fatalf("saving: %d %s", resp.StatusCode, loc)
	}
	page = body(t, getURL(t, c, m.srv.URL+"/ui/profile?notice=password_saved"))
	if form := part(t, page, `<form class="dform pwform"`, "</form>"); !strings.Contains(form, "Password saved") {
		t.Fatalf("the confirmation is not in the password form: %s", form)
	}
	if aside := part(t, page, `<aside class="pfside">`, "</aside>"); strings.Contains(aside, "Password saved") {
		t.Fatalf("the confirmation stands in the card at the left: %s", aside)
	}

	// The name keeps its answer where the name is.
	page = body(t, getURL(t, c, m.srv.URL+"/ui/profile?notice=saved"))
	if aside := part(t, page, `<aside class="pfside">`, "</aside>"); !strings.Contains(aside, "Saved") && !strings.Contains(aside, "saved") {
		t.Fatalf("the answer to the name is not at the name: %s", aside)
	}
}

func TestResetAndSignOutAreConfirmed(t *testing.T) {
	m := newMailInstance(t, true)
	jonas := m.account("jonas", "jonas@example.test")
	hash := "$argon2id$v=19$m=65536,t=3,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := m.st.SetPassword(jonas.ID, hash); err != nil {
		t.Fatal(err)
	}
	anon := noRedirect()
	m.post(anon, "/ui/mail/reset/request", url.Values{"email": {"jonas@example.test"}}).Body.Close()
	code, _, _ := m.lastMail("jonas@example.test")
	resp := m.post(anon, "/ui/mail/reset/code", url.Values{"email": {"jonas@example.test"}, "code": {code}, "password": {"brand new pw"}, "password2": {"brand new pw"}})
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/ui/profile?notice=password_reset") {
		t.Fatalf("after the reset: %d %s", resp.StatusCode, loc)
	}
	c := noRedirect()
	c.Jar = cookieJar{cookies: resp.Cookies()}
	page := body(t, getURL(t, c, m.srv.URL+"/ui/profile?notice=password_reset"))
	if form := part(t, page, `<form class="dform pwform"`, "</form>"); !strings.Contains(form, "Password changed") {
		t.Fatalf("no confirmation after the reset: %s", form)
	}

	// Signing out says so on the page that comes next, and only once.
	out := m.post(c, "/ui/logout", url.Values{"csrf_token": {csrfFromProfile(t, m, c)}})
	out.Body.Close()
	if loc := out.Header.Get("Location"); out.StatusCode != http.StatusSeeOther || loc != "/ui/login?notice=signed_out" {
		t.Fatalf("sign out: %d %s", out.StatusCode, loc)
	}
	page = body(t, getURL(t, noRedirect(), m.srv.URL+"/ui/login?notice=signed_out"))
	if !strings.Contains(page, `role="status" data-strip="notice"`) || !strings.Contains(page, "You are signed out.") {
		t.Fatalf("the sign-in page does not say you are signed out: %s", page)
	}
	if page = body(t, getURL(t, noRedirect(), m.srv.URL+"/ui/login")); strings.Contains(page, "You are signed out.") {
		t.Fatal("the note belongs to the address that asked for it")
	}
}

// ---------------------------------------------------------------- AC-686.3

func TestDeviceSessionSaysWhatItIsWhyAndHowToSignInForReal(t *testing.T) {
	m := newMailInstance(t, false)
	mara := m.account("mara", "mara@werkstatt.test")
	_, info, err := m.st.CreateDeviceToken(mara.ID, "mara-laptop")
	if err != nil {
		t.Fatal(err)
	}
	link, _, err := m.st.IssueWebLoginLink(mara.ID, info.ID, "mara-laptop")
	if err != nil {
		t.Fatal(err)
	}

	// The page that waits for the click says whose session it opens and what it may do.
	anon := noRedirect()
	page := body(t, getURL(t, anon, m.srv.URL+"/ui/login/code?code="+link))
	for _, want := range []string{"Continue as mara", "ctx web on mara-laptop", "everyday work"} {
		if !strings.Contains(page, want) {
			t.Errorf("one-time code page lacks %q: %s", want, page)
		}
	}
	if strings.Contains(page, "Create an account") {
		t.Error("a login link offers to create an account")
	}

	resp := m.post(anon, "/ui/login/code", url.Values{"code": {link}, "codepage": {"1"}, "linked": {"1"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("redeeming: %d", resp.StatusCode)
	}
	c := noRedirect()
	c.Jar = cookieJar{cookies: resp.Cookies()}

	for _, path := range []string{"/ui/profile", "/ui/orgs"} {
		page = body(t, getURL(t, c, m.srv.URL+path))
		note := part(t, page, `class="flash info devnote"`, "</form>")
		if !strings.Contains(note, "mara-laptop") || !strings.Contains(note, `action="/ui/logout"`) || !strings.Contains(note, "Sign in as yourself") {
			t.Errorf("%s: the banner of a device session lacks the reason or the button: %s", path, note)
		}
		if strings.Contains(page, "Read-only session") {
			t.Errorf("%s still has the old banner", path)
		}
	}

	// A switch it cannot move is not offered as one it can.
	page = body(t, getURL(t, c, m.srv.URL+"/ui/profile"))
	switches := regexp.MustCompile(`<button class="tog"[^>]*>`).FindAllString(page, -1)
	if len(switches) == 0 {
		t.Fatal("no switches on the profile")
	}
	for _, s := range switches {
		if !strings.Contains(s, " disabled") || !strings.Contains(s, `title="Needs your own sign-in"`) {
			t.Errorf("a switch of a device session looks operable: %s", s)
		}
	}
	if strings.Contains(page, `name="email"`) || strings.Contains(page, "/ui/profile/password") {
		t.Error("a device session is offered the address and password forms")
	}

	// Signed in as the person, none of this shows.
	person := m.signInByCode("mara@werkstatt.test")
	page = body(t, getURL(t, person, m.srv.URL+"/ui/profile"))
	if strings.Contains(page, "devnote") {
		t.Error("a person's own session carries the device banner")
	}
	for _, s := range regexp.MustCompile(`<button class="tog"[^>]*>`).FindAllString(page, -1) {
		if strings.Contains(s, " disabled") {
			t.Errorf("a person's switch is disabled: %s", s)
		}
	}
}

// ---------------------------------------------------------------- AC-686.4

func TestAccountMenuHasNoDoubleEntriesNamesTheThemeAndTicksTheOrganization(t *testing.T) {
	m := newMailInstance(t, false)
	alice := m.account("alice", "alice@example.test")
	if _, err := m.st.CreateOrg(alice.ID, "Alpha", "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.st.CreateOrg(alice.ID, "Beta", "beta"); err != nil {
		t.Fatal(err)
	}
	c := m.signInByCode("alice@example.test")
	page := body(t, getURL(t, c, m.srv.URL+"/ui/inbox"))
	menu := part(t, page, `<div class="pop r account-menu"`, "</details>")

	if strings.Contains(menu, "Team &amp; devices") {
		t.Error("the menu still has the entry that overlaps with Devices")
	}
	for _, want := range []string{`data-account="team"`, "Team settings", `data-account="devices"`, ">Devices<"} {
		if !strings.Contains(menu, want) {
			t.Errorf("the menu lacks %q", want)
		}
	}
	// The theme is a choice of three, and the page script marks the one that is on.
	for _, want := range []string{`data-theme-set="light"`, `data-theme-set="dark"`, `data-theme-set="system"`, `role="radiogroup"`, "<span>Light</span>", "<span>Dark</span>", "<span>System</span>"} {
		if !strings.Contains(menu, want) {
			t.Errorf("the theme choice lacks %q", want)
		}
	}
	// Only the organization that is shown has a tick.
	orgs := part(t, menu, `<div class="org-list">`, `</div>`)
	if n := strings.Count(orgs, `class="ck"`); n != 1 {
		t.Fatalf("%d ticks in the organization list, want 1: %s", n, orgs)
	}
	if tick := part(t, orgs, `aria-current="true"`, "</a>"); !strings.Contains(tick, `class="ck"`) {
		t.Errorf("the tick is not on the current organization: %s", orgs)
	}
}

func TestCardsKeepTheirWidthFromInvitationToCodeAndTheNameFieldHintsAtNoPerson(t *testing.T) {
	m := newMailInstance(t, false)
	code, _ := m.invite("pia@example.test")
	c := noRedirect()
	invite := body(t, getURL(t, c, m.srv.URL+"/ui/login/code?code="+code))
	resp := m.post(c, "/ui/login/code", url.Values{"code": {code}, "codepage": {"1"}, "linked": {"1"}, "name": {"pia"}})
	next := body(t, resp)
	const wide = `class="authbox wide"`
	if !strings.Contains(invite, wide) || !strings.Contains(next, wide) {
		t.Errorf("the width jumps between the invitation (%v) and the code page (%v)", strings.Contains(invite, wide), strings.Contains(next, wide))
	}
	open := body(t, getURL(t, c, m.srv.URL+"/ui/login/code?code="+func() string { c2, _ := m.invite2(); return c2 }()))
	for name, p := range map[string]string{"bound": invite, "open": open} {
		if strings.Contains(strings.ToLower(p), "robin") {
			t.Errorf("the %s invitation page hints at a person: %s", name, p)
		}
	}
	if !strings.Contains(open, `placeholder="Shown to your team"`) {
		t.Errorf("the name field lacks its hint: %s", open)
	}
}

// invite2 makes a second, unbound invitation in an own organization.
func (m *mailInstance) invite2() (string, store.Org) {
	m.t.Helper()
	bob := m.account("bob", "")
	org, err := m.st.CreateOrg(bob.ID, "Zweite", "zweite")
	if err != nil {
		m.t.Fatal(err)
	}
	code, _, err := m.st.CreateInvitation(bob.ID, org.ID, "", store.OrgMember, time.Hour)
	if err != nil {
		m.t.Fatal(err)
	}
	return code, org
}
