package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const registerProject = "github.com/dw/chan"

func registerPayload(t *testing.T, session string) map[string]any {
	t.Helper()
	repo := coordRepo(t, "https://"+registerProject+".git")
	return map[string]any{"prompt": "continue", "cwd": repo, "session_id": session, "tool_name": "Bash"}
}

func registerEnv(t *testing.T) *channelEnv {
	t.Helper()
	e := newChannelEnvIn(t, true)
	t.Setenv("XDG_CONFIG_HOME", e.cfgHome)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(agentIDEnv, "")
	return e
}

func runHookAs(t *testing.T, event, harness string, payload map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(payload)
	var out bytes.Buffer
	if code := cmdHookWith(bytes.NewReader(raw), []string{event, "--harness", harness}, &out); code != 0 {
		t.Fatalf("hook %s exit %d", event, code)
	}
}

func registeredPeer(t *testing.T, e *channelEnv, id string) (store.CoordAgent, bool) {
	t.Helper()
	peers, err := e.a.CoordPeers(store.RoomKeyForProject(registerProject), "", channelSelf)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range peers {
		if p.ExternalID == id {
			return p, true
		}
	}
	return store.CoordAgent{}, false
}

func TestSessionStartRegistersTheSessionWithoutACoordCall(t *testing.T) {
	e := registerEnv(t)
	for _, c := range []struct{ harness, id string }{{"claude", "sess-hook-claude"}, {"codex", "sess-hook-codex"}} {
		if _, ok := registeredPeer(t, e, c.id); ok {
			t.Fatalf("%s is registered before any hook ran", c.id)
		}
		runHookAs(t, "session-start", c.harness, registerPayload(t, c.id))
		p, ok := registeredPeer(t, e, c.id)
		if !ok {
			t.Fatalf("%s did not appear in the project room after session-start", c.id)
		}
		if p.Provider != c.harness {
			t.Errorf("provider = %q, want %q", p.Provider, c.harness)
		}
		if p.DisplayName != c.id {
			t.Errorf("display name = %q", p.DisplayName)
		}
	}
}

func TestPromptAndToolHooksRegisterTooAndRefreshAtMostOncePerInterval(t *testing.T) {
	e := registerEnv(t)
	const id = "sess-hook-late"
	payload := registerPayload(t, id)
	// A session that missed its start (the hooks were installed later) is
	// picked up by its next prompt.
	runHookAs(t, "user-prompt-submit", "claude", payload)
	if _, ok := registeredPeer(t, e, id); !ok {
		t.Fatal("a prompt must register the session")
	}
	path, _ := registerStateFile(id)
	first := loadCoordState(path).LastRegister
	if first == 0 {
		t.Fatal("the registration time is remembered")
	}
	runHookAs(t, "post-tool-use", "claude", payload)
	if got := loadCoordState(path).LastRegister; got != first {
		t.Fatalf("within the interval the hook must not register again: %d -> %d", first, got)
	}
	st := loadCoordState(path)
	st.LastRegister = time.Now().Add(-2 * coordHookInterval).Unix()
	saveCoordState(path, st)
	runHookAs(t, "post-tool-use", "claude", payload)
	if got := loadCoordState(path).LastRegister; got <= st.LastRegister {
		t.Fatalf("after the interval a tool call refreshes the registration: %d", got)
	}
}

func TestRegistrationHookLeavesSubagentsAndRepoLessSessionsOut(t *testing.T) {
	e := registerEnv(t)
	sub := registerPayload(t, "sess-hook-sub")
	sub["agent_id"] = "agent-1"
	runHookAs(t, "post-tool-use", "claude", sub)
	if _, ok := registeredPeer(t, e, "sess-hook-sub"); ok {
		t.Fatal("a subagent call must not register a card of its own")
	}
	runHookAs(t, "session-start", "claude", map[string]any{"cwd": t.TempDir(), "session_id": "sess-hook-norepo"})
	if _, ok := registeredPeer(t, e, "sess-hook-norepo"); ok {
		t.Fatal("outside a repository there is no project room to join")
	}
	runHookAs(t, "session-start", "opencode", registerPayload(t, "sess-hook-other"))
	if _, ok := registeredPeer(t, e, "sess-hook-other"); ok {
		t.Fatal("only claude and codex are registered by hooks")
	}
	t.Setenv("GHOSTTREE_HOOK_SYNTHETIC", "1")
	runHookAs(t, "session-start", "claude", registerPayload(t, "sess-hook-probe"))
	if _, ok := registeredPeer(t, e, "sess-hook-probe"); ok {
		t.Fatal("the doctor probe must not register an agent")
	}
}

func TestRegistrationHookStaysQuietWithoutAServer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var out bytes.Buffer
	raw, _ := json.Marshal(registerPayload(t, "sess-hook-nosrv"))
	if code := cmdHookWith(bytes.NewReader(raw), []string{"session-start", "--harness", "claude"}, &out); code != 0 || !strings.Contains(out.String(), "SessionStart") {
		t.Fatalf("hook must answer well-formed: %d %s", code, out.String())
	}
}
