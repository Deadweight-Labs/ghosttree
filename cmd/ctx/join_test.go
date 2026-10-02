package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// syncBuffer ist ein Puffer, den der Befehl und der Test gleichzeitig benutzen.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fakeTTY beantwortet Rückfragen aus einer Liste und merkt sich die Fragen.
type fakeTTY struct {
	mu      sync.Mutex
	answers []string
	asked   []string
}

func (f *fakeTTY) Ask(prompt string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, prompt)
	if len(f.answers) == 0 {
		return "", io.EOF
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	return a, nil
}

func (f *fakeTTY) Close() error { return nil }

func (f *fakeTTY) questions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

type joinFixture struct {
	tty      *fakeTTY
	opened   []string
	installs []string
	detected []string
}

// newJoinFixture isoliert Konfiguration und Umgebung und ersetzt alles, was das
// Terminal, den Browser oder die Installation berührt.
func newJoinFixture(t *testing.T, answers ...string) *joinFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SSH_CONNECTION", "")
	f := &joinFixture{tty: &fakeTTY{answers: answers}}
	oldTTY, oldOpen, oldDetect, oldInstall, oldTimeout, oldDisplay := joinTTY, joinOpenBrowser, joinDetect, joinInstall, joinTimeout, joinHasDisplay
	t.Cleanup(func() {
		joinTTY, joinOpenBrowser, joinDetect, joinInstall, joinTimeout, joinHasDisplay = oldTTY, oldOpen, oldDetect, oldInstall, oldTimeout, oldDisplay
	})
	joinTTY = func() (terminal, error) { return f.tty, nil }
	joinOpenBrowser = func(u string) error { f.opened = append(f.opened, u); return nil }
	joinDetect = func() []string { return f.detected }
	joinInstall = func(args []string, out io.Writer) int { f.installs = append(f.installs, args[0]); return 0 }
	joinHasDisplay = func() bool { return true }
	joinTimeout = 10 * time.Second
	return f
}

func TestPKCEChallengeIsS256OfTheVerifier(t *testing.T) {
	v1, c1, err := newPKCE()
	if err != nil {
		t.Fatal(err)
	}
	if len(v1) < 43 || len(v1) > 128 || !regexp.MustCompile(`^[A-Za-z0-9._~-]+$`).MatchString(v1) {
		t.Fatalf("verifier %q", v1)
	}
	sum := sha256.Sum256([]byte(v1))
	if c1 != base64.RawURLEncoding.EncodeToString(sum[:]) || len(c1) != 43 {
		t.Fatalf("challenge %q", c1)
	}
	v2, _, _ := newPKCE()
	if v1 == v2 {
		t.Fatal("verifier repeats")
	}
}

