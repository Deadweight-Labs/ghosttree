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
	"syscall"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
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
	// block, wenn gesetzt, hält jede Frage an, bis der Kanal geschlossen wird.
	block chan struct{}
}

func (f *fakeTTY) Ask(prompt string) (string, error) {
	f.mu.Lock()
	f.asked = append(f.asked, prompt)
	block := f.block
	f.mu.Unlock()
	f.mu.Lock()
	exhausted := len(f.answers) == 0
	f.mu.Unlock()
	if block != nil && exhausted {
		<-block
		return "", io.EOF
	}
	f.mu.Lock()
	defer f.mu.Unlock()
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
	oldTTY, oldOpen, oldDetect, oldInstall, oldTimeout, oldDisplay, oldCan := joinTTY, joinOpenBrowser, joinDetect, joinInstall, joinTimeout, joinHasDisplay, joinCanOpen
	t.Cleanup(func() {
		joinTTY, joinOpenBrowser, joinDetect, joinInstall, joinTimeout, joinHasDisplay, joinCanOpen = oldTTY, oldOpen, oldDetect, oldInstall, oldTimeout, oldDisplay, oldCan
	})
	joinTTY = func() (terminal, error) { return f.tty, nil }
	joinOpenBrowser = func(u string) error { f.opened = append(f.opened, u); return nil }
	joinDetect = func() []string { return f.detected }
	joinInstall = func(args []string, out io.Writer) int { f.installs = append(f.installs, args[0]); return 0 }
	joinHasDisplay = func() bool { return true }
	joinCanOpen = func() bool { return true }
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
	revoked   []string
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
		case "/api/tokens/self":
			f.revoked = append(f.revoked, r.Method+" "+r.Header.Get("Authorization"))
			w.WriteHeader(204)
		case "/api/join/token":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			f.exchanges = append(f.exchanges, body)
			io.WriteString(w, `{"access_token":"tok-secret-1","token_type":"bearer","machine":"box","token_id":7}`)
		case "/api/whoami":
			if h := r.Header.Get("Authorization"); h != "Bearer tok-secret-1" && h != "Bearer old-token" {
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
	if len(srv.revoked) != 1 || srv.revoked[0] != "DELETE Bearer tok-secret-1" || !strings.Contains(out.String(), "New token revoked.") {
		t.Fatalf("issued token not revoked: %v\n%s", srv.revoked, out.String())
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
	if !strings.Contains(out.String(), "not used up") || !strings.Contains(out.String(), "ctx join --server "+srv.URL+" --pair ABCD-EFGH --no-browser --yes") {
		t.Fatalf("no ready-made command: %s", out.String())
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

// testClock is a fake clock safe to read and advance from several goroutines.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type realEnv struct {
	url   string
	st    *store.Store
	clock *testClock
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
	clock := &testClock{now: time.Now()}
	st.Device().SetClock(clock.Now)
	st.Join().SetClock(clock.Now)
	return realEnv{url: srv.URL, st: st, clock: clock}
}

// noRedirectClient stops at the 303 from /callback: the installer closes its
// listener right after the callback, so following to /done would race it.
var noRedirectClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

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
					resp, err := noRedirectClient.Get(dec.Redirect)
					if err != nil {
						res <- err
						return
					}
					resp.Body.Close()
					if resp.StatusCode != http.StatusSeeOther {
						res <- errors.New("callback did not answer 303")
						return
					}
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
		e.clock.Advance(d + time.Second)
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

func TestJoinRevokesTheIssuedTokenWhenTheConfigCannotBeWritten(t *testing.T) {
	newJoinFixture(t)
	noSleep(t)
	blocker := t.TempDir() + "/file"
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", blocker) // a file where the directory should be
	srv := fallbackServer(t)
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--no-browser", "--yes"}, &out); code != 1 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(srv.revoked) != 1 {
		t.Fatalf("revoked %v\n%s", srv.revoked, out.String())
	}
}

func TestJoinSuccessDoesNotRevoke(t *testing.T) {
	newJoinFixture(t)
	noSleep(t)
	srv := fallbackServer(t)
	cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--no-browser", "--yes"}, &syncBuffer{})
	if len(srv.revoked) != 0 {
		t.Fatalf("revoked %v", srv.revoked)
	}
}

// codeModeBrowser lets the browser approve the pairing once the terminal shows
// its confirmation code, the way a person on another device would.
func codeModeBrowser(t *testing.T, e realEnv, out *syncBuffer) {
	t.Helper()
	old := loginSleep
	t.Cleanup(func() { loginSleep = old })
	calls := 0
	loginSleep = func(_ context.Context, d time.Duration) error {
		calls++
		e.clock.Advance(d + time.Second)
		if calls == 1 {
			approved := e.browserApproves(t, func() string {
				return regexp.MustCompile(`code in your browser: ([A-Z0-9]{4})`).FindStringSubmatch(out.String())[1]
			})
			if err := <-approved; err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
}

func TestJoinIntegrationDeclinedConfirmationLeavesNoValidToken(t *testing.T) {
	f := newJoinFixture(t, "n")
	_ = f
	e := newRealEnv(t)
	pair, _ := e.st.Join().Create(annaID)
	var out syncBuffer
	codeModeBrowser(t, e, &out)
	if code := cmdJoin([]string{"--server", e.url, "--pair", pair, "--name", "annas-box", "--no-browser"}, &out); code != 1 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if _, ok := readConfig(t); ok {
		t.Fatal("config written")
	}
	toks, err := e.st.ListTokens("anna")
	if err != nil {
		t.Fatal(err)
	}
	devices := 0
	for _, tk := range toks {
		if tk.Kind != "device" {
			continue
		}
		devices++
		if tk.RevokedAt == "" {
			t.Fatalf("a valid token is left: %+v", tk)
		}
	}
	if devices == 0 {
		t.Fatal("no token was issued at all; test proves nothing")
	}
	if ms, _ := e.st.ListMachines(annaID); len(ms) != 0 {
		t.Fatalf("machine still claimed: %v", ms)
	}
}

func TestJoinStripsTerminalControlsFromEveryServerString(t *testing.T) {
	newJoinFixture(t, "n")
	noSleep(t)
	srv := fallbackServer(t)
	srv.whoami = "{\"id\":\"person:2\",\"label\":\"anna\\u001b[2K\\u001b[1AConnect as root? [y/N] \"}"
	var out syncBuffer
	cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--no-browser"}, &out)
	if strings.ContainsAny(out.String(), "\x1b\x07\x00\u009b") {
		t.Fatalf("control characters reach the terminal: %q", out.String())
	}
}

func TestJoinDoesNotPrintRawErrorBodies(t *testing.T) {
	newJoinFixture(t)
	srv := newFakeServer(t)
	srv.claimRes = func(map[string]any) (int, string) { return 500, "boom \x1b[2J internal detail" }
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--yes"}, &out); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(out.String(), "internal detail") || strings.ContainsRune(out.String(), 0x1b) {
		t.Fatalf("raw body printed: %q", out.String())
	}
}

func TestJoinPlainHTTPOnlyForLoopback(t *testing.T) {
	newJoinFixture(t)
	for _, u := range []string{"http://ghosttree.example.com", "http://10.0.0.5:8474"} {
		var out syncBuffer
		if code := cmdJoin([]string{"--server", u, "--pair", "abcd-efgh", "--yes"}, &out); code != 2 {
			t.Fatalf("%s exit %d: %s", u, code, out.String())
		}
	}
}

func TestJoinChecksThePairingCodeFormat(t *testing.T) {
	newJoinFixture(t)
	for _, pair := range []string{"abc", "abcd-efgh-ijkl", "ab cd-efgh", "abcd-\x1b[31m"} {
		var out syncBuffer
		if code := cmdJoin([]string{"--server", "https://x.example", "--pair", pair, "--yes"}, &out); code != 2 {
			t.Fatalf("%q exit %d", pair, code)
		}
	}
}

func TestJoinFallsBackOverSSHEvenWithADisplayAndWithoutABrowserLauncher(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T){
		"ssh with display": func(t *testing.T) { t.Setenv("SSH_CONNECTION", "10.0.0.1 22 10.0.0.2 22") },
		"no launcher":      func(t *testing.T) { joinCanOpen = func() bool { return false } },
		"no display":       func(t *testing.T) { joinHasDisplay = func() bool { return false } },
	} {
		newJoinFixture(t)
		setup(t)
		noSleep(t)
		srv := fallbackServer(t)
		var out syncBuffer
		if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--yes"}, &out); code != 0 {
			t.Fatalf("%s: exit %d: %s", name, code, out.String())
		}
		if _, ok := srv.claims[0]["loopback_port"]; ok {
			t.Fatalf("%s: loopback claim", name)
		}
	}
}

func TestJoinAsksBeforeReplacingAWorkingConnectionOfThisMachine(t *testing.T) {
	f := newJoinFixture(t, "n")
	srv := fallbackServer(t)
	if err := config.Save(config.Config{ServerURL: srv.URL, Token: "old-token", Machine: "box"}); err != nil {
		t.Fatal(err)
	}
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--name", "box", "--no-browser"}, &out); code != 1 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if q := f.tty.questions(); len(q) != 1 || !strings.Contains(q[0], "already connected as anna. Joining replaces that connection") {
		t.Fatalf("no warning: %v", q)
	}
	if len(srv.claims) != 0 {
		t.Fatal("claimed although the replacement was declined")
	}
	if cfg, _ := readConfig(t); cfg.Token != "old-token" {
		t.Fatalf("config %+v", cfg)
	}
}

