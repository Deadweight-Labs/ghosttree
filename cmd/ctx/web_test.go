package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// webFixture runs the whole server (API and web interface) with one account that
// has a device token on this machine's config.
func webFixture(t *testing.T) (*httptest.Server, *store.Store, store.TokenInfo) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	st, err := store.Open(filepath.Join(home, "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.AddAccount("alice", "", true); err != nil {
		t.Fatal(err)
	}
	token, info, err := st.CreateDeviceToken("person:1", "mainex")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(buildServerHandler(st, serveConfig{DB: filepath.Join(home, "web.db"), SnapshotLimits: snapshot.DefaultLimits()}, io.Discard))
	t.Cleanup(srv.Close)
	if err := config.Save(config.Config{ServerURL: srv.URL, Token: token, Machine: "mainex"}); err != nil {
		t.Fatal(err)
	}
	return srv, st, info
}

func signInWithLink(t *testing.T, srv *httptest.Server, link string) (*http.Response, string) {
	t.Helper()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// Opening the link only asks for the confirmation; it does not use the link up.
	for i := 0; i < 2; i++ {
		resp, err := c.Get(link)
		if err != nil {
			t.Fatal(err)
		}
		page, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(page), `name="linked"`) {
			t.Fatalf("link page (open %d): %d %s", i, resp.StatusCode, page)
		}
	}
	u, _ := url.Parse(link)
	form := url.Values{"code": {u.Query().Get("code")}, "codepage": {"1"}, "linked": {"1"}}
	req, _ := http.NewRequest("POST", srv.URL+"/ui/login/code", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", srv.URL)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp, u.Query().Get("code")
}