func TestStateIsValidForTheServer(t *testing.T) {
	s, err := newState()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._~-]{8,128}$`).MatchString(s) {
		t.Fatalf("state %q", s)
	}
}

func callbackGet(t *testing.T, cb *callback, method, path, host string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(method, "http://"+cb.Addr()+path, nil)
	if host != "" {
		req.Host = host
	}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestCallbackListensOnLoopbackOnlyAndChecksState(t *testing.T) {
	cb, err := startCallback("state-12345678")
	if err != nil {
		t.Fatal(err)
	}
	defer cb.Close()
	host, _, _ := net.SplitHostPort(cb.Addr())
	if host != "127.0.0.1" || cb.Port() < 1024 {
		t.Fatalf("addr %s", cb.Addr())
	}
	for _, c := range []struct{ method, path, host string }{
		{"GET", "/callback?code=abc&state=wrong-state-1", ""},
		{"GET", "/callback?code=abc", ""},
		{"GET", "/callback?state=state-12345678", ""},
		{"POST", "/callback?code=abc&state=state-12345678", ""},
		{"GET", "/other?code=abc&state=state-12345678", ""},
		{"GET", "/callback?code=abc&state=state-12345678", "evil.example"},
	} {
		if code, _, _ := callbackGet(t, cb, c.method, c.path, c.host); code == 200 {
			t.Fatalf("%+v answered 200", c)
		}
	}
	select {
	case <-cb.got:
		t.Fatal("a rejected request delivered a code")
	default:
	}
}

func TestCallbackDeliversOneCodeRedirectsToACleanURLAndShowsASelfContainedPage(t *testing.T) {
	cb, err := startCallback("state-12345678")
	if err != nil {
		t.Fatal(err)
	}
	defer cb.Close()
	type res struct {
		code int
		h    http.Header
	}
	done := make(chan res, 1)
	go func() {
		req, _ := http.NewRequest("GET", "http://"+cb.Addr()+"/callback?code=the-code&state=state-12345678", nil)
		req.Host = "localhost:" + strconv.Itoa(cb.Port())
		noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := noFollow.Do(req)
		if err != nil {
			done <- res{0, nil}
			return
		}
		resp.Body.Close()
		done <- res{resp.StatusCode, resp.Header}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := cb.Wait(ctx)
	if err != nil || code != "the-code" {
		t.Fatalf("wait: %q %v", code, err)
	}
	cb.Finish(nil)
	r := <-done
	if r.code != http.StatusSeeOther || r.h.Get("Location") != "/done" || r.h.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("callback answer: %d %v", r.code, r.h)
	}
	c, body, h := callbackGet(t, cb, "GET", "/done", "")
	if c != 200 || !strings.Contains(body, "Connected. You can close this tab.") {
		t.Fatalf("page: %d %s", c, body)
	}
	if strings.Contains(body, "http://") || strings.Contains(body, "https://") || strings.Contains(body, "src=") || strings.Contains(body, "<link") || strings.Contains(body, "the-code") {
		t.Fatalf("page loads something or shows the code: %s", body)
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") || h.Get("Cache-Control") != "no-store" || h.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("headers: %v", h)
	}
	// Einmalig: eine zweite gültige Anfrage bekommt keine Weiterleitung und liefert nichts.
	if c, _, _ := callbackGet(t, cb, "GET", "/callback?code=again&state=state-12345678", ""); c == 200 || c == 303 {
		t.Fatal("callback accepted twice")
	}
}

func TestCallbackReportsAccessDeniedOnlyForTheRightState(t *testing.T) {
	cb, err := startCallback("state-12345678")
	if err != nil {
		t.Fatal(err)
	}
	defer cb.Close()
	if c, _, _ := callbackGet(t, cb, "GET", "/callback?error=access_denied&state=wrong-state-1", ""); c == 303 || c == 200 {
		t.Fatalf("foreign denial accepted: %d", c)
	}
	go func() {
		if resp, err := http.Get("http://" + cb.Addr() + "/callback?error=access_denied&state=state-12345678"); err == nil {
			resp.Body.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cb.Wait(ctx); !errors.Is(err, errDenied) {
		t.Fatalf("wait: %v", err)
	}
	cb.Finish(errDenied)
}

func TestCallbackWaitTimesOutAndTheListenerCloses(t *testing.T) {
	cb, err := startCallback("state-12345678")
	if err != nil {
		t.Fatal(err)
	}
	addr := cb.Addr()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := cb.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait: %v", err)
	}
	cb.Close()
	if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		conn.Close()
		t.Fatal("listener still open after Close")
	}
}

func TestJoinHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"join", "--help"}, {"join", "-h"}} {
		var out bytes.Buffer
		if code := run(args, &out); code != 0 {
			t.Fatalf("%v exit %d: %s", args, code, out.String())
		}
		if !strings.Contains(out.String(), "--pair") {
			t.Fatalf("help: %s", out.String())
		}
	}
	var out bytes.Buffer
	run([]string{"help"}, &out)
	if !regexp.MustCompile(`(?m)^  join `).MatchString(out.String()) {
		t.Fatalf("ctx help does not list join:\n%s", out.String())
	}
}

func TestJoinUsageErrors(t *testing.T) {
	newJoinFixture(t)
	for _, args := range [][]string{
		{},
		{"--server", "https://x.example"},
		{"--pair", "ABCD-EFGH"},
		{"--server", "ftp://x.example", "--pair", "ABCD-EFGH"},
		{"--server", "https://x.example", "--pair", "ABCD-EFGH", "extra"},
	} {
		var out bytes.Buffer
		if code := cmdJoin(args, &out); code != 2 {
			t.Fatalf("%v exit %d: %s", args, code, out.String())
		}
	}
}

// fakeServer beantwortet Claim, Token, Poll und whoami und zählt die Aufrufe.
type fakeServer struct {
	*httptest.Server
	mu        sync.Mutex
	claims    []map[string]any
	exchanges []map[string]string
	paths     []string
	claimRes  func(map[string]any) (int, string)
	pollRes   []string
	whoami    string
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{whoami: `{"id":"person:2","label":"anna"}`}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/join/claim":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.claims = append(f.claims, body)
			code, res := f.claimRes(body)
			w.WriteHeader(code)
			io.WriteString(w, res)
		case "/api/auth/device/token":
			res := f.pollRes[0]
			if len(f.pollRes) > 1 {
				f.pollRes = f.pollRes[1:]
			}
			if strings.Contains(res, "error") {
				w.WriteHeader(400)
			}
			io.WriteString(w, res)
		case "/api/join/token":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			f.exchanges = append(f.exchanges, body)
			io.WriteString(w, `{"access_token":"tok-secret-1","token_type":"bearer","machine":"box","token_id":7}`)
		case "/api/whoami":
			if r.Header.Get("Authorization") != "Bearer tok-secret-1" {
				w.WriteHeader(401)
				return
			}
			io.WriteString(w, f.whoami)
		case "/api/orgs":
			io.WriteString(w, `[{"id":1,"slug":"alpha","name":"Alpha","default":true}]`)
		case "/api/health", "/healthz", "/health":
			io.WriteString(w, `{"ok":true}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeServer) sawPath(p string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.paths {
		if x == p {
			return true
		}
	}
	return false
}

