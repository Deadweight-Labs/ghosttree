package main

import (
	"bytes"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func coordCLIEnv(t *testing.T) (*store.Store, config.Config) {
	t.Helper()
	return coordCLIEnvWith(t, false)
}

// coordCLIEnvWith: bound=true nutzt ein an die Maschine "testbox" gebundenes
// Token wie der Geräte-Login; ohne Bindung ein Legacy-Token.
func coordCLIEnvWith(t *testing.T, bound bool) (*store.Store, config.Config) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("alice")
	if bound {
		var err error
		token, _, err = st.CreateToken("alice", store.TokenSpec{Label: "device", Machine: "testbox"})
		if err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	cfg := config.Config{ServerURL: srv.URL, Token: token, Machine: "testbox"}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	return st, cfg
}

func coordRepo(t *testing.T, remote string) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", remote}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func TestCoordSendIntentReachesTheServer(t *testing.T) {
	_, cfg := coordCLIEnv(t)
	repo := coordRepo(t, "https://github.com/x/intent.git")
	c := client.New(cfg)
	room := store.RoomKeyForProject("github.com/x/intent")
	if _, err := c.RegisterCoordAgent(store.CoordAgent{ExternalID: "sess-peer", Provider: "test", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := run([]string{"coord", "send", "may I push?", "--intent", "question", "--mention", "sess-peer", repo}, &out); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	msgs, err := c.CoordInbox(store.DestinationRoom, room, "sess-peer", 0, 10)
	if err != nil || len(msgs) != 1 || msgs[0].Intent != store.IntentQuestion {
		t.Fatalf("intent not stored: %+v %v", msgs, err)
	}
}

func TestCoordSendIntentIsValidatedLocally(t *testing.T) {
	coordCLIEnv(t)
	repo := coordRepo(t, "https://github.com/x/intent2.git")
	for _, args := range [][]string{
		{"coord", "send", "x", "--intent", "standing", "--mention", "a", repo},
		{"coord", "send", "x", "--intent", "bogus", repo},
		{"coord", "send", "x", "--intent", "question", repo}, // keine Erwähnung
		{"coord", "send", "x", "--intent"},
	} {
		var out bytes.Buffer
		if code := run(args, &out); code != 2 {
			t.Errorf("%v: want 2, got %d: %s", args, code, out.String())
		}
	}
}

// Die CLI-Identität gehört einem Projektraum. Aus einem zweiten Repo kam
// bisher nur ein 403 oder 404 ohne Erklärung, weil die Ablehnung der
// Anmeldung verworfen wurde. Der Hinweis muss auch mit einem an die Maschine
// gebundenen Geräte-Token funktionieren.
func TestCoordSendFromSecondRepoExplainsTheBoundIdentity(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "machine-bound"}[bound], func(t *testing.T) {
			coordCLIEnvWith(t, bound)
			first := coordRepo(t, "https://github.com/x/first.git")
			second := coordRepo(t, "https://github.com/x/second.git")
			var out bytes.Buffer
			if code := run([]string{"coord", "send", "hello", first}, &out); code != 0 {
				t.Fatalf("first send: %d %s", code, out.String())
			}
			out.Reset()
			if code := run([]string{"coord", "send", "hello", second}, &out); code != 1 {
				t.Fatalf("second send: want 1, got %d: %s", code, out.String())
			}
			got := out.String()
			for _, want := range []string{"already registered in another room", "--agent-name <name>", "ctx:testbox:<name>"} {
				if !strings.Contains(got, want) {
					t.Errorf("output lacks %q: %s", want, got)
				}
			}
			if strings.Contains(got, `{"error"`) {
				t.Errorf("raw JSON body leaked: %s", got)
			}
			out.Reset()
			if code := run([]string{"coord", "send", "hello", "--agent-name", "second", second}, &out); code != 0 {
				t.Fatalf("send with --agent-name: %d %s", code, out.String())
			}
		})
	}
}

// Codierte Fehler des Servers (*client.APIError) dürfen nicht untergehen.
func TestCoordSendShowsCodedServerErrors(t *testing.T) {
	_, cfg := coordCLIEnvWith(t, true)
	cfg.Machine = "otherbox" // das Token ist an testbox gebunden
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	repo := coordRepo(t, "https://github.com/x/coded.git")
	var out bytes.Buffer
	if code := run([]string{"coord", "send", "hello", repo}, &out); code != 1 {
		t.Fatalf("want 1, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "machine_bound") || !strings.Contains(out.String(), "testbox") {
		t.Fatalf("coded error swallowed: %s", out.String())
	}
}

func TestCoordAPIErrorReadsBothErrorTypes(t *testing.T) {
	st, msg, ok := coordAPIError(&client.APIError{Status: 403, Code: "machine_bound", Message: "bound to x"})
	if !ok || st != 403 || !strings.Contains(msg, "machine_bound") || !strings.Contains(msg, "bound to x") {
		t.Fatalf("APIError: %d %q %v", st, msg, ok)
	}
	st, msg, ok = coordAPIError(&client.StatusError{Status: 400, Body: `{"error":"already registered in another room"}`})
	if !ok || st != 400 || msg != "already registered in another room" {
		t.Fatalf("StatusError: %d %q %v", st, msg, ok)
	}
}

func TestCoordAgentNameRejectsInvalidNames(t *testing.T) {
	coordCLIEnv(t)
	repo := coordRepo(t, "https://github.com/x/res.git")
	var out bytes.Buffer
	if code := run([]string{"coord", "send", "x", "--agent-name", "system:bot", repo}, &out); code != 2 {
		t.Fatalf("want 2, got %d: %s", code, out.String())
	}
}
