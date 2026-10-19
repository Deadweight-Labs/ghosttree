package main

import (
	"bytes"
	"encoding/json"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"testing"
	"time"
)

// The Stop hook must answer with an empty JSON object: Claude Code validates a
// Stop hook's output, and a hookSpecificOutput is not part of it.
func TestTurnEndHookAnswersWithAnEmptyObject(t *testing.T) {
	registerEnv(t)
	var out bytes.Buffer
	raw, _ := json.Marshal(registerPayload(t, "sess-turn-end"))
	if code := cmdHookWith(bytes.NewReader(raw), []string{"turn-end", "--harness", "claude"}, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got) != 0 {
		t.Fatalf("Stop hook output = %q, want {}", out.String())
	}
}

// A subagent's tool call carries the session id of the main session and an
// agent id. It keeps the main session's sign of life fresh (at the same
// throttle as the session's own calls) and never opens a card.
func TestSubagentCallsKeepTheMainSessionAlive(t *testing.T) {
	e := registerEnv(t)
	const id = "sess-hook-subalive"
	runHookAs(t, "session-start", "claude", registerPayload(t, id))
	path, _ := registerStateFile(id)
	st := loadCoordState(path)
	st.LastRegister = time.Now().Add(-2 * coordHookInterval).Unix()
	saveCoordState(path, st)
	before := loadCoordState(path).LastRegister

	sub := registerPayload(t, id)
	sub["agent_id"] = "agent-7"
	runHookAs(t, "post-tool-use", "claude", sub)
	if got := loadCoordState(path).LastRegister; got <= before {
		t.Fatalf("a subagent tool call must refresh the main session's registration: %d -> %d", before, got)
	}
	if _, ok := registeredPeer(t, e, id+"/agent-7"); ok {
		t.Fatal("a subagent never gets a card")
	}
}

// End to end, hook to store: a prompt opens the turn, the Stop hook closes it,
// and only after a Stop was ever seen can silence mean "stuck".
func TestHooksCarryTheTurnStateToTheServer(t *testing.T) {
	e := registerEnv(t)
	const id = "sess-turn-e2e"
	room := store.RoomKeyForProject(registerProject)
	silent := func() store.Presence {
		t.Helper()
		past := time.Now().UTC().Add(-(store.StuckAfter + time.Minute)).Format(time.RFC3339)
		if _, err := e.st.DB().Exec(`UPDATE coord_agents SET last_hook_at=? WHERE external_id=?`, past, id); err != nil {
			t.Fatal(err)
		}
		peers, err := e.st.CoordPeers(room, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range peers {
			if p.ExternalID == id {
				return *p.Presence
			}
		}
		t.Fatal("agent not registered")
		return store.Presence{}
	}
	payload := registerPayload(t, id)
	runHookAs(t, "session-start", "claude", payload)
	runHookAs(t, "user-prompt-submit", "claude", payload)
	if got := silent().WorkState.Value; got == store.WorkStuck {
		t.Fatal("before any Stop hook an open turn proves nothing")
	}
	runHookAs(t, "turn-end", "claude", payload)
	if got := silent().WorkState.Value; got == store.WorkStuck {
		t.Fatal("a finished turn is never stuck")
	}
	runHookAs(t, "user-prompt-submit", "claude", payload)
	if got := silent().WorkState.Value; got != store.WorkStuck {
		t.Fatalf("an open turn, silent for ten minutes, is stuck; got %s", got)
	}
}