func fallbackServer(t *testing.T) *fakeServer {
	f := newFakeServer(t)
	f.claimRes = func(map[string]any) (int, string) {
		return 200, `{"mode":"code","device_code":"dc-1","confirm_code":"7Q4K","interval":1,"expires_in":600,"token_endpoint":"/api/auth/device/token"}`
	}
	f.pollRes = []string{`{"error":"authorization_pending","interval":1}`, `{"access_token":"tok-secret-1","token_type":"bearer","machine":"box","token_id":7}`}
	return f
}

func noSleep(t *testing.T) {
	old := loginSleep
	t.Cleanup(func() { loginSleep = old })
	loginSleep = func(context.Context, time.Duration) error { return nil }
}

func readConfig(t *testing.T) (config.Config, bool) {
	t.Helper()
	if _, err := os.Stat(config.Path()); err != nil {
		return config.Config{}, false
	}
	c, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return c, true
}

func TestJoinFallbackShowsTheConfirmCodePollsAndWritesConfigAfterConfirmation(t *testing.T) {
	f := newJoinFixture(t, "y")
	noSleep(t)
	srv := fallbackServer(t)
	var out syncBuffer
	code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--name", "box", "--no-browser"}, &out)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "Type this code in your browser: 7Q4K") {
		t.Fatalf("no confirm code: %s", out.String())
	}
	claim := srv.claims[0]
	for _, k := range []string{"code_challenge", "loopback_port", "state", "loopback_host", "code_challenge_method"} {
		if _, ok := claim[k]; ok {
			t.Fatalf("fallback claim sends %s: %v", k, claim)
		}
	}
	if claim["pair"] != "abcd-efgh" || claim["machine"] != "box" {
		t.Fatalf("claim %v", claim)
	}
	if len(f.opened) != 0 {
		t.Fatalf("browser opened with --no-browser: %v", f.opened)
	}
	cfg, ok := readConfig(t)
	if !ok || cfg.Token != "tok-secret-1" || cfg.ServerURL != srv.URL || cfg.Machine != "box" {
		t.Fatalf("config %+v", cfg)
	}
	if st, _ := os.Stat(config.Path()); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	for _, secret := range []string{"tok-secret-1", "abcd-efgh", "dc-1"} {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("output leaks %q: %s", secret, out.String())
		}
	}
	if !strings.Contains(out.String(), "anna") || !strings.Contains(out.String(), "Alpha") {
		t.Fatalf("account and org not shown: %s", out.String())
	}
}

