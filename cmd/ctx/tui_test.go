package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/tui"
)

// fakeTerminal makes ctx believe it talks to a person and records the TUI
// runs instead of starting one.
func fakeTerminal(t *testing.T, terminal bool, result tui.Model) *[]tui.Options {
	t.Helper()
	oldTerm, oldRun := interactiveTerminal, runTUI
	t.Cleanup(func() { interactiveTerminal, runTUI = oldTerm, oldRun })
	var runs []tui.Options
	interactiveTerminal = func(io.Writer) bool { return terminal }
	runTUI = func(opt tui.Options) (tui.Model, error) {
		runs = append(runs, opt)
		return result, nil
	}
	return &runs
}

func TestCtxWithoutArgumentsOpensTUIOnlyInATerminal(t *testing.T) {
	runs := fakeTerminal(t, true, tui.Model{})
	var out bytes.Buffer
	if code := run(nil, &out); code != 0 || len(*runs) != 1 {
		t.Fatalf("terminal: exit %d, runs %d, out %q", code, len(*runs), out.String())
	}
	if (*runs)[0].Start != tui.ScreenHome {
		t.Fatalf("start screen %v", (*runs)[0].Start)
	}

	runs = fakeTerminal(t, false, tui.Model{})
	out.Reset()
	if code := run(nil, &out); code != 2 || len(*runs) != 0 || !strings.Contains(out.String(), "usage: ctx <command>") {
		t.Fatalf("no terminal: exit %d, runs %d, out %q", code, len(*runs), out.String())
	}

	runs = fakeTerminal(t, true, tui.Model{})
	out.Reset()
	if code := run([]string{"version"}, &out); code != 0 || len(*runs) != 0 {
		t.Fatalf("with a command: exit %d, runs %d", code, len(*runs))
	}
}

func TestCtxTUIRefusesWithoutTerminalAndUnknownLook(t *testing.T) {
	runs := fakeTerminal(t, false, tui.Model{})
	var out bytes.Buffer
	if code := cmdTUI(nil, &out); code != 2 || len(*runs) != 0 {
		t.Fatalf("exit %d runs %d", code, len(*runs))
	}
	runs = fakeTerminal(t, true, tui.Model{})
	if code := cmdTUI([]string{"--look", "neon"}, &out); code != 2 || len(*runs) != 0 {
		t.Fatalf("unknown look: exit %d runs %d", code, len(*runs))
	}
	if code := cmdTUI([]string{"--look", "lines"}, &out); code != 0 || len(*runs) != 1 || (*runs)[0].Look != tui.LogoLines {
		t.Fatalf("look lines: exit %d runs %+v", code, *runs)
	}
}

func TestLoginInATerminalRunsTheLoginScreen(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	runs := fakeTerminal(t, true, tui.Model{LoggedIn: false})
	var out bytes.Buffer
	if code := cmdLogin([]string{"--server", "https://ghosttree.example.invalid", "--machine", "box"}, &out); code != 1 {
		t.Fatalf("cancelled login: exit %d", code)
	}
	if len(*runs) != 1 {
		t.Fatalf("runs %d", len(*runs))
	}
	opt := (*runs)[0]
	if opt.Start != tui.ScreenLogin || !opt.LoginDirect || opt.LoginServer != "https://ghosttree.example.invalid" || opt.LoginMachine != "box" {
		t.Fatalf("options %+v", opt)
	}

	// Without --server the form comes first.
	runs = fakeTerminal(t, true, tui.Model{LoggedIn: true})
	if code := cmdLogin(nil, &out); code != 0 || (*runs)[0].LoginDirect {
		t.Fatalf("form login: exit %d, %+v", code, *runs)
	}

	// --plain keeps the old text flow, which here stops for want of a server.
	runs = fakeTerminal(t, true, tui.Model{})
	out.Reset()
	if code := cmdLogin([]string{"--plain"}, &out); code != 2 || len(*runs) != 0 || !strings.Contains(out.String(), "no server") {
		t.Fatalf("plain: exit %d runs %d out %q", code, len(*runs), out.String())
	}
}