// REQ-676 AC-676.3 and the core of REQ-609 AC-1: ctx web signs in the web interface
// with a one-time link, short-lived and tied to the device.
func TestCtxWebMakesAOneTimeLinkBoundToTheDevice(t *testing.T) {
	srv, st, info := webFixture(t)
	var out bytes.Buffer
	if code := cmdWeb([]string{"--print", "--json"}, &out); code != 0 {
		t.Fatalf("ctx web: %d %s", code, out.String())
	}
	var res struct {
		URL       string `json:"url"`
		ExpiresIn int    `json:"expires_in"`
		Opened    bool   `json:"opened"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if !regexp.MustCompile(`^` + regexp.QuoteMeta(srv.URL) + `/ui/login/code\?code=[0-9a-f]{64}$`).MatchString(res.URL) {
		t.Fatalf("link %q", res.URL)
	}
	if res.ExpiresIn <= 0 || res.ExpiresIn > 120 || res.Opened {
		t.Fatalf("expires_in=%d opened=%v, want at most 120 seconds and nothing opened with --print", res.ExpiresIn, res.Opened)
	}
	var tokenID int64
	var machine string
	code := res.URL[strings.Index(res.URL, "code=")+5:]
	if err := st.DB().QueryRow(`SELECT token_id, machine FROM account_codes WHERE kind='login' ORDER BY rowid DESC LIMIT 1`).Scan(&tokenID, &machine); err != nil {
		t.Fatal(err)
	}
	if tokenID != info.ID || machine != "mainex" {
		t.Fatalf("link bound to token %d on %q, want %d on mainex", tokenID, machine, info.ID)
	}

	resp, _ := signInWithLink(t, srv, res.URL)
	if resp.StatusCode != http.StatusSeeOther || len(resp.Cookies()) == 0 {
		t.Fatalf("signing in: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/ui/profile", nil)
	req.AddCookie(resp.Cookies()[0])
	profile, _ := http.DefaultClient.Do(req)
	page, _ := io.ReadAll(profile.Body)
	profile.Body.Close()
	if profile.StatusCode != http.StatusOK || !strings.Contains(string(page), "alice") {
		t.Fatalf("not signed in as alice: %d", profile.StatusCode)
	}
	// Once.
	form := url.Values{"code": {code}}
	again, _ := http.NewRequest("POST", srv.URL+"/ui/login/code", strings.NewReader(form.Encode()))
	again.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	again.Header.Set("Origin", srv.URL)
	r2, _ := http.DefaultClient.Do(again)
	r2.Body.Close()
	if r2.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("the link worked twice: %d", r2.StatusCode)
	}
}

func TestCtxWebLinkOfARevokedTokenIsWorthless(t *testing.T) {
	srv, st, info := webFixture(t)
	var out bytes.Buffer
	if code := cmdWeb([]string{"--print", "--json"}, &out); code != 0 {
		t.Fatal(out.String())
	}
	var res struct{ URL string }
	json.Unmarshal(out.Bytes(), &res)
	if err := st.RevokeToken(info.ID); err != nil {
		t.Fatal(err)
	}
	resp, _ := signInWithLink(t, srv, res.URL)
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatal("a link made by a revoked device signed in")
	}
	// And a revoked device cannot ask for a new one.
	if code := cmdWeb([]string{"--print"}, &bytes.Buffer{}); code == 0 {
		t.Fatal("ctx web worked with a revoked token")
	}
}

func TestCtxWebOpensTheBrowserOnlyWhereOneCanBeOpened(t *testing.T) {
	webFixture(t)
	f := useOpener(t, "linux", map[string]string{"DISPLAY": ":0"})
	var out bytes.Buffer
	if code := cmdWeb(nil, &out); code != 0 || len(f.opened) != 1 || !strings.Contains(f.opened[0], "/ui/login/code?code=") {
		t.Fatalf("code=%d opened=%v out=%s", code, f.opened, out.String())
	}
	if strings.Contains(out.String(), "code=") {
		t.Fatalf("the link is printed although the browser opened: %s", out.String())
	}
	f2 := useOpener(t, "linux", map[string]string{"SSH_CONNECTION": "1 2 3 4"})
	out.Reset()
	if code := cmdWeb(nil, &out); code != 0 || len(f2.opened) != 0 || !strings.Contains(out.String(), "/ui/login/code?code=") || !strings.Contains(out.String(), "ssh session") {
		t.Fatalf("over ssh: code=%d opened=%v out=%s", code, f2.opened, out.String())
	}
}

func TestCtxWebWithoutAConnectedDeviceSaysSo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	var out bytes.Buffer
	if code := cmdWeb([]string{"--print"}, &out); code == 0 || !strings.Contains(out.String(), "ctx login") {
		t.Fatalf("code=%d %s", code, out.String())
	}
}

// ---------------------------------------------------------------- AC-676.7: the mail setup of ctx serve

func clearMailEnv(t *testing.T) {
	for _, k := range []string{envSMTPHost, envSMTPPort, envSMTPSecurity, envSMTPFrom, envSMTPUser, envSMTPPasswordFile, envMailDir, envPasswordLogin} {
		t.Setenv(k, "")
	}
}

func TestServeStartLineNamesTheMailMode(t *testing.T) {
	clearMailEnv(t)
	cfg, err := parseServeConfig(nil, io.Discard)
	if err != nil || cfg.Mailer != nil {
		t.Fatalf("cfg=%+v err=%v", cfg.Mail, err)
	}
	if line := mailStartLine(cfg); !strings.HasPrefix(line, "mail: off") || !strings.Contains(line, "ctx web") {
		t.Fatalf("line %q", line)
	}

	dir := filepath.Join(t.TempDir(), "mails")
	cfg, err = parseServeConfig([]string{"--mail-dir", dir, "--smtp-from", "ghosttree <noreply@example.test>", "--password-login"}, io.Discard)
	if err != nil || cfg.Mailer == nil || !cfg.PasswordLogin {
		t.Fatalf("file mailbox: %v", err)
	}
	if line := mailStartLine(cfg); !strings.Contains(line, "file mailbox "+dir) || !strings.Contains(line, "email and password: on") {
		t.Fatalf("line %q", line)
	}

	pw := filepath.Join(t.TempDir(), "smtp-pass")
	os.WriteFile(pw, []byte("hunter2\n"), 0o600)
	t.Setenv(envSMTPHost, "mail.example.test")
	t.Setenv(envSMTPFrom, "noreply@example.test")
	t.Setenv(envSMTPUser, "ghosttree")
	t.Setenv(envSMTPPasswordFile, pw)
	t.Setenv(envSMTPPort, "465")
	t.Setenv(envSMTPSecurity, "tls")
	t.Setenv(envPublicURL, "https://gt.example.test")
	cfg, err = parseServeConfig(nil, io.Discard)
	if err != nil || cfg.Mailer == nil {
		t.Fatalf("smtp from the environment: %v", err)
	}
	if line := mailStartLine(cfg); !strings.Contains(line, "SMTP mail.example.test:465 (tls)") || strings.Contains(line, "hunter2") {
		t.Fatalf("line %q", line)
	}
}

func TestServeRefusesBrokenMailSetupAtStart(t *testing.T) {
	clearMailEnv(t)
	for name, args := range map[string][]string{
		"password login without mail":   {"--password-login"},
		"smtp host without sender":      {"--smtp-host", "mail.example.test"},
		"missing password file":         {"--smtp-host", "mail.example.test", "--smtp-from", "a@example.test", "--smtp-user", "u", "--smtp-password-file", "/nonexistent/pw"},
		"password flag does not exist":  {"--smtp-password", "x"},
		"smtp without a public address": {"--smtp-host", "mail.example.test", "--smtp-from", "a@example.test"},
		"host and directory":            {"--smtp-host", "mail.example.test", "--mail-dir", t.TempDir(), "--smtp-from", "a@example.test"},
		"clear password to a remote":    {"--smtp-host", "mail.example.test", "--smtp-from", "a@example.test", "--smtp-security", "none", "--smtp-user", "u", "--smtp-password-file", "x"},
	} {
		if _, err := parseServeConfig(args, io.Discard); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A link from ctx web is asked for with the device token, and that token sits on
// every machine an agent runs on. The session it opens therefore has the rights of
// the token (reading, ordinary work), not those of a person signing in: no
// administrative forms, and it ends when the device is revoked.
func TestCtxWebSessionHasTheRightsOfTheDeviceNotOfAPerson(t *testing.T) {
	srv, st, info := webFixture(t)
	var out bytes.Buffer
	if code := cmdWeb([]string{"--print", "--json"}, &out); code != 0 {
		t.Fatal(out.String())
	}
	var res struct{ URL string }
	json.Unmarshal(out.Bytes(), &res)
	resp, _ := signInWithLink(t, srv, res.URL)
	if resp.StatusCode != http.StatusSeeOther || len(resp.Cookies()) == 0 {
		t.Fatalf("signing in: %d", resp.StatusCode)
	}
	cookie := resp.Cookies()[0]
	get := func(path string) (int, string) {
		c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.AddCookie(cookie)
		r, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		return r.StatusCode, string(b)
	}
	code, page := get("/ui/inbox")
	if code != http.StatusOK {
		t.Fatalf("inbox: %d", code)
	}
	// An interactive session is offered "New organization"; a device session is not.
	if strings.Contains(page, `data-new="org"`) {
		t.Fatal("a session from ctx web is offered administrative actions of a person")
	}
	if err := st.RevokeToken(info.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := get("/ui/profile"); code == http.StatusOK {
		t.Fatal("the session from ctx web outlived the revoked device")
	}
}