func TestJoinNeverWritesConfigWhenTheAccountIsNotConfirmed(t *testing.T) {
	newJoinFixture(t, "n")
	noSleep(t)
	srv := fallbackServer(t)
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--no-browser"}, &out); code == 0 {
		t.Fatalf("declined join exited 0: %s", out.String())
	}
	if _, ok := readConfig(t); ok {
		t.Fatal("config written without confirmation")
	}
	if strings.Contains(out.String(), "tok-secret-1") {
		t.Fatal("token printed")
	}
}

func TestJoinWithoutTerminalNeedsYes(t *testing.T) {
	newJoinFixture(t)
	joinTTY = func() (terminal, error) { return nil, errors.New("no tty") }
	noSleep(t)
	srv := fallbackServer(t)
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--no-browser"}, &out); code == 0 {
		t.Fatalf("exit 0 without tty: %s", out.String())
	}
	if len(srv.paths) != 0 {
		t.Fatalf("talked to the server before knowing it can ask: %v", srv.paths)
	}
	out = syncBuffer{}
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--no-browser", "--yes"}, &out); code != 0 {
		t.Fatalf("--yes: exit %d: %s", code, out.String())
	}
	if cfg, ok := readConfig(t); !ok || cfg.Token != "tok-secret-1" {
		t.Fatal("config missing")
	}
}

func TestJoinAsksBeforeReplacingAConfigForAnotherServer(t *testing.T) {
	newJoinFixture(t, "n")
	noSleep(t)
	if err := config.Save(config.Config{ServerURL: "https://other.example", Token: "old-token", Machine: "m"}); err != nil {
		t.Fatal(err)
	}
	srv := fallbackServer(t)
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--no-browser"}, &out); code == 0 {
		t.Fatalf("declined replace exited 0: %s", out.String())
	}
	if len(srv.paths) != 0 {
		t.Fatalf("claimed before the replace question was answered: %v", srv.paths)
	}
	if cfg, _ := readConfig(t); cfg.Token != "old-token" || cfg.ServerURL != "https://other.example" {
		t.Fatalf("config changed: %+v", cfg)
	}
}

func TestJoinReplacesAfterYesAndKeepsTheOldConfigWhenSetupIsInterrupted(t *testing.T) {
	newJoinFixture(t, "y", "y")
	noSleep(t)
	if err := config.Save(config.Config{ServerURL: "https://other.example", Token: "old-token", Machine: "m"}); err != nil {
		t.Fatal(err)
	}
	srv := fallbackServer(t)
	srv.pollRes = []string{`{"error":"expired_token"}`}
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--no-browser"}, &out); code != 1 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "Setup was interrupted. Run the command again.") {
		t.Fatalf("message: %s", out.String())
	}
	if cfg, _ := readConfig(t); cfg.Token != "old-token" {
		t.Fatalf("config changed on failure: %+v", cfg)
	}
}

func TestJoinReportsAnUnusablePairingCodeWithoutAnythingElse(t *testing.T) {
	newJoinFixture(t)
	srv := newFakeServer(t)
	srv.claimRes = func(map[string]any) (int, string) { return 400, `{"error":"invalid_pair"}` }
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--yes"}, &out); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "Pairing code") || strings.Contains(out.String(), "Waiting") {
		t.Fatalf("message: %s", out.String())
	}
}

