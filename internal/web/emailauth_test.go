package web

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"mime/quotedprintable"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/mail"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// mailInstance is a web server with a file mailbox: what a local instance without
// a mail server looks like, and what the tests read the mails from.
type mailInstance struct {
	srv *httptest.Server
	st  *store.Store
	dir string
	app *app
	t   *testing.T
}

func newMailInstance(t *testing.T, passwords bool, extra ...Option) *mailInstance {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	dir := filepath.Join(t.TempDir(), "mailbox")
	sender, err := mail.New(mail.Config{Dir: dir, From: "ghosttree <noreply@example.test>"})
	if err != nil {
		t.Fatal(err)
	}
	opts := []Option{WithMail(sender), WithPasswordLogin(passwords), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))}
	h := New(st, append(opts, extra...)...)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &mailInstance{srv: srv, st: st, dir: dir, app: h.(*appHandler).app, t: t}
}

func noRedirect() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// mails returns the mails written for an address, oldest first.
func (m *mailInstance) mails(to string) []string {
	m.t.Helper()
	m.app.mailWG.Wait()
	files, _ := filepath.Glob(filepath.Join(m.dir, "*-"+strings.ToLower(to)+".eml"))
	sort.Strings(files)
	var out []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			m.t.Fatal(err)
		}
		_, body, _ := bytes.Cut(raw, []byte("\r\n\r\n"))
		text, _ := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body)))
		out = append(out, string(raw[:len(raw)-len(body)])+string(text))
	}
	return out
}

var (
	codeInMail = regexp.MustCompile(`\b([A-Z2-9]{4}-[A-Z2-9]{4})\b`)
	linkInMail = regexp.MustCompile(`https?://\S+`)
)

// lastMail returns the code and the link of the newest mail to an address.
func (m *mailInstance) lastMail(to string) (code, link, text string) {
	m.t.Helper()
	all := m.mails(to)
	if len(all) == 0 {
		m.t.Fatalf("no mail for %s in %s", to, m.dir)
	}
	text = all[len(all)-1]
	if c := codeInMail.FindStringSubmatch(text); c != nil {
		code = c[1]
	}
	link = linkInMail.FindString(text)
	return
}

func (m *mailInstance) post(c *http.Client, path string, form url.Values) *http.Response {
	m.t.Helper()
	return sameOriginPostForm(m.t, c, m.srv.URL+path, form)
}

func (m *mailInstance) account(name, email string) store.Account {
	m.t.Helper()
	a, err := m.st.AddAccount(name, email, false)
	if err != nil {
		m.t.Fatal(err)
	}
	return a
}

// signedIn reports whether a response started a session that reaches the inbox.
func (m *mailInstance) signedIn(resp *http.Response) bool {
	m.t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || len(resp.Cookies()) == 0 {
		return false
	}
	c := noRedirect()
	c.Jar = cookieJar{cookies: resp.Cookies()}
	r, err := c.Get(m.srv.URL + "/ui/inbox")
	if err != nil {
		m.t.Fatal(err)
	}
	r.Body.Close()
	return r.StatusCode == http.StatusOK
}

// ---------------------------------------------------------------- AC-676.6

