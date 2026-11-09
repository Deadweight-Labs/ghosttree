package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The invitation link the organisation page hands out (/ui/login/code?code=…) is an
// invitation link: "Create an account" takes it, and so does its bare code.
func TestRegisterTakesTheInvitationLinkOfAnOrganisation(t *testing.T) {
	srv, st, _ := testWeb(t)
	org, _ := st.CreateOrg("person:1", "Alpha", "alpha")
	code, _, err := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, link := range []string{srv.URL + "/ui/login/code?code=" + code, "  " + srv.URL + "/ui/login/code?code=" + code + "  ", code} {
		resp := sameOriginPostForm(t, c, srv.URL+"/ui/register", url.Values{"link": {link}})
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/login/code?code="+code {
			t.Errorf("%q leads to %d %q", link, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	// One answer for everything that does not work, and the link stays in the field.
	for _, link := range []string{srv.URL + "/ui/login/code?code=" + strings.Repeat("e", 64), strings.Repeat("e", 64), srv.URL + "/ui/login/code?code=nope-nope-nope-nope"} {
		resp := sameOriginPostForm(t, c, srv.URL+"/ui/register", url.Values{"link": {link}})
		page := body(t, resp)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(page, "does not work") || !strings.Contains(page, `role="alert"`) {
			t.Errorf("%q: %d %s", link, resp.StatusCode, page)
		}
	}
}

// A single use code that is used or wrong stays on the one-time-code page with the
// code in the field, and says what is wrong in a few words.
func TestOneTimeCodePageKeepsTheCodeAndNamesTheLinkWhenItFails(t *testing.T) {
	srv, st, _ := testWeb(t)
	code, _, err := st.CreateAccountCode(store.CodeLogin, "alice")
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// The link opens on a page that asks nothing but to continue.
	resp, _ := c.Get(srv.URL + "/ui/login/code?code=" + code)
	page := body(t, resp)
	if strings.Contains(page, `type="password"`) || !strings.Contains(page, `name="code" value="`+code+`"`) {
		t.Fatalf("a link should carry its code unseen: %s", page)
	}
	resp = sameOriginPostForm(t, c, srv.URL+"/ui/login/code", url.Values{"code": {code}, "codepage": {"1"}, "linked": {"1"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("redeem: %d", resp.StatusCode)
	}
	resp = sameOriginPostForm(t, c, srv.URL+"/ui/login/code", url.Values{"code": {code}, "codepage": {"1"}, "linked": {"1"}})
	page = body(t, resp)
	for _, want := range []string{"<h1>One-time code</h1>", "The link has expired or was already used.", `type="password"`, `value="` + code + `"`, `role="alert"`} {
		if !strings.Contains(page, want) {
			t.Errorf("failed link lacks %q: %s", want, page)
		}
	}
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a failed form is %d", resp.StatusCode)
	}
	// An expired link says so on opening it instead of offering a button that fails.
	resp, _ = c.Get(srv.URL + "/ui/login/code?code=" + code)
	if page := body(t, resp); !strings.Contains(page, "The link has expired or was already used.") {
		t.Errorf("a used link opens without saying so: %s", page)
	}
	// Nothing typed: ask for a code, not "invalid".
	resp = sameOriginPostForm(t, c, srv.URL+"/ui/login/code", url.Values{"code": {""}})
	if page := body(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(page, "Enter a code.") {
		t.Errorf("an empty code: %d %s", resp.StatusCode, page)
	}
}

// Whoever is signed in does not see the sign-in form; a pasted token still does,
// since that is the way to a login.
func TestSignedInPeopleDoNotSeeTheSignInForm(t *testing.T) {
	srv, st, tok := testWeb(t)
	alice := loginInteractive(t, srv, st, "alice")
	for _, path := range []string{"/ui/login", "/ui/register"} {
		resp, err := alice.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/inbox" {
			t.Errorf("%s while signed in: %d %q", path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	pasted := login(t, srv, tok)
	resp, err := pasted.Get(srv.URL + "/ui/login")
	if err != nil {
		t.Fatal(err)
	}
	if page := body(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(page, `name="code"`) {
		t.Errorf("a pasted token session gets no sign-in form: %d", resp.StatusCode)
	}
}

// "I already have an account" on the invitation page signs in and comes back to the
// invitation; the code never rides in the address of the sign-in page.
func TestInvitationPageSignInComesBackToTheInvitation(t *testing.T) {
	srv, st, _, org, _ := joinWeb(t)
	st.AddPerson("anna")
	code := projectInvite(t, st, org, store.RoleMember)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	page, _ := c.Get(srv.URL + "/join/" + code)
	text := body(t, page)
	if !strings.Contains(text, `href="/join/`+code+`/login"`) || strings.Contains(text, "/ui/login?next=") {
		t.Fatalf("the sign-in link of the invitation: %s", text)
	}
	resp, _ := c.Get(srv.URL + "/join/" + code + "/login")
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/login" {
		t.Fatalf("sign-in start: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	var next *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == nextCookie {
			next = ck
		}
	}
	if next == nil {
		t.Fatal("the invitation is not remembered")
	}
	loginCode, _, _ := st.CreateAccountCode(store.CodeLogin, "anna")
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/ui/login/code", strings.NewReader(url.Values{"code": {loginCode}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", srv.URL)
	req.AddCookie(next)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/join/"+code {
		t.Fatalf("after signing in: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestSafeNextAllowsAnInvitationPageButNothingAroundIt(t *testing.T) {
	code := strings.Repeat("ab", joinCodeLen/2)
	if got := safeNext("/join/" + code); got != "/join/"+code {
		t.Errorf("an invitation page is refused: %q", got)
	}
	for _, bad := range []string{"/join/" + code + "?x=1", "/join/" + code + "/accept", "/join/pair", "/join/" + code[:10], "/join/%61" + code[1:], "/join/" + strings.ToUpper(code)} {
		if got := safeNext(bad); got != "" {
			t.Errorf("%q is let through as %q", bad, got)
		}
	}
}

// A name that is taken or empty is said at the field, not changed behind the person's back.
func TestInvitationNameTakenIsAnErrorAtTheField(t *testing.T) {
	srv, st, _, org, _ := joinWeb(t)
	code := projectInvite(t, st, org, store.RoleGuest)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp := sameOriginPostForm(t, c, srv.URL+"/ui/login/code", url.Values{"code": {code}, "join": {"1"}, "name": {"alice"}})
	page := body(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(page, "taken or not allowed") || !strings.Contains(page, `value="alice"`) || !strings.Contains(page, `aria-invalid="true"`) {
		t.Fatalf("a taken name: %d %s", resp.StatusCode, page)
	}
	if st.CodeKindFor(code) != store.CodeInvitation && st.CodeKindFor(code) != "" {
		t.Error("the invitation changed")
	}
	if _, err := st.PreviewInvitation(code, true); err != nil {
		t.Errorf("the invitation was used up: %v", err)
	}
	resp = sameOriginPostForm(t, c, srv.URL+"/ui/login/code", url.Values{"code": {code}, "join": {"1"}, "name": {"Neu Person"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/join/pair" {
		t.Errorf("a free name: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestExpiresInSaysItInWordsAndRoundsUp(t *testing.T) {
	l := langDE
	now := overviewNow().UTC()
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Minute, "Läuft in unter einer Stunde ab"},
		{50 * time.Minute * 2, "Läuft ab in 2 Stunden"},
		{23*time.Hour + 5*time.Minute, "Läuft ab in 1 Tag"},
		{26 * time.Hour, "Läuft ab in 2 Tagen"},
		{2*24*time.Hour + 18*time.Hour, "Läuft ab in 3 Tagen"},
		{7 * 24 * time.Hour, "Läuft ab in 7 Tagen"},
	} {
		if got := expiresIn(l, now.Add(tc.in).Format(time.RFC3339)); got != tc.want {
			t.Errorf("%v -> %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestShortNameIsTheLastSegment(t *testing.T) {
	for in, want := range map[string]string{"github.com/acme/app": "app", "app": "app", "github.com/acme/app/": "app", "": ""} {
		if got := shortName(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// The first sign-in of a new instance lands in the setup, not in an empty inbox; a
// later sign-in goes where it always went.
func TestBootstrapSignInLandsInTheSetup(t *testing.T) {
	st := mustStore(t)
	code, ok, err := st.EnsureBootstrapCode()
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	file := filepath.Join(t.TempDir(), "bootstrap-code")
	if err := os.WriteFile(file, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st, WithBootstrapFile(file)))
	defer srv.Close()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp := sameOriginPostForm(t, c, srv.URL+"/ui/login/code", url.Values{"code": {code}, "name": {"robin"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/overview" {
		t.Fatalf("bootstrap sign-in: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}