func TestJoinOffersOnlyDetectedHarnesses(t *testing.T) {
	f := newJoinFixture(t, "y", "y", "n")
	f.detected = []string{"claude", "codex"}
	noSleep(t)
	srv := fallbackServer(t)
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--no-browser"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if strings.Join(f.installs, ",") != "claude" {
		t.Fatalf("installs %v", f.installs)
	}
	f2 := newJoinFixture(t)
	f2.detected = nil
	srv2 := fallbackServer(t)
	cmdJoin([]string{"--server", srv2.URL, "--pair", "abcd-efgh", "--no-browser", "--yes"}, &syncBuffer{})
	if len(f2.installs) != 0 {
		t.Fatalf("installed %v with nothing detected", f2.installs)
	}
}

func TestJoinYesInstallsDetectedHarnessesWithoutAsking(t *testing.T) {
	f := newJoinFixture(t)
	f.detected = []string{"claude", "codex"}
	noSleep(t)
	srv := fallbackServer(t)
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--no-browser", "--yes"}, &syncBuffer{}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.Join(f.installs, ",") != "claude,codex" || len(f.tty.questions()) != 0 {
		t.Fatalf("installs %v, questions %v", f.installs, f.tty.questions())
	}
}

func TestJoinWithoutDisplayOverSSHUsesTheFallback(t *testing.T) {
	f := newJoinFixture(t)
	t.Setenv("SSH_CONNECTION", "10.0.0.1 22 10.0.0.2 22")
	joinHasDisplay = func() bool { return false }
	noSleep(t)
	srv := fallbackServer(t)
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--yes"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if _, ok := srv.claims[0]["loopback_port"]; ok || len(f.opened) != 0 {
		t.Fatalf("loopback over ssh: %v", srv.claims[0])
	}
}

func TestJoinFallsBackWhenNoLoopbackListenerIsPossible(t *testing.T) {
	newJoinFixture(t)
	old := startCallbackFunc
	t.Cleanup(func() { startCallbackFunc = old })
	startCallbackFunc = func(string) (*callback, error) { return nil, errors.New("no sockets") }
	noSleep(t)
	srv := fallbackServer(t)
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--yes"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "Type this code in your browser: 7Q4K") {
		t.Fatalf("out: %s", out.String())
	}
}

// ---- Integration gegen den echten Server ----

type realEnv struct {
	url   string
	st    *store.Store
	clock *time.Time
}