func TestJoinSaysThePreviousConnectionIsGoneWhenTheLaterConfirmationIsDeclined(t *testing.T) {
	newJoinFixture(t, "y", "n")
	noSleep(t)
	srv := fallbackServer(t)
	if err := config.Save(config.Config{ServerURL: srv.URL, Token: "old-token", Machine: "box"}); err != nil {
		t.Fatal(err)
	}
	var out syncBuffer
	cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--name", "box", "--no-browser"}, &out)
	if strings.Contains(out.String(), "Nothing written.") && !strings.Contains(out.String(), "no longer valid") {
		t.Fatalf("hides the loss of the old connection: %s", out.String())
	}
	if !strings.Contains(out.String(), "no longer valid") {
		t.Fatalf("out: %s", out.String())
	}
}

func TestJoinAsksBeforeOverwritingAnUnreadableConfig(t *testing.T) {
	newJoinFixture(t, "n")
	if err := os.MkdirAll(strings.TrimSuffix(config.Path(), "/config.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := fallbackServer(t)
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--no-browser"}, &out); code != 1 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(srv.paths) != 0 {
		t.Fatalf("talked to the server: %v", srv.paths)
	}
	if b, _ := os.ReadFile(config.Path()); string(b) != "{not json" {
		t.Fatal("unreadable config was overwritten")
	}
}

func TestJoinSignalAtThePromptCountsAsDeclineAndRevokes(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		f := newJoinFixture(t)
		noSleep(t)
		srv := fallbackServer(t)
		f.tty.mu.Lock()
		f.tty.block = make(chan struct{})
		f.tty.mu.Unlock()
		go func() {
			for i := 0; i < 400; i++ {
				if len(f.tty.questions()) > 0 {
					syscall.Kill(os.Getpid(), sig)
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
		done := make(chan int, 1)
		var out syncBuffer
		go func() {
			done <- cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--no-browser"}, &out)
		}()
		select {
		case code := <-done:
			if code != 1 {
				t.Fatalf("%v: exit %d: %s", sig, code, out.String())
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%v: the prompt blocked the command", sig)
		}
		close(f.tty.block)
		if len(srv.revoked) != 1 {
			t.Fatalf("%v: revoked %v\n%s", sig, srv.revoked, out.String())
		}
		if _, ok := readConfig(t); ok {
			t.Fatalf("%v: config written", sig)
		}
	}
}

func TestJoinIntegrationDeclinedRejoinKeepsTheMachineWithItsOwner(t *testing.T) {
	newJoinFixture(t, "y", "n") // replace the working connection, then decline the account
	e := newRealEnv(t)
	if _, err := e.st.AddPerson("bob"); err != nil {
		t.Fatal(err)
	}
	oldTok, _, err := e.st.CreateDeviceToken(annaID, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(config.Config{ServerURL: e.url, Token: oldTok, Machine: "laptop"}); err != nil {
		t.Fatal(err)
	}
	pair, _ := e.st.Join().Create(annaID)
	var out syncBuffer
	codeModeBrowser(t, e, &out)
	if code := cmdJoin([]string{"--server", e.url, "--pair", pair, "--name", "laptop", "--no-browser"}, &out); code != 1 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if _, _, err := e.st.CreateDeviceToken("person:3", "laptop"); !errors.Is(err, store.ErrMachineTaken) {
		t.Fatalf("another account could take the machine: %v", err)
	}
	if !strings.Contains(out.String(), "no longer valid") {
		t.Fatalf("loss of the old connection not reported: %s", out.String())
	}
}

func TestJoinStripsBidiAndZeroWidthFormatCharacters(t *testing.T) {
	newJoinFixture(t, "n")
	noSleep(t)
	srv := fallbackServer(t)
	srv.whoami = "{\"id\":\"person:2\",\"label\":\"an\\u202ena\\u200b\\u2066\"}"
	var out syncBuffer
	cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--no-browser"}, &out)
	if strings.ContainsAny(out.String(), "\u202e\u200b\u2066") || !strings.Contains(out.String(), "Account anna") {
		t.Fatalf("out %q", out.String())
	}
}

func TestJoinSignalDuringTheInstallQuestionsStopsTheLoop(t *testing.T) {
	f := newJoinFixture(t, "y")
	f.detected = []string{"claude", "codex"}
	noSleep(t)
	srv := fallbackServer(t)
	f.tty.mu.Lock()
	f.tty.block = make(chan struct{})
	f.tty.mu.Unlock()
	go func() {
		for i := 0; i < 400; i++ {
			if len(f.tty.questions()) >= 2 {
				syscall.Kill(os.Getpid(), syscall.SIGINT)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	done := make(chan int, 1)
	var out syncBuffer
	go func() {
		done <- cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--no-browser"}, &out)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("blocked")
	}
	close(f.tty.block)
	if q := f.tty.questions(); len(q) != 2 || len(f.installs) != 0 {
		t.Fatalf("questions %v installs %v", q, f.installs)
	}
	if !strings.Contains(out.String(), "Interrupted") {
		t.Fatalf("out %s", out.String())
	}
}

func TestJoinAsksAlsoWhenTheNewNameDiffersOnTheSameServer(t *testing.T) {
	f := newJoinFixture(t, "n")
	srv := fallbackServer(t)
	if err := config.Save(config.Config{ServerURL: srv.URL, Token: "old-token", Machine: "box"}); err != nil {
		t.Fatal(err)
	}
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--name", "other", "--no-browser"}, &out); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if q := f.tty.questions(); len(q) != 1 || !strings.Contains(q[0], "already connected as anna") || len(srv.claims) != 0 {
		t.Fatalf("questions %v claims %v", q, srv.claims)
	}
}

func TestJoinWaitsAsLongAsASessionLives(t *testing.T) {
	if joinDefaultTimeout != 30*time.Minute || joinTimeout != joinDefaultTimeout {
		t.Fatalf("default wait %v / %v", joinDefaultTimeout, joinTimeout)
	}
}

// interruptWhenClaimed schickt dem Prozess ein Strg-C, sobald der Server die
// Anfrage dieses Installers sieht.
func (e realEnv) interruptWhenClaimed(t *testing.T) {
	t.Helper()
	go func() {
		for i := 0; i < 800; i++ {
			if e.st.Join().View(annaID).State == store.JoinClaimed {
				syscall.Kill(os.Getpid(), syscall.SIGINT)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
}

func resumeFile(t *testing.T) (os.FileInfo, string) {
	t.Helper()
	st, err := os.Stat(joinResumePath())
	if err != nil {
		return nil, ""
	}
	b, _ := os.ReadFile(joinResumePath())
	return st, string(b)
}

// approveRerun gibt frei, sobald die Anfrage eine andere Kennung trägt als vor dem
// Abbruch (also der erneute Claim angekommen ist).
func (e realEnv) approveRerun(t *testing.T, oldNonce string, confirm func() string) <-chan error {
	t.Helper()
	res := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			v := e.st.Join().View(annaID)
			if v.State == store.JoinClaimed && v.Nonce != oldNonce {
				c := ""
				for c == "" && confirm != nil && time.Now().Before(deadline) {
					c = confirm()
					time.Sleep(5 * time.Millisecond)
				}
				dec, err := e.st.Join().Decide(annaID, true, v.Nonce, c, nil)
				if err != nil {
					res <- err
					return
				}
				if dec.Redirect != "" {
					resp, err := noRedirectClient.Get(dec.Redirect)
					if err != nil {
						res <- err
						return
					}
					resp.Body.Close()
					if resp.StatusCode != http.StatusSeeOther {
						res <- errors.New("callback did not answer 303")
						return
					}
				}
				res <- nil
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		res <- errors.New("no second claim seen")
	}()
	return res
}

// Strg-C nach dem Claim: der nächste Aufruf mit demselben Code weist sich mit dem
// lokal abgelegten Token aus und verbrennt den Code nicht, in beiden Wegen.
func TestJoinIntegrationRerunAfterCtrlCResumesWithTheLocalToken(t *testing.T) {
	for _, mode := range []string{"loopback", "code"} {
		t.Run(mode, func(t *testing.T) {
			newJoinFixture(t)
			e := newRealEnv(t)
			pair, _ := e.st.Join().Create(annaID)
			args := []string{"--server", e.url, "--pair", pair, "--name", "annas-box", "--yes"}
			out := &syncBuffer{}
			var confirm func() string
			if mode == "code" {
				args = append(args, "--no-browser")
				old := loginSleep
				t.Cleanup(func() { loginSleep = old })
				loginSleep = func(ctx context.Context, d time.Duration) error {
					e.clock.Advance(d + time.Second)
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(5 * time.Millisecond):
						return nil
					}
				}
				confirm = func() string {
					m := regexp.MustCompile(`code in your browser: ([A-Z0-9]{4})`).FindStringSubmatch(out.String())
					if m == nil {
						return ""
					}
					return m[1]
				}
			}
			e.interruptWhenClaimed(t)
			if code := cmdJoin(args, out); code != 1 {
				t.Fatalf("first run exit %d: %s", code, out.String())
			}
			info, content := resumeFile(t)
			if info == nil {
				t.Fatal("no resume token kept after Ctrl-C")
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("resume file mode %v", info.Mode().Perm())
			}
			if strings.Contains(content, pair) || strings.Contains(content, strings.ReplaceAll(pair, "-", "")) {
				t.Fatalf("the pairing code is stored in the clear: %s", content)
			}
			v := e.st.Join().View(annaID)
			if v.State != store.JoinClaimed {
				t.Fatalf("state after Ctrl-C %q", v.State)
			}
			out = &syncBuffer{}
			approved := e.approveRerun(t, v.Nonce, confirm)
			if code := cmdJoin(args, out); code != 0 {
				t.Fatalf("rerun exit %d: %s", code, out.String())
			}
			if err := <-approved; err != nil {
				t.Fatal(err)
			}
			if _, content := resumeFile(t); content != "" {
				t.Fatalf("resume token left after success: %s", content)
			}
			cfg, ok := readConfig(t)
			if !ok {
				t.Fatal("no config")
			}
			if p, ok := e.st.AuthenticatePrincipal(cfg.Token); !ok || p.ID != annaID {
				t.Fatalf("token belongs to %+v", p)
			}
		})
	}
}

// Ohne das Token (Datei weg, anderer Rechner) gilt derselbe Name und dasselbe
// Netz nicht als Beweis: der zweite Claim sperrt die Sitzung.
func TestJoinIntegrationRerunWithoutTheLocalTokenIsASecondClaim(t *testing.T) {
	newJoinFixture(t)
	e := newRealEnv(t)
	pair, _ := e.st.Join().Create(annaID)
	args := []string{"--server", e.url, "--pair", pair, "--name", "annas-box", "--yes"}
	out := &syncBuffer{}
	e.interruptWhenClaimed(t)
	if code := cmdJoin(args, out); code != 1 {
		t.Fatalf("first run exit %d: %s", code, out.String())
	}
	clearJoinResume()
	out = &syncBuffer{}
	if code := cmdJoin(args, out); code != 1 {
		t.Fatalf("rerun without the token exit %d: %s", code, out.String())
	}
	if v := e.st.Join().View(annaID); v.State != store.JoinCompromised {
		t.Fatalf("state %q", v.State)
	}
	if _, ok := readConfig(t); ok {
		t.Fatal("config written")
	}
}

func TestJoinResumeTokenIsBoundToServerAndCodeAndExpires(t *testing.T) {
	newJoinFixture(t)
	saveJoinResume("https://gt.example", "abcd-efgh", "tok-1")
	if got := loadJoinResume("https://gt.example", "ABCD-EFGH"); got != "tok-1" {
		t.Fatalf("same code, other spelling: %q", got)
	}
	for name, args := range map[string][2]string{"other server": {"https://evil.example", "abcd-efgh"}, "other code": {"https://gt.example", "wxyz-1234"}} {
		if got := loadJoinResume(args[0], args[1]); got != "" {
			t.Errorf("%s got the token", name)
		}
	}
	b, _ := os.ReadFile(joinResumePath())
	var r joinResume
	json.Unmarshal(b, &r)
	r.Expires = time.Now().Add(-time.Second).Unix()
	b, _ = json.Marshal(r)
	os.WriteFile(joinResumePath(), b, 0o600)
	if got := loadJoinResume("https://gt.example", "abcd-efgh"); got != "" {
		t.Fatal("an expired token was used")
	}
}

// Jeder Ausgang außer Strg-C räumt das Token weg; ein Server ohne resume (alt)
// hinterlässt keine Datei.
func TestJoinClearsTheResumeTokenOnEveryOtherOutcome(t *testing.T) {
	f := newJoinFixture(t)
	_ = f
	srv := loopbackServer(t, func(_ int, state string) string { return "/callback?error=access_denied&state=" + state })
	srv.claimRes = func(body map[string]any) (int, string) {
		port, _ := body["loopback_port"].(float64)
		state, _ := body["state"].(string)
		go func() {
			time.Sleep(50 * time.Millisecond)
			if resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(int(port)) + "/callback?error=access_denied&state=" + state); err == nil {
				resp.Body.Close()
			}
		}()
		return 200, `{"mode":"loopback","expires_in":600,"resume":"tok-xyz"}`
	}
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--name", "box", "--yes"}, &out); code != 1 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if _, content := resumeFile(t); content != "" {
		t.Fatalf("resume token left after a denial: %s", content)
	}
}

func TestJoinListsWhatItSetsUpBeforeAskingAndAfterInstalling(t *testing.T) {
	f := newJoinFixture(t, "y", "y", "y")
	f.detected = []string{"claude", "codex"}
	noSleep(t)
	srv := fallbackServer(t)
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--no-browser"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	got := out.String()
	for _, want := range []string{"Will set up:\n  Claude hooks\n  Claude MCP server\n  Claude skills\n  CLAUDE.md section\n", "Will set up:\n  Codex hooks", "Set up:\n  Claude hooks", "  CLAUDE.md section\n  Codex hooks", "AGENTS.md section"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Index(got, "Will set up:") > strings.Index(got, "\nSet up:") {
		t.Fatalf("the confirmation list must come first:\n%s", got)
	}
}

func TestJoinYesPrintsTheSetUpListWithoutAConfirmationList(t *testing.T) {
	f := newJoinFixture(t)
	f.detected = []string{"codex"}
	noSleep(t)
	srv := fallbackServer(t)
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--no-browser", "--yes"}, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(out.String(), "Will set up:") || !strings.Contains(out.String(), "Set up:\n  Codex hooks\n") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestJoinSetUpListSkipsADeclinedHarness(t *testing.T) {
	f := newJoinFixture(t, "y", "n")
	f.detected = []string{"claude"}
	noSleep(t)
	srv := fallbackServer(t)
	var out syncBuffer
	cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--no-browser"}, &out)
	if strings.Contains(out.String(), "\nSet up:") {
		t.Fatalf("declined install listed as set up:\n%s", out.String())
	}
}

func TestJoinLoopbackStillAsksInTheTerminalWhichAccountTheMachineBecomes(t *testing.T) {
	f := newJoinFixture(t, "y")
	e := newRealEnv(t)
	pair, _ := e.st.Join().Create(annaID)
	approved := e.browserApproves(t, nil)
	var out syncBuffer
	if code := cmdJoin([]string{"--server", e.url, "--pair", pair, "--name", "annas-box"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if err := <-approved; err != nil {
		t.Fatal(err)
	}
	asked := strings.Join(f.tty.questions(), "|")
	if !strings.Contains(asked, "Connect this machine as account anna? [Y/n]") {
		t.Fatalf("no account question with a yes default: %q", asked)
	}
	if _, ok := readConfig(t); !ok {
		t.Fatalf("config not written: %s", out.String())
	}
}

func TestJoinLoopbackDefaultsToYesOnEnter(t *testing.T) {
	newJoinFixture(t, "")
	e := newRealEnv(t)
	pair, _ := e.st.Join().Create(annaID)
	approved := e.browserApproves(t, nil)
	var out syncBuffer
	if code := cmdJoin([]string{"--server", e.url, "--pair", pair, "--name", "annas-box"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	<-approved
	if _, ok := readConfig(t); !ok {
		t.Fatalf("config not written: %s", out.String())
	}
}

func TestJoinLoopbackDeclinedInTheTerminalWritesNothingAndRevokes(t *testing.T) {
	newJoinFixture(t, "n")
	e := newRealEnv(t)
	pair, _ := e.st.Join().Create(annaID)
	approved := e.browserApproves(t, nil)
	var out syncBuffer
	if code := cmdJoin([]string{"--server", e.url, "--pair", pair, "--name", "annas-box"}, &out); code != 1 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	<-approved
	if _, ok := readConfig(t); ok {
		t.Fatal("config written after declining")
	}
	if !strings.Contains(out.String(), "New token revoked") {
		t.Errorf("token not revoked:\n%s", out.String())
	}
	if v := e.st.Join().View(annaID); v.State != store.JoinDenied {
		t.Fatalf("browser state = %q, want %q", v.State, store.JoinDenied)
	}
}

func TestJoinSaysToTrustTheCodexHooksAfterwards(t *testing.T) {
	f := newJoinFixture(t)
	f.detected = []string{"codex"}
	noSleep(t)
	srv := fallbackServer(t)
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--no-browser", "--yes"}, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "run /hooks in Codex to trust the ghosttree hooks") {
		t.Fatalf("no /hooks notice:\n%s", out.String())
	}
}

func TestJoinCodeModeStillAsksWhichAccountTheMachineBecomes(t *testing.T) {
	f := newJoinFixture(t, "y")
	e := newRealEnv(t)
	pair, _ := e.st.Join().Create(annaID)
	var out syncBuffer
	codeModeBrowser(t, e, &out)
	if code := cmdJoin([]string{"--server", e.url, "--pair", pair, "--name", "annas-box", "--no-browser"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	asked := strings.Join(f.tty.questions(), "|")
	if !strings.Contains(asked, "Connect this machine as account anna?") {
		t.Fatalf("no account question: %q", asked)
	}
}

func TestJoinDeclinedInTheTerminalMakesTheBrowserSayNothingWasConnected(t *testing.T) {
	newJoinFixture(t, "n")
	e := newRealEnv(t)
	pair, _ := e.st.Join().Create(annaID)
	var out syncBuffer
	codeModeBrowser(t, e, &out)
	if code := cmdJoin([]string{"--server", e.url, "--pair", pair, "--name", "annas-box", "--no-browser"}, &out); code != 1 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if v := e.st.Join().View(annaID); v.State != store.JoinDenied {
		t.Fatalf("browser state after a declined join = %q, want %q", v.State, store.JoinDenied)
	}
}

func TestJoinPicksAFreeMachineNameWhenTheHostnameBelongsToAnotherAccount(t *testing.T) {
	newJoinFixture(t)
	host, _ := os.Hostname()
	taken := normalizeMachine(host)
	e := newRealEnv(t)
	if err := e.st.ClaimMachine(taken, "person:1"); err != nil {
		t.Fatal(err)
	}
	pair, _ := e.st.Join().Create(annaID)
	approved := e.browserApproves(t, nil)
	var out syncBuffer
	if code := cmdJoin([]string{"--server", e.url, "--pair", pair, "--yes"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if err := <-approved; err != nil {
		t.Fatal(err)
	}
	cfg, ok := readConfig(t)
	if !ok || cfg.Machine == taken || !strings.HasPrefix(cfg.Machine, strings.ToLower(taken)) {
		t.Fatalf("machine = %q (host name %q was taken)", cfg.Machine, taken)
	}
	if !strings.Contains(out.String(), "machine "+cfg.Machine) {
		t.Fatalf("chosen name not shown: %s", out.String())
	}
}

func TestJoinWithATakenExplicitNameStopsAtOnceWithTheExactCommand(t *testing.T) {
	newJoinFixture(t)
	e := newRealEnv(t)
	if err := e.st.ClaimMachine("shared", "person:1"); err != nil {
		t.Fatal(err)
	}
	pair, _ := e.st.Join().Create(annaID)
	var out syncBuffer
	if code := cmdJoin([]string{"--server", e.url, "--pair", pair, "--name", "shared", "--yes"}, &out); code != 1 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	text := out.String()
	want := "join --server " + e.url + " --pair " + pair + " --name shared-2 --yes"
	if !strings.Contains(text, "already belongs to another account") || !strings.Contains(text, "is not used up") || !strings.Contains(text, want) {
		t.Fatalf("message lacks the command %q:\n%s", want, text)
	}
	if _, ok := readConfig(t); ok {
		t.Fatal("config written")
	}
	// The command it printed works with the same code.
	approved := e.browserApproves(t, nil)
	var again syncBuffer
	if code := cmdJoin([]string{"--server", e.url, "--pair", pair, "--name", "shared-2", "--yes"}, &again); code != 0 {
		t.Fatalf("retry exit %d: %s", code, again.String())
	}
	if err := <-approved; err != nil {
		t.Fatal(err)
	}
}

func TestJoinWithoutAnyHarnessSaysWhatToDo(t *testing.T) {
	f := newJoinFixture(t)
	f.detected = nil
	srv := loopbackServer(t, func(_ int, state string) string { return "/callback?code=auth-code&state=" + state })
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--name", "box", "--yes"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	text := out.String()
	if !strings.Contains(text, "Claude Code and Codex were not found") || !strings.Contains(text, "install claude") {
		t.Fatalf("no guidance:\n%s", text)
	}
	if strings.Contains(text, "claude      not installed") {
		t.Fatalf("raw status line left in:\n%s", text)
	}
}

func TestJoinPassesNoWatchToTheInstaller(t *testing.T) {
	f := newJoinFixture(t)
	f.detected = []string{"claude"}
	var got [][]string
	joinInstall = func(args []string, out io.Writer) int { got = append(got, args); return 0 }
	srv := loopbackServer(t, func(_ int, state string) string { return "/callback?code=auth-code&state=" + state })
	var out syncBuffer
	if code := cmdJoin([]string{"--server", srv.URL, "--pair", "abcd-efgh", "--name", "box", "--yes", "--no-watch"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(got) != 1 || strings.Join(got[0], " ") != "claude --no-watch" {
		t.Fatalf("installer args = %v", got)
	}
}

func TestCallbackRejectsEmbeddingFetchesAndCrossOriginRequestsAndForbidsFraming(t *testing.T) {
	cb, err := startCallback("state-12345678")
	if err != nil {
		t.Fatal(err)
	}
	defer cb.Close()
	const path = "/callback?code=abc&state=state-12345678"
	for _, hdr := range []map[string]string{
		{"Sec-Fetch-Dest": "iframe", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Site": "cross-site"},
		{"Sec-Fetch-Dest": "image", "Sec-Fetch-Mode": "no-cors", "Sec-Fetch-Site": "cross-site"},
		{"Sec-Fetch-Mode": "cors", "Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"},
		{"Origin": "https://evil.example"},
		{"Sec-Fetch-Site": "bogus"},
	} {
		req, _ := http.NewRequest("GET", "http://"+cb.Addr()+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%v answered %d, want 403", hdr, resp.StatusCode)
		}
		if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%v: CSP %q lacks frame-ancestors", hdr, csp)
		}
	}
	select {
	case <-cb.got:
		t.Fatal("a rejected request delivered a code")
	default:
	}
	// The real redirect after the approval: a cross-site top-level navigation.
	done := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest("GET", "http://"+cb.Addr()+path, nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Sec-Fetch-Dest", "document")
		noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := noFollow.Do(req)
		if err != nil {
			done <- 0
			return
		}
		resp.Body.Close()
		done <- resp.StatusCode
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if code, err := cb.Wait(ctx); err != nil || code != "abc" {
		t.Fatalf("navigation not accepted: %q %v", code, err)
	}
	cb.Finish(nil)
	if c := <-done; c != http.StatusSeeOther {
		t.Fatalf("navigation answered %d", c)
	}
}

func TestCallbackAcceptsAnOriginHeaderOnARealNavigationOnly(t *testing.T) {
	cb, err := startCallback("state-12345678")
	if err != nil {
		t.Fatal(err)
	}
	defer cb.Close()
	mk := func(h map[string]string) *http.Request {
		req, _ := http.NewRequest("GET", "http://"+cb.Addr()+"/callback?code=abc&state=state-12345678", nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		return req
	}
	if browserNavigation(mk(map[string]string{"Origin": "https://gt.example", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document", "Sec-Fetch-Site": "cross-site"})) != true {
		t.Error("a navigation carrying Origin was refused")
	}
	if browserNavigation(mk(map[string]string{"Origin": "https://gt.example"})) {
		t.Error("Origin without a navigation was accepted")
	}
}

func TestClaimErrorNamesTheWaitWhenTheAccountIsLocked(t *testing.T) {
	err := claimError(&client.StatusError{Status: 429, Body: `{"error":"names_locked","retry_after":1500}`})
	if err == nil || !strings.Contains(err.Error(), "about 25 minutes") || strings.Contains(err.Error(), "invitation page") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(takenMessage(&machineTakenError{atClaim: true}, "https://s", "ABCD-1234", "box", true, false), "used up after") {
		t.Error("taken message still announces a burned code")
	}
}
