package store

import (
	"testing"
	"time"
)

func TestHookActivityShowsAsWorkingUntilItIsStale(t *testing.T) {
	st := controlFixture(t)
	room := RoomKeyForProject(roleProject)
	register := func(hook bool, at string) {
		t.Helper()
		if _, err := st.RegisterCoordAgent(CoordAgent{ExternalID: controlAgent, Provider: "claude", RoomKey: room,
			PrincipalID: "person:4", Role: "member", HookActivity: hook, RegisteredAt: at}); err != nil {
			t.Fatal(err)
		}
	}

	if p := presenceOf(t, st, room, controlAgent); p.WorkState.Value != WorkUnknown {
		t.Fatalf("a registration alone proves no work: %+v", p.WorkState)
	}
	register(false, "")
	if p := presenceOf(t, st, room, controlAgent); p.WorkState.Value != WorkUnknown {
		t.Fatalf("a registration without a hook proves no work: %+v", p.WorkState)
	}
	register(true, "")
	if p := presenceOf(t, st, room, controlAgent); p.WorkState.Value != WorkWorking || p.WorkState.Origin != OriginObserved {
		t.Fatalf("hook activity = %+v", p.WorkState)
	}
	// A later registration that is no hook does not wipe the evidence.
	register(false, "")
	if p := presenceOf(t, st, room, controlAgent); p.WorkState.Value != WorkWorking {
		t.Fatalf("evidence lost: %+v", p.WorkState)
	}
	// Old hook activity falls back to unknown, never to idle.
	register(true, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339))
	if p := presenceOf(t, st, room, controlAgent); p.WorkState.Value != WorkUnknown {
		t.Fatalf("stale hook activity = %+v", p.WorkState)
	}
}

func TestAnAgentKeepsItsNameWhenAnotherRegistrationOffersOnlyTheId(t *testing.T) {
	st := controlFixture(t)
	room := RoomKeyForProject(roleProject)
	nameOf := func() string {
		t.Helper()
		peers, err := st.CoordPeers(room, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range peers {
			if p.ExternalID == controlAgent {
				return p.DisplayName
			}
		}
		t.Fatal("agent not listed")
		return ""
	}
	register := func(name string) {
		t.Helper()
		if _, err := st.RegisterCoordAgent(CoordAgent{ExternalID: controlAgent, Provider: "claude", RoomKey: room,
			PrincipalID: "person:4", Role: "member", DisplayName: name}); err != nil {
			t.Fatal(err)
		}
	}
	register("Claude · chan")
	for _, offered := range []string{"", controlAgent} {
		register(offered)
		if got := nameOf(); got != "Claude · chan" {
			t.Errorf("a registration offering %q renamed the agent to %q", offered, got)
		}
	}
	register("reviewer")
	if got := nameOf(); got != "reviewer" {
		t.Errorf("a deliberate name must replace it, got %q", got)
	}
}

func TestReadableAgentName(t *testing.T) {
	for _, c := range []struct{ provider, project, want string }{
		{"claude", "github.com/dw/chan", "Claude · chan"},
		{"codex", "github.com/dw/chan/", "Codex · chan"},
		{"claude", "", "Claude"},
		{"unknown", "github.com/dw/chan", ""},
		{"", "github.com/dw/chan", ""},
	} {
		if got := ReadableAgentName(c.provider, c.project); got != c.want {
			t.Errorf("ReadableAgentName(%q, %q) = %q, want %q", c.provider, c.project, got, c.want)
		}
	}
}