func newRealEnv(t *testing.T) realEnv {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, n := range []string{"alice", "anna"} {
		if _, err := st.AddPerson(n); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	now := time.Now()
	clock := &now
	st.Device().SetClock(func() time.Time { return *clock })
	st.Join().SetClock(func() time.Time { return *clock })
	return realEnv{url: srv.URL, st: st, clock: clock}
}

const annaID = "person:2"

func (e realEnv) browserApproves(t *testing.T, confirm func() string) <-chan error {
	t.Helper()
	res := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			v := e.st.Join().View(annaID)
			if v.State == store.JoinClaimed {
				c := ""
				if confirm != nil {
					c = confirm()
				}
				dec, err := e.st.Join().Decide(annaID, true, v.Nonce, c, nil)
				if err != nil {
					res <- err
					return
				}
				if dec.Redirect != "" { // the browser follows the redirect to the loopback
					resp, err := http.Get(dec.Redirect)
					if err != nil {
						res <- err
						return
					}
					resp.Body.Close()
				}
				res <- nil
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		res <- errors.New("no claim seen")
	}()
	return res
}

func TestJoinIntegrationLoopbackPairsTheMachineWithTheBoundAccount(t *testing.T) {
	f := newJoinFixture(t)
	e := newRealEnv(t)
	pair, err := e.st.Join().Create(annaID)
	if err != nil {
		t.Fatal(err)
	}
	approved := e.browserApproves(t, nil)
	var out syncBuffer
	if code := cmdJoin([]string{"--server", e.url, "--pair", pair, "--name", "annas-box", "--yes"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if err := <-approved; err != nil {
		t.Fatal(err)
	}
	cfg, ok := readConfig(t)
	if !ok || cfg.Machine != "annas-box" || cfg.ServerURL != e.url {
		t.Fatalf("config %+v", cfg)
	}
	if p, ok := e.st.AuthenticatePrincipal(cfg.Token); !ok || p.ID != annaID {
		t.Fatalf("token belongs to %+v", p)
	}
	if len(f.opened) != 1 || !strings.HasSuffix(f.opened[0], "/join/pair") {
		t.Fatalf("browser: %v", f.opened)
	}
	if strings.Contains(out.String(), cfg.Token) || strings.Contains(out.String(), pair) {
		t.Fatalf("output leaks a secret: %s", out.String())
	}
	if !strings.Contains(out.String(), "anna") {
		t.Fatalf("account not shown: %s", out.String())
	}
}

func TestJoinIntegrationFallbackPairsThroughTheDeviceFlow(t *testing.T) {
	newJoinFixture(t)
	e := newRealEnv(t)
	pair, _ := e.st.Join().Create(annaID)
	out := &syncBuffer{}
	old := loginSleep
	t.Cleanup(func() { loginSleep = old })
	calls := 0
	var approved <-chan error
	loginSleep = func(_ context.Context, d time.Duration) error {
		calls++
		*e.clock = e.clock.Add(d + time.Second)
		if calls == 1 {
			approved = e.browserApproves(t, func() string {
				return regexp.MustCompile(`code in your browser: ([A-Z0-9]{4})`).FindStringSubmatch(out.String())[1]
			})
			if err := <-approved; err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	if code := cmdJoin([]string{"--server", e.url, "--pair", pair, "--name", "far-box", "--no-browser", "--yes"}, out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	cfg, ok := readConfig(t)
	if !ok {
		t.Fatalf("no config: %s", out.String())
	}
	if p, ok := e.st.AuthenticatePrincipal(cfg.Token); !ok || p.ID != annaID || p.Machine != "far-box" {
		t.Fatalf("token belongs to %+v", p)
	}
}

func TestJoinIntegrationAThiefWhoClaimedFirstGetsNothingAndTheVictimGetsNoToken(t *testing.T) {
	newJoinFixture(t)
	e := newRealEnv(t)
	pair, _ := e.st.Join().Create(annaID)
	// Der Dieb claimt zuerst, mit eigener Challenge.
	claim, _ := json.Marshal(map[string]any{"pair": pair, "machine": "thief-box",
		"code_challenge": base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "code_challenge_method": "S256",
		"loopback_port": 40999, "state": "thief-state-1"})
	resp, err := http.Post(e.url+"/api/join/claim", "application/json", bytes.NewReader(claim))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("thief claim: %v", err)
	}
	resp.Body.Close()
	var out syncBuffer
	if code := cmdJoin([]string{"--server", e.url, "--pair", pair, "--yes"}, &out); code != 1 {
		t.Fatalf("victim exit %d: %s", code, out.String())
	}
	if _, ok := readConfig(t); ok {
		t.Fatal("config written")
	}
}

func TestJoinIntegrationASecondClaimerAfterTheVictimStopsTheFlow(t *testing.T) {
	newJoinFixture(t)
	joinTimeout = 600 * time.Millisecond
	e := newRealEnv(t)
	pair, _ := e.st.Join().Create(annaID)
	go func() { // der Dieb meldet sich, nachdem der Rechner des Opfers geclaimt hat
		for i := 0; i < 400; i++ {
			if e.st.Join().View(annaID).State == store.JoinClaimed {
				body, _ := json.Marshal(map[string]any{"pair": pair, "machine": "thief"})
				if resp, err := http.Post(e.url+"/api/join/claim", "application/json", bytes.NewReader(body)); err == nil {
					resp.Body.Close()
				}
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	var out syncBuffer
	if code := cmdJoin([]string{"--server", e.url, "--pair", pair, "--yes"}, &out); code != 1 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if _, ok := readConfig(t); ok {
		t.Fatal("config written")
	}
	if v := e.st.Join().View(annaID); v.State == store.JoinApproved || v.State == store.JoinConnected {
		t.Fatalf("state %q", v.State)
	}
}

// loopbackServer ruft nach dem Claim wie ein Browser den Callback auf.
func loopbackServer(t *testing.T, callback func(port int, state string) string) *fakeServer {
	f := newFakeServer(t)
	f.claimRes = func(body map[string]any) (int, string) {
		port, _ := body["loopback_port"].(float64)
		state, _ := body["state"].(string)
		go func() {
			time.Sleep(50 * time.Millisecond)
			target := "http://127.0.0.1:" + strconv.Itoa(int(port)) + callback(int(port), state)
			noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			if resp, err := noFollow.Get(target); err == nil {
				resp.Body.Close()
			}
		}()
		return 200, `{"mode":"loopback","expires_in":600,"token_endpoint":"/api/join/token"}`
	}
	return f
}

func TestJoinLoopbackSendsS256ExplicitlyAndProvesTheVerifier(t *testing.T) {
	f := newJoinFixture(t)
	srv := loopbackServer(t, func(_ int, state string) string { return "/callback?code=auth-code&state=" + state })
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--name", "box", "--yes"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	claim := srv.claims[0]
	if claim["code_challenge_method"] != "S256" || claim["loopback_host"] != "127.0.0.1" || claim["loopback_port"].(float64) < 1024 {
		t.Fatalf("claim %v", claim)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._~-]{8,128}$`).MatchString(claim["state"].(string)) {
		t.Fatalf("state %v", claim["state"])
	}
	ex := srv.exchanges[0]
	sum := sha256.Sum256([]byte(ex["code_verifier"]))
	if ex["code"] != "auth-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != claim["code_challenge"] {
		t.Fatalf("exchange %v does not prove the challenge", ex)
	}
	if len(f.opened) != 1 || !strings.HasSuffix(f.opened[0], "/join/pair") {
		t.Fatalf("browser %v", f.opened)
	}
	if n := strings.Count(strings.TrimSpace(out.String()), "\n"); n > 8 {
		t.Fatalf("output is not short (%d lines): %s", n+1, out.String())
	}
}

func TestJoinLoopbackListenerIsGoneAfterTheCommand(t *testing.T) {
	newJoinFixture(t)
	var port int
	srv := loopbackServer(t, func(p int, state string) string { port = p; return "/callback?code=c&state=" + state })
	cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--yes"}, &syncBuffer{})
	if conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second); err == nil {
		conn.Close()
		t.Fatal("listener still open")
	}
}

func TestJoinLoopbackDeniedInTheBrowser(t *testing.T) {
	newJoinFixture(t)
	srv := loopbackServer(t, func(_ int, state string) string { return "/callback?error=access_denied&state=" + state })
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--yes"}, &out); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "Denied in the browser.") || len(srv.exchanges) != 0 {
		t.Fatalf("out %s exchanges %v", out.String(), srv.exchanges)
	}
	if _, ok := readConfig(t); ok {
		t.Fatal("config written")
	}
}

func TestJoinLoopbackTimeoutMeansInterrupted(t *testing.T) {
	newJoinFixture(t)
	joinTimeout = 150 * time.Millisecond
	srv := loopbackServer(t, func(_ int, state string) string { return "/nothing" })
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--yes"}, &out); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "Setup was interrupted. Run the command again.") {
		t.Fatalf("out %s", out.String())
	}
}

func TestJoinMachineNames(t *testing.T) {
	for in, want := range map[string]string{
		"annas-box":             "annas-box",
		"Annas MacBook.local":   "Annas-MacBook.local",
		"büro/pc:1":             "b-ro-pc-1",
		"":                      "machine",
		strings.Repeat("a", 80): strings.Repeat("a", 64),
	} {
		if got := normalizeMachine(in); got != want {
			t.Errorf("normalizeMachine(%q) = %q, want %q", in, got, want)
		}
	}
	newJoinFixture(t)
	var out syncBuffer
	if code := cmdJoin([]string{"--server", "https://x.example", "--pair", "abcd-efgh", "--name", "bad name!", "--yes"}, &out); code != 2 {
		t.Fatalf("invalid --name exit %d", code)
	}
}