func TestSignInByEmailCodeAndByMagicLink(t *testing.T) {
	m := newMailInstance(t, false)
	m.account("jonas", "jonas@example.test")
	c := noRedirect()

	// The sign-in page offers the email as the way in.
	page := body(t, getURL(t, c, m.srv.URL+"/ui/login"))
	for _, want := range []string{`name="email"`, "/ui/mail/login/request", "Send code by email"} {
		if !strings.Contains(page, want) {
			t.Errorf("sign-in page lacks %q", want)
		}
	}

	resp := m.post(c, "/ui/mail/login/request", url.Values{"email": {"Jonas@Example.test"}})
	page = body(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "j***@example.test") || !strings.Contains(page, `name="code"`) {
		t.Fatalf("after asking: %d %s", resp.StatusCode, page)
	}
	code, link, text := m.lastMail("jonas@example.test")
	if code == "" || !strings.Contains(link, "/ui/mail/login/link?t=") {
		t.Fatalf("mail lacks code or link:\n%s", text)
	}
	if !strings.Contains(text, "Subject: Your ghosttree sign-in code") || strings.Contains(text, "<img") {
		t.Fatalf("mail text:\n%s", text)
	}

	// A wrong code stays on the page; the right one signs in (typed in small letters).
	resp = m.post(c, "/ui/mail/login/code", url.Values{"email": {"jonas@example.test"}, "code": {"ZZZZ-ZZZZ"}})
	if body := body(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "does not work or has expired") {
		t.Fatalf("wrong code: %d %s", resp.StatusCode, body)
	}
	resp = m.post(c, "/ui/mail/login/code", url.Values{"email": {"jonas@example.test"}, "code": {strings.ToLower(code)}})
	if !m.signedIn(resp) {
		t.Fatalf("the code did not sign in")
	}
	// Used once: the same code does nothing the second time.
	if resp = m.post(c, "/ui/mail/login/code", url.Values{"email": {"jonas@example.test"}, "code": {code}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("code used twice: %d", resp.StatusCode)
	}

	// The magic link: opening it (a mail preview does that) burns nothing; the button does.
	m.post(c, "/ui/mail/login/request", url.Values{"email": {"jonas@example.test"}}).Body.Close()
	_, link, _ = m.lastMail("jonas@example.test")
	token := url.QueryEscape(strings.TrimPrefix(link[strings.Index(link, "?t=")+3:], ""))
	for i := 0; i < 2; i++ {
		resp = getURL(t, c, link)
		page = body(t, resp)
		if resp.StatusCode != http.StatusOK || !strings.Contains(page, `action="/ui/mail/login/link"`) || !strings.Contains(page, "j***@example.test") {
			t.Fatalf("link page (try %d): %d %s", i, resp.StatusCode, page)
		}
	}
	form, _ := url.ParseQuery("t=" + token)
	resp = m.post(c, "/ui/mail/login/link", form)
	if !m.signedIn(resp) {
		t.Fatal("the magic link did not sign in")
	}
	if resp = getURL(t, c, link); resp.StatusCode != http.StatusGone {
		t.Fatalf("a used link opens as %d", resp.StatusCode)
	}
	if resp = m.post(c, "/ui/mail/login/link", form); resp.StatusCode != http.StatusGone {
		t.Fatalf("a used link signs in again: %d", resp.StatusCode)
	}
}

func TestOneMailSignsInOnceWhetherByCodeOrByLink(t *testing.T) {
	m := newMailInstance(t, false)
	m.account("jonas", "jonas@example.test")
	c := noRedirect()
	m.post(c, "/ui/mail/login/request", url.Values{"email": {"jonas@example.test"}}).Body.Close()
	code, link, _ := m.lastMail("jonas@example.test")
	token := link[strings.Index(link, "?t=")+3:]
	if resp := m.post(c, "/ui/mail/login/code", url.Values{"email": {"jonas@example.test"}, "code": {code}}); !m.signedIn(resp) {
		t.Fatal("code did not sign in")
	}
	if resp := m.post(c, "/ui/mail/login/link", url.Values{"t": {token}}); resp.StatusCode != http.StatusGone {
		t.Fatalf("link of a used mail works: %d", resp.StatusCode)
	}
}

func TestSignInPageAnswersTheSameForAKnownAndAnUnknownAddress(t *testing.T) {
	m := newMailInstance(t, false)
	m.account("jonas", "jonas@example.test")
	c := noRedirect()
	known := m.post(c, "/ui/mail/login/request", url.Values{"email": {"jonas@example.test"}})
	// "jan" shows as j***@example.test, like "jonas": nothing on the page differs.
	unknown := m.post(c, "/ui/mail/login/request", url.Values{"email": {"jan@example.test"}})
	if known.StatusCode != unknown.StatusCode {
		t.Fatalf("status %d against %d", known.StatusCode, unknown.StatusCode)
	}
	// What differs is the address the person typed, which is echoed in a hidden field.
	typed := regexp.MustCompile(`value="[^"]*@example.test"`)
	if a, b := typed.ReplaceAllString(body(t, known), ""), typed.ReplaceAllString(body(t, unknown), ""); a != b {
		t.Fatalf("pages differ:\n%s\n----\n%s", a, b)
	}
	if len(m.mails("jonas@example.test")) != 1 || len(m.mails("jan@example.test")) != 0 {
		t.Fatal("a mail went to an address without account, or none to the account")
	}
	// A wrong code for an unknown address looks like a wrong code for a known one.
	k := m.post(c, "/ui/mail/login/code", url.Values{"email": {"jonas@example.test"}, "code": {"ZZZZ-ZZZZ"}})
	u := m.post(c, "/ui/mail/login/code", url.Values{"email": {"jan@example.test"}, "code": {"ZZZZ-ZZZZ"}})
	if k.StatusCode != u.StatusCode {
		t.Fatalf("wrong code: %d against %d", k.StatusCode, u.StatusCode)
	}
	kb, ub := body(t, k), body(t, u)
	if strings.ReplaceAll(kb, "jonas", "jan") != ub {
		t.Fatalf("wrong-code pages differ:\n%s\n----\n%s", kb, ub)
	}
}

func TestMailRequestsAreRateLimitedPerAddressAndPerSender(t *testing.T) {
	m := newMailInstance(t, false)
	m.account("jonas", "jonas@example.test")
	c := noRedirect()
	for i := 0; i < 8; i++ {
		resp := m.post(c, "/ui/mail/login/request", url.Values{"email": {"jonas@example.test"}})
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: %d (the answer for one address stays the same page)", i, resp.StatusCode)
		}
	}
	if n := len(m.mails("jonas@example.test")); n != 5 {
		t.Fatalf("%d mails for 8 requests, want 5", n)
	}
	// One sender asking for many addresses is stopped as a sender.
	limited := false
	for i := 0; i < 30 && !limited; i++ {
		resp := m.post(c, "/ui/mail/login/request", url.Values{"email": {"x" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "@example.test"}})
		resp.Body.Close()
		limited = resp.StatusCode == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("no limit per sender")
	}
}

