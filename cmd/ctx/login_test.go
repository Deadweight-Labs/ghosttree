package main

import (
	"bytes"
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"net/http/httptest"
)

// loginFixture startet einen Server und lässt die Abfragepause als "Mensch im
// Browser" wirken: beim ersten Warten liest sie den Code aus der Ausgabe und
// entscheidet.
func loginFixture(t *testing.T, approve bool) (url string, st *store.Store, out *bytes.Buffer, clock *time.Time) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	var err error
	st, err = store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.AddPerson("alice"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	out = &bytes.Buffer{}
	now := time.Now()
	clock = &now
	st.Device().SetClock(func() time.Time { return *clock })
	calls := 0
	old := loginSleep
	t.Cleanup(func() { loginSleep = old })
	loginSleep = func(ctx context.Context, d time.Duration) error {
		calls++
		*clock = clock.Add(d + time.Second)
		if calls == 2 { // erster Poll war "pending", jetzt bestätigt der Mensch
			m := regexp.MustCompile(`[A-Z0-9]{4}-[A-Z0-9]{4}`).FindString(out.String())
			if m == "" {
				t.Fatalf("no user code in output: %s", out.String())
			}
			if err := st.Device().Decide(m, "person:1", approve); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	return srv.URL, st, out, clock
}

func TestLoginStoresDeviceTokenInClientConfig(t *testing.T) {
	url, st, out, clock := loginFixture(t, true)
	if code := cmdLogin([]string{"--server", url, "--machine", "build-box"}, out); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerURL != url || cfg.Machine != "build-box" || cfg.Token == "" {
		t.Fatalf("config: %+v", cfg)
	}
	info, err := os.Stat(config.Path())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config file mode: %v %v", info, err)
	}
	p, ok := st.AuthenticatePrincipal(cfg.Token)
	if !ok || p.Machine != "build-box" || p.TokenKind != "device" || p.Label != "alice" {
		t.Fatalf("principal: %+v %v", p, ok)
	}
	for _, want := range []string{"/ui/device", "enter the code", "signed in as alice"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// Ein zweiter Login derselben Maschine ersetzt das Token.
	calls := 0
	loginSleep = func(ctx context.Context, d time.Duration) error {
		calls++
		*clock = clock.Add(d + time.Second)
		if calls == 2 {
			m := regexp.MustCompile(`[A-Z0-9]{4}-[A-Z0-9]{4}`).FindAllString(out.String(), -1)
			_ = st.Device().Decide(m[len(m)-1], "person:1", true)
		}
		return nil
	}
	if code := cmdLogin([]string{"--machine", "build-box"}, out); code != 0 {
		t.Fatalf("second login exit %d: %s", code, out)
	}
	cfg2, _ := config.Load()
	if cfg2.Token == cfg.Token || cfg2.ServerURL != url {
		t.Fatalf("second config: %+v", cfg2)
	}
	if _, ok := st.AuthenticatePrincipal(cfg.Token); ok {
		t.Fatal("old token of the machine still works")
	}
}

func TestLoginDeniedLeavesConfigUntouched(t *testing.T) {
	url, _, out, _ := loginFixture(t, false)
	if err := config.Save(config.Config{ServerURL: url, Token: "old", Machine: "m"}); err != nil {
		t.Fatal(err)
	}
	if code := cmdLogin([]string{"--machine", "m"}, out); code != 1 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if cfg, _ := config.Load(); cfg.Token != "old" {
		t.Fatalf("config changed: %+v", cfg)
	}
	if !strings.Contains(out.String(), "denied") {
		t.Fatalf("output: %s", out)
	}
}

func TestLoginNeedsAServer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out bytes.Buffer
	if code := cmdLogin(nil, &out); code != 2 {
		t.Fatalf("exit %d", code)
	}
}
