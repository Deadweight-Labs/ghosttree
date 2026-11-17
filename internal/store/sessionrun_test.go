package store

import "testing"

// REQ-685 AC-4: what the hooks say about a session. The transcript of a run that is over keeps arriving for a
// minute or two, so "uploaded a moment ago" says running; SessionRunStates is what says it ended (SessionEnd), that
// it only waits for the next prompt (Stop), or that it runs again (a resumed session).
func TestSessionRunStatesFollowTheHooksNotTheAgeOfTheUpload(t *testing.T) {
	st := controlFixture(t)
	room := RoomKeyForProject(roleProject)
	sid, err := st.UpsertSession(Session{Harness: "claude-code", ExternalID: "sess-run-1", AccountID: 4})
	if err != nil {
		t.Fatal(err)
	}
	hook := func(event string) {
		t.Helper()
		if _, err := st.RegisterCoordAgent(CoordAgent{ExternalID: controlAgent, Provider: "claude", RoomKey: room, DisplayName: controlAgent,
			PrincipalID: "person:4", Role: "member", SessionID: "sess-run-1", HookActivity: true, HookEvent: event}); err != nil {
			t.Fatal(err)
		}
	}
	state := func() (RunState, bool) {
		t.Helper()
		m, err := st.SessionRunStates([]int64{sid, 9999})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := m[9999]; ok {
			t.Fatal("a session nobody registered for has a run state")
		}
		s, ok := m[sid]
		return s, ok
	}
	if got, ok := state(); ok {
		t.Fatalf("before any hook the run state is unknown, got %+v", got)
	}
	hook(HookTurnStart)
	hook(HookTool)
	if got, _ := state(); got.Ended || got.Idle {
		t.Fatalf("a running turn is neither ended nor idle: %+v", got)
	}
	hook(HookTurnEnd)
	if got, _ := state(); got.Ended || !got.Idle {
		t.Fatalf("after the Stop hook the agent is alive and idle: %+v", got)
	}
	hook(HookSessionEnd)
	if got, _ := state(); !got.Ended {
		t.Fatalf("after SessionEnd the session has ended: %+v", got)
	}
	hook(HookTouch)
	if got, _ := state(); !got.Ended {
		t.Fatalf("a subagent's sign of life does not bring it back: %+v", got)
	}
	hook(HookTurnStart)
	if got, _ := state(); got.Ended || got.Idle {
		t.Fatalf("a resumed session runs again: %+v", got)
	}
	// Another account's agent with the same session id is not this session's agent.
	if _, err := st.RegisterCoordAgent(CoordAgent{ExternalID: "claude:other:1", Provider: "claude", RoomKey: room, DisplayName: "other",
		PrincipalID: "person:3", Role: "member", SessionID: "sess-run-1", HookActivity: true, HookEvent: HookSessionEnd}); err != nil {
		t.Fatal(err)
	}
	if got, _ := state(); got.Ended {
		t.Fatalf("the agent of another account ended this session: %+v", got)
	}
}