func TestWrongCodesAreCountedPerAddressAlsoWithoutAnAccount(t *testing.T) {
	m := newMailInstance(t, false)
	c := noRedirect()
	var last int
	for i := 0; i < 12; i++ {
		resp := m.post(c, "/ui/mail/login/code", url.Values{"email": {"nobody@example.test"}, "code": {"ZZZZ-ZZZZ"}})
		resp.Body.Close()
		last = resp.StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("12 wrong codes for one address end in %d, want 429", last)
	}
}

func TestMailRoutesRefuseForeignOriginAndMailOffMeansNoRoutes(t *testing.T) {
	m := newMailInstance(t, false)
	req, _ := http.NewRequest("POST", m.srv.URL+"/ui/mail/login/request", strings.NewReader("email=jonas%40example.test"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	resp, err := noRedirect().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", resp.StatusCode)
	}
	srv, _, _ := testWeb(t) // no mail
	c := noRedirect()
	for _, p := range []string{"/ui/mail/login/request", "/ui/mail/login/code", "/ui/mail/login/link"} {
		resp := sameOriginPostForm(t, c, srv.URL+p, url.Values{"email": {"a@example.test"}})
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s without mail: %d, want 404", p, resp.StatusCode)
		}
	}
	if body := body(t, getURL(t, c, srv.URL+"/ui/login")); strings.Contains(body, `name="email"`) {
		t.Error("an instance without mail shows an email field")
	}
}

func TestMailIsInTheServerLanguage(t *testing.T) {
	m := newMailInstance(t, false, WithLanguage("de"))
	m.account("jonas", "jonas@example.test")
	m.post(noRedirect(), "/ui/mail/login/request", url.Values{"email": {"jonas@example.test"}}).Body.Close()
	_, _, text := m.lastMail("jonas@example.test")
	if !strings.Contains(text, "Dein Anmeldecode") || strings.Contains(text, "Your sign-in") {
		t.Fatalf("mail is not German:\n%s", text)
	}
}

