package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func ageRegistration(t *testing.T, id string) int64 {
	t.Helper()
	path, _ := registerStateFile(id)
	st := loadCoordState(path)
	st.LastRegister = time.Now().Add(-2 * coordHookInterval).Unix()
	saveCoordState(path, st)
	return st.LastRegister
}

func TestCodexHeartbeatComesFromPreToolUse(t *testing.T) {
	e := registerEnv(t)
	const id = "sess-hook-codex-beat"
	payload := registerPayload(t, id)
	runHookAs(t, "pre-tool-use", "codex", payload)
	p, ok := registeredPeer(t, e, id)
	if !ok || p.Provider != "codex" {
		t.Fatalf("a codex tool call registers the session as codex: %+v %v", p, ok)
	}
	path, _ := registerStateFile(id)
	first := loadCoordState(path).LastRegister
	runHookAs(t, "pre-tool-use", "codex", payload)
	if got := loadCoordState(path).LastRegister; got != first {
		t.Fatalf("within the interval no second registration: %d -> %d", first, got)
	}
	aged := ageRegistration(t, id)
	runHookAs(t, "pre-tool-use", "codex", payload)
	if got := loadCoordState(path).LastRegister; got <= aged {
		t.Fatalf("after the interval the heartbeat refreshes: %d", got)
	}
	// Claude has post-tool-use for this; its pre-tool-use stays out of it.
	runHookAs(t, "pre-tool-use", "claude", registerPayload(t, "sess-hook-claude-pre"))
	if _, ok := registeredPeer(t, e, "sess-hook-claude-pre"); ok {
		t.Fatal("claude's pre-tool-use must not register")
	}
}

func TestPostToolUseWithAHangingServerCostsOneDeadline(t *testing.T) {
	e := registerEnv(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	cfg := config.Config{ServerURL: srv.URL, Token: "x", Machine: "chanbox"}
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(e.cfgHome, "ghosttree", "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	runHookAs(t, "post-tool-use", "claude", registerPayload(t, "sess-hook-hang"))
	took := time.Since(start)
	if took < registerHookDeadline-200*time.Millisecond {
		t.Fatalf("the server never answered, yet the hook returned after %v", took)
	}
	if took > registerHookDeadline+700*time.Millisecond {
		t.Fatalf("registration and inbox must wait side by side, took %v", took)
	}
}

func TestRegistrationKeepsTheFirstRoomWhenTheSessionMovesRepos(t *testing.T) {
	e := registerEnv(t)
	const id = "sess-hook-mover"
	runHookAs(t, "session-start", "claude", registerPayload(t, id))
	other := coordRepo(t, "https://github.com/dw/other.git")
	ageRegistration(t, id)
	runHookAs(t, "post-tool-use", "claude", map[string]any{"cwd": other, "session_id": id, "tool_name": "Bash"})
	// The room does not even exist (404) when nobody was announced in it.
	if peers, err := e.a.CoordPeers(store.RoomKeyForProject("github.com/dw/other"), "", channelSelf); err == nil && len(peers) != 0 {
		t.Fatalf("the session must not be announced in the repository it moved into: %+v", peers)
	}
	if _, ok := registeredPeer(t, e, id); !ok {
		t.Fatal("it stays in its first room")
	}
	path, _ := registerStateFile(id)
	if got := loadCoordState(path).RegisterRoom; got != store.RoomKeyForProject(registerProject) {
		t.Fatalf("the pinned room changed: %q", got)
	}
}

func TestRegistrationHookIsSilentWhenTheServerRefusesTheRoom(t *testing.T) {
	e := registerEnv(t)
	const id = "sess-hook-bound"
	// The session is already bound to another project room (for instance by
	// its first coord call); the server refuses a second one.
	if _, err := e.a.RegisterCoordAgent(store.CoordAgent{ExternalID: id, Provider: "claude", RoomKey: store.RoomKeyForProject("github.com/dw/first")}); err != nil {
		t.Fatal(err)
	}
	runHookAs(t, "session-start", "claude", registerPayload(t, id))
	if _, ok := registeredPeer(t, e, id); ok {
		t.Fatal("the refused room must not show the session")
	}
	path, _ := registerStateFile(id)
	if st := loadCoordState(path); st.RegisterRoom != "" || st.LastRegister == 0 {
		t.Fatalf("a refusal is not remembered as a join but still backs off: %+v", st)
	}
}

func TestRepoLessSessionBacksOffWithoutRunningGitAgain(t *testing.T) {
	registerEnv(t)
	const id = "sess-hook-nogit"
	runHookAs(t, "post-tool-use", "claude", map[string]any{"cwd": t.TempDir(), "session_id": id, "tool_name": "Bash"})
	path, _ := registerStateFile(id)
	if loadCoordState(path).LastRegister == 0 {
		t.Fatal("the throttle is written before the early return")
	}
}