func TestCLIBackendLoginWritesConfigAndNamesTheAccount(t *testing.T) {
	url, st, out, _ := loginFixture(t, true)
	if err := config.Save(config.Config{ServerURL: url, Machine: "old", Channel: config.ChannelDev}); err != nil {
		t.Fatal(err)
	}
	b := newCLIBackend()
	ctx := context.Background()
	start, err := b.StartLogin(ctx, "", "tui-box")
	if err != nil {
		t.Fatal(err)
	}
	if start.Code == "" || !strings.Contains(start.CompleteURL, start.Code) || start.Expires.Before(time.Now()) {
		t.Fatalf("start %+v", start)
	}
	out.WriteString(start.Code) // the fixture approves the code it finds here
	res, err := b.WaitLogin(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	if res.Account != "alice" || res.Machine != "tui-box" {
		t.Fatalf("result %+v", res)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token == "" || cfg.Machine != "tui-box" || cfg.ServerURL != url || cfg.Channel != config.ChannelDev {
		t.Fatalf("config %+v", cfg)
	}
	if _, ok := st.AuthenticatePrincipal(cfg.Token); !ok {
		t.Fatal("token not accepted")
	}

	// The backend reports the login without ever handing out the token.
	s := b.CheckServer(ctx, url)
	if s.Token != tui.TokenValid || s.Account != "alice" || !s.Reachable {
		t.Fatalf("server %+v", s)
	}
	// Since when the login exists comes from the server; this device login
	// does not expire.
	if s.Since.IsZero() || time.Since(s.Since) > time.Minute || time.Since(s.Since) < -time.Minute || !s.Expires.IsZero() {
		t.Fatalf("login since %v expires %v", s.Since, s.Expires)
	}
	local := b.Local()
	if len(local.Servers) != 1 || local.Servers[0].Token != tui.TokenStored || local.Channel != "dev" || local.ProfileURL != url+"/ui/profile" {
		t.Fatalf("local %+v", local)
	}

	// Signing out revokes on the server and keeps server, machine and channel.
	var lo bytes.Buffer
	if code := cmdLogout(nil, &lo); code != 0 {
		t.Fatalf("logout exit %d: %s", code, lo.String())
	}
	after, _ := config.Load()
	if after.Token != "" || after.ServerURL != url || after.Machine != "tui-box" || after.Channel != config.ChannelDev {
		t.Fatalf("after logout %+v", after)
	}
	if _, ok := st.AuthenticatePrincipal(cfg.Token); ok {
		t.Fatal("token still valid after logout")
	}
	if code := cmdLogout(nil, &lo); code != 1 || !strings.Contains(lo.String(), "not signed in") {
		t.Fatalf("second logout: %d %s", code, lo.String())
	}
}

func TestCLIBackendSetsTheChannel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if err := config.Save(config.Config{ServerURL: "https://ghosttree.example.invalid", Token: "t", Machine: "m"}); err != nil {
		t.Fatal(err)
	}
	b := newCLIBackend()
	if err := b.SetChannel("dev"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := config.Load(); cfg.Channel != "dev" || cfg.Token != "t" {
		t.Fatalf("config %+v", cfg)
	}
	if err := b.SetChannel("nightly"); err == nil {
		t.Fatal("unknown channel accepted")
	}
}

func TestParseDoctorReadsChecksAndFixes(t *testing.T) {
	out := strings.Join([]string{
		"opencode: not installed (skipped)",
		"client config     OK  /home/x/.config/ghosttree/config.json (machine m)",
		"server reachable  FAIL  https://h: connection refused",
		"                        fix: check the server URL, network path, and running service",
		"ctx on PATH       UNVERIFIED  not found in a shell",
	}, "\n")
	rep := parseDoctor(out)
	if len(rep.Lines) != 4 {
		t.Fatalf("lines %+v", rep.Lines)
	}
	if rep.Lines[0].Status != "note" || rep.Lines[0].Name != "opencode" {
		t.Fatalf("note %+v", rep.Lines[0])
	}
	if l := rep.Lines[2]; l.Name != "server reachable" || l.Status != "FAIL" || !strings.HasPrefix(l.Fix, "check the server URL") {
		t.Fatalf("fail %+v", l)
	}
	ok, fail, unv := rep.Counts()
	if ok != 1 || fail != 1 || unv != 1 {
		t.Fatalf("counts %d %d %d", ok, fail, unv)
	}
}

func TestCLIBackendOpensOnlyWebAddresses(t *testing.T) {
	old := openLink
	t.Cleanup(func() { openLink = old })
	var started []string
	openLink = func(url string) error { started = append(started, url); return nil }
	for _, bad := range []string{"-a Calculator", "file:///etc/passwd", "javascript:x"} {
		if ok, why := newCLIBackend().Open(bad); ok || why != "not a web address" {
			t.Fatalf("%q: ok %v why %q", bad, ok, why)
		}
	}
	if len(started) != 0 {
		t.Fatalf("started %v", started)
	}
}

// revokeServer answers DELETE /api/tokens/self with status and counts the calls.
func revokeServer(t *testing.T, status int) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/api/tokens/self" {
			calls++
			w.WriteHeader(status)
			if status >= 400 {
				fmt.Fprint(w, `{"error":"no`+"\x1b[2J"+`"}`)
			}
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestLogoutExitCodesFollowTheServerAnswer(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   int
	}{{http.StatusNoContent, 0}, {http.StatusUnauthorized, 0}, {http.StatusForbidden, 3}, {http.StatusInternalServerError, 3}} {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv("HOME", t.TempDir())
		srv, calls := revokeServer(t, tc.status)
		if err := config.Save(config.Config{ServerURL: srv.URL, Token: "tok", Machine: "m"}); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if got := cmdLogout(nil, &out); got != tc.code || *calls != 1 {
			t.Fatalf("status %d: exit %d (want %d), calls %d, out %q", tc.status, got, tc.code, *calls, out.String())
		}
		if strings.ContainsRune(out.String(), '\x1b') {
			t.Fatalf("status %d: escape in output %q", tc.status, out.String())
		}
		if cfg, _ := config.Load(); cfg.Token != "" {
			t.Fatalf("status %d: token kept", tc.status)
		}
	}
}

func TestLoginOnAnotherServerRevokesTheOldLogin(t *testing.T) {
	url, _, out, _ := loginFixture(t, true)
	old, calls := revokeServer(t, http.StatusNoContent)
	if err := config.Save(config.Config{ServerURL: old.URL, Token: "old-token", Machine: "box"}); err != nil {
		t.Fatal(err)
	}
	if code := cmdLogin([]string{"--server", url, "--machine", "box"}, out); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if *calls != 1 || !strings.Contains(out.String(), "signed out of "+old.URL) {
		t.Fatalf("old login not revoked: calls %d, out %s", *calls, out)
	}
}

func TestLoginOnTheSameServerRevokesNothing(t *testing.T) {
	url, _, out, _ := loginFixture(t, true)
	if err := config.Save(config.Config{ServerURL: url + "/", Token: "previous", Machine: "box"}); err != nil {
		t.Fatal(err)
	}
	if code := cmdLogin([]string{"--server", url, "--machine", "box"}, out); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if strings.Contains(out.String(), "signed out of") || strings.Contains(out.String(), "could not be revoked") {
		t.Fatalf("same server treated as a switch: %s", out)
	}
}

func TestOldLoginThatCannotBeRevokedIsAWarning(t *testing.T) {
	old, _ := revokeServer(t, http.StatusForbidden)
	note, warning := retireOldLogin(context.Background(), config.Config{ServerURL: old.URL, Token: "t"}, config.Config{ServerURL: "https://other.example.invalid"})
	if note != "" || !strings.Contains(warning, "could not be revoked") || strings.ContainsRune(warning, '\x1b') {
		t.Fatalf("note %q warning %q", note, warning)
	}
}

func TestPlainLoginPrintsNoServerEscapes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/device":
			fmt.Fprint(w, `{"device_code":"d","user_code":"AB\u001b]52;c;eA==\u0007CD","verification_uri":"https://h/\u001b[2Jdevice","verification_uri_complete":"https://h/\u009b2J","expires_in":60,"interval":1}`)
		case "/api/auth/device/token":
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"access_denied"}`)
		}
	}))
	defer srv.Close()
	old := loginSleep
	t.Cleanup(func() { loginSleep = old })
	loginSleep = func(context.Context, time.Duration) error { return nil }
	var out bytes.Buffer
	if code := cmdLogin([]string{"--plain", "--server", srv.URL, "--machine", "m"}, &out); code != 1 {
		t.Fatalf("exit %d: %q", code, out.String())
	}
	for _, r := range out.String() {
		if r == '\x1b' || r == '\x07' || (r >= 0x80 && r <= 0x9f) {
			t.Fatalf("control %U in output %q", r, out.String())
		}
	}
	if !strings.Contains(out.String(), "AB52CEACD") {
		t.Fatalf("code not shown filtered: %q", out.String())
	}
}