func TestFailedMailIsLoggedWithoutTheAddress(t *testing.T) {
	var logs bytes.Buffer
	st, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.AddAccount("jonas", "jonas@example.test", false)
	h := New(st, WithMail(failingSender{}), WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp := sameOriginPostForm(t, noRedirect(), srv.URL+"/ui/mail/login/request", url.Values{"email": {"jonas@example.test"}})
	resp.Body.Close()
	h.(*appHandler).app.mailWG.Wait()
	out := logs.String()
	if !strings.Contains(out, "mail_send_failed") {
		t.Fatalf("failure not logged: %q", out)
	}
	if strings.Contains(out, "jonas") || strings.Contains(out, "example.test") {
		t.Fatalf("the log names the address: %q", out)
	}
}

type failingSender struct{}

func (failingSender) Mode() string { return "failing" }
func (failingSender) Send(m mail.Message) error {
	return errors.New("550 5.1.1 <" + m.To + "> user unknown")
}

// ---------------------------------------------------------------- AC-676.8

func TestPasswordSignInIsOffUnlessTheInstanceSwitchesItOn(t *testing.T) {
	m := newMailInstance(t, false)
	m.account("jonas", "jonas@example.test")
	resp := m.post(noRedirect(), "/ui/login/password", url.Values{"email": {"jonas@example.test"}, "password": {"whatever1"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("password sign-in on an instance without it: %d", resp.StatusCode)
	}
	if page := body(t, getURL(t, noRedirect(), m.srv.URL+"/ui/login")); strings.Contains(page, `name="password"`) {
		t.Fatal("the sign-in page shows a password field")
	}
}

func TestPasswordSetChangeSignInAndNoLeakOfAccounts(t *testing.T) {
	m := newMailInstance(t, true)
	jonas := m.account("jonas", "jonas@example.test")
	m.account("pia", "pia@example.test")
	c := loginInteractive(t, m.srv, m.st, "jonas")

	// The profile sets a password; a short one and two different ones are refused.
	set := func(form url.Values) *http.Response {
		form.Set("csrf_token", csrfFromProfile(t, m, c))
		return m.post(c, "/ui/profile/password", form)
	}
	if resp := set(url.Values{"password": {"short"}, "password2": {"short"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("short password: %d", resp.StatusCode)
	}
	if resp := set(url.Values{"password": {"long enough one"}, "password2": {"long enough two"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("different passwords: %d", resp.StatusCode)
	}
	if resp := set(url.Values{"password": {"correct horse"}, "password2": {"correct horse"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("set: %d", resp.StatusCode)
	}
	if h := m.st.PasswordHash(jonas.ID); !strings.HasPrefix(h, "$argon2id$") || strings.Contains(h, "correct") {
		t.Fatalf("stored password: %q", h)
	}
	// Changing it needs the current one.
	if resp := set(url.Values{"current": {"not it"}, "password": {"another one!"}, "password2": {"another one!"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("change with a wrong current password: %d", resp.StatusCode)
	}
	if resp := set(url.Values{"current": {"correct horse"}, "password": {"another one!"}, "password2": {"another one!"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("change: %d", resp.StatusCode)
	}

	// Signing in: the right password works, a wrong one and an unknown address give one answer.
	anon := noRedirect()
	if resp := m.post(anon, "/ui/login/password", url.Values{"email": {"JONAS@example.test"}, "password": {"another one!"}}); !m.signedIn(resp) {
		t.Fatal("the password did not sign in")
	}
	wrong := m.post(anon, "/ui/login/password", url.Values{"email": {"jonas@example.test"}, "password": {"correct horse"}})
	unknown := m.post(anon, "/ui/login/password", url.Values{"email": {"jonas2@example.test"}, "password": {"correct horse"}})
	noPassword := m.post(anon, "/ui/login/password", url.Values{"email": {"pia@example.test"}, "password": {"correct horse"}})
	statuses := []int{wrong.StatusCode, unknown.StatusCode, noPassword.StatusCode}
	if statuses[0] != http.StatusUnprocessableEntity || statuses[1] != statuses[0] || statuses[2] != statuses[0] {
		t.Fatalf("statuses %v", statuses)
	}
	b1, b2, b3 := body(t, wrong), body(t, unknown), body(t, noPassword)
	norm := func(s string) string {
		return regexp.MustCompile(`value="[^"]*@example.test"`).ReplaceAllString(s, `value=""`)
	}
	if norm(b1) != norm(b2) || norm(b1) != norm(b3) || !strings.Contains(b1, "Email or password is not right.") {
		t.Fatalf("answers differ:\n%s\n---\n%s\n---\n%s", b1, b2, b3)
	}
}

func csrfFromProfile(t *testing.T, m *mailInstance, c *http.Client) string {
	t.Helper()
	page := body(t, getURL(t, c, m.srv.URL+"/ui/profile"))
	hit := regexp.MustCompile(`name="csrf_token" value="([0-9a-f]+)"`).FindStringSubmatch(page)
	if hit == nil {
		t.Fatalf("no csrf token on the profile: %s", page)
	}
	return hit[1]
}

func TestPasswordIsRateLimitedPerAddress(t *testing.T) {
	m := newMailInstance(t, true)
	m.account("jonas", "jonas@example.test")
	var last int
	for i := 0; i < 14; i++ {
		resp := m.post(noRedirect(), "/ui/login/password", url.Values{"email": {"jonas@example.test"}, "password": {"guess number " + string(rune('a'+i))}})
		resp.Body.Close()
		last = resp.StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("14 wrong passwords end in %d, want 429", last)
	}
}

func TestPasswordResetByMailSetsAPasswordAndEndsOldSessions(t *testing.T) {
	m := newMailInstance(t, true)
	jonas := m.account("jonas", "jonas@example.test")
	hash := "$argon2id$v=19$m=65536,t=3,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := m.st.SetPassword(jonas.ID, hash); err != nil {
		t.Fatal(err)
	}
	old := loginInteractive(t, m.srv, m.st, "jonas")
	anon := noRedirect()

	if page := body(t, getURL(t, anon, m.srv.URL+"/ui/password/forgot")); !strings.Contains(page, "/ui/mail/reset/request") {
		t.Fatalf("no reset form: %s", page)
	}
	resp := m.post(anon, "/ui/mail/reset/request", url.Values{"email": {"jonas@example.test"}})
	if page := body(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(page, `name="password2"`) {
		t.Fatalf("reset page: %d %s", resp.StatusCode, page)
	}
	// An unknown address gets the same page and no mail.
	other := m.post(anon, "/ui/mail/reset/request", url.Values{"email": {"jan@example.test"}})
	if other.StatusCode != http.StatusOK || len(m.mails("jan@example.test")) != 0 {
		t.Fatal("reset for an unknown address answers differently")
	}
	other.Body.Close()
	code, link, _ := m.lastMail("jonas@example.test")
	token := link[strings.Index(link, "?t=")+3:]
	if !strings.Contains(link, "/ui/mail/reset/link") {
		t.Fatalf("link %s", link)
	}
	// Two passwords that differ do not spend the mail.
	if resp := m.post(anon, "/ui/mail/reset/code", url.Values{"email": {"jonas@example.test"}, "code": {code}, "password": {"brand new pw"}, "password2": {"brand new px"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("mismatch: %d", resp.StatusCode)
	}
	resp = m.post(anon, "/ui/mail/reset/link", url.Values{"t": {token}, "password": {"brand new pw"}, "password2": {"brand new pw"}})
	if !m.signedIn(resp) {
		t.Fatal("the reset did not sign in")
	}
	if got := m.st.PasswordHash(jonas.ID); got == hash || !strings.HasPrefix(got, "$argon2id$") {
		t.Fatalf("hash after reset: %q", got)
	}
	if resp := m.post(noRedirect(), "/ui/login/password", url.Values{"email": {"jonas@example.test"}, "password": {"brand new pw"}}); !m.signedIn(resp) {
		t.Fatal("the new password does not sign in")
	}
	// The session from before the reset is over.
	r, _ := old.Get(m.srv.URL + "/ui/inbox")
	r.Body.Close()
	if r.StatusCode == http.StatusOK {
		t.Fatal("an old session survived the password reset")
	}
	if resp := m.post(anon, "/ui/mail/reset/link", url.Values{"t": {token}, "password": {"brand new pw"}, "password2": {"brand new pw"}}); resp.StatusCode != http.StatusGone {
		t.Fatalf("reset link used twice: %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------- AC-676.9

// invite makes an organisation for alice (person:1) and an invitation to it.
func (m *mailInstance) invite(boundTo string) (code string, org store.Org) {
	m.t.Helper()
	alice := m.account("alice", "")
	org, err := m.st.CreateOrg(alice.ID, "Werkstatt Keller", "werkstatt")
	if err != nil {
		m.t.Fatal(err)
	}
	code, _, err = m.st.CreateInvitation(alice.ID, org.ID, boundTo, store.OrgMember, time.Hour)
	if err != nil {
		m.t.Fatal(err)
	}
	return code, org
}

func TestJoiningWithNameAndEmailConfirmsTheAddressByMail(t *testing.T) {
	m := newMailInstance(t, false)
	code, _ := m.invite("")
	c := noRedirect()

	// The invitation link opens the name form directly, with the address.
	page := body(t, getURL(t, c, m.srv.URL+"/ui/login/code?code="+code))
	for _, want := range []string{`name="name"`, `name="email"`, "Create account and join"} {
		if !strings.Contains(page, want) {
			t.Fatalf("invitation page lacks %q: %s", want, page)
		}
	}
	// A broken address is refused at the field, nothing is created.
	resp := m.post(c, "/ui/login/code", url.Values{"code": {code}, "codepage": {"1"}, "linked": {"1"}, "name": {"jonas"}, "email": {"nonsense"}})
	if page := body(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(page, "Enter an email address") {
		t.Fatalf("broken address: %d %s", resp.StatusCode, page)
	}
	if _, err := m.st.AccountByName("jonas"); err == nil {
		t.Fatal("an account exists after a refused form")
	}

	resp = m.post(c, "/ui/login/code", url.Values{"code": {code}, "codepage": {"1"}, "linked": {"1"}, "name": {"jonas"}, "email": {"Jonas@Example.test"}})
	if !m.signedIn(resp) {
		t.Fatalf("joining did not sign in: %d", resp.StatusCode)
	}
	jonas, err := m.st.AccountByName("jonas")
	if err != nil || jonas.Email != "" {
		t.Fatalf("account %+v err=%v: the address is not confirmed yet and must not be on the account", jonas, err)
	}
	// Until it is confirmed the address is no way in.
	m.post(noRedirect(), "/ui/mail/login/request", url.Values{"email": {"jonas@example.test"}}).Body.Close()
	if n := len(m.mails("jonas@example.test")); n != 1 {
		t.Fatalf("%d mails; only the confirmation should exist", n)
	}
	cCode, link, text := m.lastMail("jonas@example.test")
	if !strings.Contains(text, "Confirm this email address") || !strings.Contains(link, "/ui/mail/confirm/link") {
		t.Fatalf("not a confirmation mail:\n%s", text)
	}
	token := link[strings.Index(link, "?t=")+3:]
	if page := body(t, getURL(t, c, link)); !strings.Contains(page, "Confirm address") {
		t.Fatalf("confirm page: %s", page)
	}
	if a, _ := m.st.AccountByName("jonas"); a.Email != "" {
		t.Fatal("opening the link confirmed the address")
	}
	resp = m.post(c, "/ui/mail/confirm/code", url.Values{"email": {"jonas@example.test"}, "code": {cCode}})
	if page := body(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(page, "Address confirmed") {
		t.Fatalf("confirm: %d %s", resp.StatusCode, page)
	}
	_ = token
	if a, _ := m.st.AccountByName("jonas"); a.Email != "jonas@example.test" {
		t.Fatalf("email after confirmation: %q", a.Email)
	}
	// Now the address signs in.
	m.post(noRedirect(), "/ui/mail/login/request", url.Values{"email": {"jonas@example.test"}}).Body.Close()
	lc, _, _ := m.lastMail("jonas@example.test")
	if resp := m.post(noRedirect(), "/ui/mail/login/code", url.Values{"email": {"jonas@example.test"}, "code": {lc}}); !m.signedIn(resp) {
		t.Fatal("a confirmed address does not sign in")
	}
}

func TestInvitationBoundToAnAddressNeedsThatAddressBeforeAnythingExists(t *testing.T) {
	m := newMailInstance(t, false)
	code, org := m.invite("Pia@Example.test")
	c := noRedirect()
	page := body(t, getURL(t, c, m.srv.URL+"/ui/login/code?code="+code))
	if !strings.Contains(page, "p***@example.test") || strings.Contains(page, "pia@example.test") || strings.Contains(page, `name="email"`) || !strings.Contains(page, "Send me a code") {
		t.Fatalf("bound invitation page: %s", page)
	}
	resp := m.post(c, "/ui/login/code", url.Values{"code": {code}, "codepage": {"1"}, "linked": {"1"}, "name": {"pia"}})
	page = body(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "p***@example.test") || !strings.Contains(page, `name="code"`) {
		t.Fatalf("after the name: %d %s", resp.StatusCode, page)
	}
	if _, err := m.st.AccountByName("pia"); err == nil {
		t.Fatal("the account exists before the mail was confirmed")
	}
	jc, _, text := m.lastMail("pia@example.test")
	if !strings.Contains(text, "You were invited to ghosttree") {
		t.Fatalf("mail:\n%s", text)
	}
	if resp := m.post(c, "/ui/mail/join/code", url.Values{"email": {"pia@example.test"}, "code": {"ZZZZ-ZZZZ"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("wrong code: %d", resp.StatusCode)
	}
	resp = m.post(c, "/ui/mail/join/code", url.Values{"email": {"pia@example.test"}, "code": {jc}})
	if !m.signedIn(resp) {
		t.Fatalf("confirming did not join: %d", resp.StatusCode)
	}
	pia, err := m.st.AccountByName("pia")
	if err != nil || pia.Email != "pia@example.test" || m.st.OrgRole(org.ID, pia.ID) != store.OrgMember {
		t.Fatalf("pia=%+v err=%v role=%q", pia, err, m.st.OrgRole(org.ID, pia.ID))
	}
}

func TestProfileChangesTheAddressOnlyAfterConfirmation(t *testing.T) {
	m := newMailInstance(t, false)
	m.account("jonas", "alt@example.test")
	c := loginInteractive(t, m.srv, m.st, "jonas")
	csrf := csrfFromProfile(t, m, c)
	page := body(t, getURL(t, c, m.srv.URL+"/ui/profile"))
	if !strings.Contains(page, "alt@example.test") || !strings.Contains(page, "/ui/profile/email") {
		t.Fatalf("profile shows no address form: %s", page)
	}
	// Without the CSRF token nothing happens.
	if resp := m.post(c, "/ui/profile/email", url.Values{"email": {"neu@example.test"}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no csrf token: %d", resp.StatusCode)
	}
	resp := m.post(c, "/ui/profile/email", url.Values{"csrf_token": {csrf}, "email": {"neu@example.test"}})
	if page := body(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(page, "n***@example.test") {
		t.Fatalf("after changing: %d %s", resp.StatusCode, page)
	}
	if a, _ := m.st.AccountByName("jonas"); a.Email != "alt@example.test" {
		t.Fatalf("the address changed before the confirmation: %q", a.Email)
	}
	// The old address still signs in meanwhile.
	m.post(noRedirect(), "/ui/mail/login/request", url.Values{"email": {"alt@example.test"}}).Body.Close()
	if len(m.mails("alt@example.test")) != 1 {
		t.Fatal("the old address does not get its sign-in mail")
	}
	code, _, _ := m.lastMail("neu@example.test")
	resp = m.post(noRedirect(), "/ui/mail/confirm/code", url.Values{"email": {"neu@example.test"}, "code": {code}})
	resp.Body.Close()
	if a, _ := m.st.AccountByName("jonas"); a.Email != "neu@example.test" {
		t.Fatalf("after confirming: %q", a.Email)
	}
}

// ---------------------------------------------------------------- AC-676.2 / .1

func TestBoundInvitationWithoutMailExplainsItselfWithTheMaskedAddress(t *testing.T) {
	srv, st, _ := testWeb(t)
	org, _ := st.CreateOrg("person:1", "Alpha", "alpha")
	code, _, err := st.CreateInvitation("person:1", org.ID, "pia@example.test", store.OrgMember, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c := noRedirect()
	// The register page leads on instead of in a circle: the invitation page names the address and the way.
	resp := sameOriginPostForm(t, c, srv.URL+"/ui/register", url.Values{"link": {srv.URL + "/ui/login/code?code=" + code}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/login/code?code="+code {
		t.Fatalf("register: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	page := body(t, getURL(t, c, srv.URL+"/ui/login/code?code="+code))
	for _, want := range []string{"p***@example.test", "alice", "Ask alice for a new link", `role="alert"`} {
		if !strings.Contains(page, want) {
			t.Errorf("explanation lacks %q: %s", want, page)
		}
	}
	if strings.Contains(page, `name="name"`) || strings.Contains(page, "pia@example.test") {
		t.Errorf("page offers a form that cannot work, or shows the whole address: %s", page)
	}
	resp = sameOriginPostForm(t, c, srv.URL+"/ui/login/code", url.Values{"code": {code}, "codepage": {"1"}, "name": {"pia"}})
	page = body(t, resp)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(page, "p***@example.test") || !strings.Contains(page, "alice") {
		t.Fatalf("submitting: %d %s", resp.StatusCode, page)
	}
	if _, err := st.AccountByName("pia"); err == nil {
		t.Fatal("an account was created for a bound invitation without mail")
	}
}

func TestInvitationFormWithoutMailOffersNoAddress(t *testing.T) {
	srv, st, _ := testWeb(t)
	org, _ := st.CreateOrg("person:1", "Alpha", "alpha")
	code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, time.Hour)
	page := body(t, getURL(t, noRedirect(), srv.URL+"/ui/login/code?code="+code))
	if !strings.Contains(page, `name="name"`) || strings.Contains(page, `name="email"`) || strings.Contains(page, `name="link"`) {
		t.Fatalf("name form of an instance without mail: %s", page)
	}
}

func TestRejectedInvitationOfASignedInAccountNamesTheMaskedAddressAndTheWay(t *testing.T) {
	srv, st, _ := testWeb(t)
	st.AddAccount("jonas", "jonas@example.test", false)
	org, _ := st.CreateOrg("person:1", "Alpha", "alpha")
	code, _, _ := st.CreateInvitation("person:1", org.ID, "pia@example.test", store.OrgMember, time.Hour)
	c := loginInteractive(t, srv, st, "jonas")
	csrf := regexp.MustCompile(`name="csrf_token" value="([0-9a-f]+)"`).FindStringSubmatch(body(t, getURL(t, c, srv.URL+"/ui/orgs")))[1]
	resp := sameOriginPostForm(t, c, srv.URL+"/ui/orgs/accept", url.Values{"csrf_token": {csrf}, "code": {code}})
	page := body(t, resp)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, want := range []string{"p***@example.test", "alice", "ask alice for a new link"} {
		if !strings.Contains(strings.ToLower(page), strings.ToLower(want)) {
			t.Errorf("message lacks %q: %s", want, page)
		}
	}
	if strings.Contains(page, "pia@example.test") {
		t.Error("the whole address is shown")
	}
}

// ---------------------------------------------------------------- AC-676.10

func TestOwnerMakesALoginLinkForAMember(t *testing.T) {
	srv, st, _ := testWeb(t)
	org, _ := st.CreateOrg("person:1", "Alpha", "alpha")
	jonas, _ := st.AddAccount("jonas", "", false)
	code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, time.Hour)
	if _, err := st.AcceptInvitation(jonas.ID, code); err != nil {
		t.Fatal(err)
	}
	owner := loginInteractive(t, srv, st, "alice")
	page := body(t, getURL(t, owner, srv.URL+"/ui/orgs?org=alpha"))
	if !strings.Contains(page, "/ui/orgs/member/login-link") || !strings.Contains(page, "Login link") {
		t.Fatalf("the member list has no login-link button: %s", page)
	}
	csrf := regexp.MustCompile(`name="csrf_token" value="([0-9a-f]+)"`).FindStringSubmatch(page)[1]
	// Not for oneself.
	resp := sameOriginPostForm(t, owner, srv.URL+"/ui/orgs/member/login-link", url.Values{"csrf_token": {csrf}, "org": {"alpha"}, "member": {"person:1"}})
	resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatal("an owner made a login link for themselves")
	}
	resp = sameOriginPostForm(t, owner, srv.URL+"/ui/orgs/member/login-link", url.Values{"csrf_token": {csrf}, "org": {"alpha"}, "member": {jonas.ID}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("make link: %d", resp.StatusCode)
	}
	page = body(t, getURL(t, owner, srv.URL+"/ui/orgs?org=alpha"))
	link := regexp.MustCompile(`http://[^"<\s]+/ui/login/code\?code=[0-9a-f]{64}`).FindString(page)
	if link == "" {
		t.Fatalf("the link is not shown: %s", page)
	}
	// It signs in as jonas, once.
	jc := noRedirect()
	if page := body(t, getURL(t, jc, link)); !strings.Contains(page, `name="linked"`) {
		t.Fatalf("link page: %s", page)
	}
	u, _ := url.Parse(link)
	resp = sameOriginPostForm(t, jc, srv.URL+"/ui/login/code", url.Values{"code": {u.Query().Get("code")}, "codepage": {"1"}, "linked": {"1"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("redeeming: %d", resp.StatusCode)
	}
	jc.Jar = cookieJar{cookies: resp.Cookies()}
	if who := body(t, getURL(t, jc, srv.URL+"/ui/profile")); !strings.Contains(who, "jonas") {
		t.Fatalf("not signed in as jonas: %s", who)
	}
	resp = sameOriginPostForm(t, noRedirect(), srv.URL+"/ui/login/code", url.Values{"code": {u.Query().Get("code")}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("the link worked twice: %d", resp.StatusCode)
	}
	// A member sees no such button.
	member := loginInteractive(t, srv, st, "jonas")
	if page := body(t, getURL(t, member, srv.URL+"/ui/orgs?org=alpha")); strings.Contains(page, "/ui/orgs/member/login-link") {
		t.Fatal("a member is offered login links")
	}
}

func TestSignInPageNamesTheWaysBackInWhenThereIsNoMail(t *testing.T) {
	srv, st, _ := testWeb(t)
	st.AddAccount("jonas", "", false)
	page := body(t, getURL(t, noRedirect(), srv.URL+"/ui/login"))
	if !strings.Contains(page, "ctx web") || !strings.Contains(page, "owner of your organization") {
		t.Fatalf("no hint for an instance without mail: %s", page)
	}
	m := newMailInstance(t, false)
	if page := body(t, getURL(t, noRedirect(), m.srv.URL+"/ui/login")); strings.Contains(page, "ctx web") {
		t.Fatalf("an instance with mail still names ctx web: %s", page)
	}
}

// ---------------------------------------------------------------- AC-676.1 / .4

func TestWelcomeCardAfterJoiningAndAfterTheFirstAccount(t *testing.T) {
	// After joining: org name and next steps in the inbox; the card goes away when dismissed.
	srv, st, _ := testWeb(t)
	org, _ := st.CreateOrg("person:1", "Werkstatt Keller", "werkstatt")
	code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, time.Hour)
	c := noRedirect()
	resp := sameOriginPostForm(t, c, srv.URL+"/ui/login/code", url.Values{"code": {code}, "codepage": {"1"}, "linked": {"1"}, "name": {"jonas"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("join: %d", resp.StatusCode)
	}
	c.Jar = cookieJar{cookies: resp.Cookies()}
	page := body(t, getURL(t, c, srv.URL+"/ui/inbox"))
	for _, want := range []string{"You are in Werkstatt Keller", "Connect a device", "Open a repo"} {
		if !strings.Contains(page, want) {
			t.Errorf("welcome card lacks %q: %s", want, page)
		}
	}
	if strings.Contains(page, "Invite people") {
		t.Error("a member is told to invite people")
	}
	csrf := regexp.MustCompile(`name="csrf_token" value="([0-9a-f]+)"`).FindStringSubmatch(page)[1]
	resp = sameOriginPostForm(t, c, srv.URL+"/ui/welcome/dismiss", url.Values{"csrf_token": {csrf}, "org": {"1"}})
	resp.Body.Close()
	if page := body(t, getURL(t, c, srv.URL+"/ui/inbox")); strings.Contains(page, "You are in") {
		t.Error("the card stays after it was dismissed")
	}
}

func TestSetupPageOfAnEmptyInstanceSaysCreateAccountAndWelcomesTheFirstAccount(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "empty.db"))
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
	c := noRedirect()
	page := body(t, getURL(t, c, srv.URL+"/ui/login"))
	if !strings.Contains(page, ">Create account</button>") || strings.Contains(page, ">Sign in</button>") {
		t.Fatalf("the setup button is not labelled Create account: %s", page)
	}
	resp := sameOriginPostForm(t, c, srv.URL+"/ui/login/code", url.Values{"code": {bootstrap}, "name": {"robin"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/overview" {
		t.Fatalf("first account: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	c.Jar = cookieJar{cookies: resp.Cookies()}
	page = body(t, getURL(t, c, srv.URL+"/ui/overview"))
	for _, want := range []string{"You are in Default", "Invite people", "Connect a device"} {
		if !strings.Contains(page, want) {
			t.Errorf("first-account welcome lacks %q: %s", want, page)
		}
	}
}

func TestInvitationFormOffersAnAddressOnlyWhereOneCanBeConfirmed(t *testing.T) {
	srv, st, _ := testWeb(t)
	st.CreateOrg("person:1", "Alpha", "alpha")
	owner := loginInteractive(t, srv, st, "alice")
	page := body(t, getURL(t, owner, srv.URL+"/ui/orgs?org=alpha"))
	if strings.Contains(page, `name="email"`) || !strings.Contains(page, "cannot confirm email addresses") {
		t.Fatalf("an instance without mail offers the address binding: %s", page)
	}
	m := newMailInstance(t, false)
	m.account("alice", "alice@example.test")
	m.st.CreateOrg("person:1", "Alpha", "alpha")
	owner = loginInteractive(t, m.srv, m.st, "alice")
	page = body(t, getURL(t, owner, m.srv.URL+"/ui/orgs?org=alpha"))
	if !strings.Contains(page, `name="email"`) || strings.Contains(page, "cannot confirm email addresses") {
		t.Fatalf("an instance with mail hides the address binding: %s", page)
	}
}

func TestFirstAccountOfAnInstanceWithMailGivesAnAddressAndGetsAConfirmation(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "first.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	bootstrap, _, _ := st.EnsureBootstrapCode()
	file := filepath.Join(t.TempDir(), "bootstrap-code")
	os.WriteFile(file, []byte(bootstrap), 0o600)
	dir := filepath.Join(t.TempDir(), "box")
	sender, _ := mail.New(mail.Config{Dir: dir, From: "noreply@example.test"})
	h := New(st, WithBootstrapFile(file), WithMail(sender))
	srv := httptest.NewServer(h)
	defer srv.Close()
	c := noRedirect()
	if page := body(t, getURL(t, c, srv.URL+"/ui/login")); !strings.Contains(page, `name="email"`) {
		t.Fatalf("the setup page asks for no address: %s", page)
	}
	resp := sameOriginPostForm(t, c, srv.URL+"/ui/login/code", url.Values{"code": {bootstrap}, "name": {"robin"}, "email": {"robin@example.test"}})
	resp.Body.Close()
	h.(*appHandler).app.mailWG.Wait()
	files, _ := filepath.Glob(filepath.Join(dir, "*-robin@example.test.eml"))
	if resp.StatusCode != http.StatusSeeOther || len(files) != 1 {
		t.Fatalf("status %d, %d mails", resp.StatusCode, len(files))
	}
	if a, _ := st.AccountByName("robin"); a.Email != "" || st.PendingEmail(a.ID) != "robin@example.test" {
		t.Fatalf("account %+v pending %q", a, st.PendingEmail(a.ID))
	}
}
