package store

import "testing"

// The composer suggests names for the room's people and agents; every
// suggestion must resolve to exactly the one it was made for.
func TestMentionHandlesResolveBackToTheirOwner(t *testing.T) {
	e := authorityFixture(t)
	sender := e.human("person:1")
	handles, err := sender.MentionHandles(e.room)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"person:2", "a-anna", "a-ben", "person:4"} {
		if handles[want] == "" {
			t.Errorf("no handle for %s in %v", want, handles)
		}
	}
	if _, self := handles["person:1"]; self {
		t.Error("the sender is not offered to itself")
	}
	for id, handle := range handles {
		msgID, err := sendText(t, sender, e.room, "@"+handle+" hi", "")
		if err != nil {
			t.Fatalf("@%s: %v", handle, err)
		}
		got := storedMentions(t, e, msgID)
		if len(got) != 1 || got[0] != id {
			t.Errorf("@%s reached %v, want %s", handle, got, id)
		}
	}
}

// The agent reads as "Claude · Anna · mainex" in the UI (tool, owner, machine).
// Writing that name as a handle, with the tool word the UI shows even when the
// harness registers itself as claude-code, must reach exactly that agent and
// never its session ID.
func TestMentionReachesTheReadableAgentName(t *testing.T) {
	e := authorityFixture(t)
	const ext = "claude-code:mainex:0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9"
	if _, err := e.st.RegisterCoordAgent(CoordAgent{ExternalID: ext, Provider: "claude-code", RoomKey: e.room, DisplayName: ext, PrincipalID: "person:2", Role: "lead"}); err != nil {
		t.Fatal(err)
	}
	sender := e.human("person:1")
	for _, body := range []string{"@claude-anna-mainex hi", "@Claude-Anna-mainex hi"} {
		id, err := sendText(t, sender, e.room, body, "")
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		got := storedMentions(t, e, id)
		if len(got) != 1 || got[0] != ext {
			t.Errorf("%q reached %v, want %s", body, got, ext)
		}
	}
}

// An agent's display name comes from its client; it must not take a person's
// handle. "@anna" stays Anna, however an agent names itself.
func TestAnAgentCannotTakeAPersonsHandle(t *testing.T) {
	e := authorityFixture(t)
	if _, err := e.st.RegisterCoordAgent(CoordAgent{ExternalID: "a-imposter", Provider: "claude", RoomKey: e.room, DisplayName: "anna", PrincipalID: "person:3", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	id, err := sendText(t, e.human("person:1"), e.room, "@anna hello", "")
	if err != nil {
		t.Fatalf("@anna must resolve to the person, not be ambiguous: %v", err)
	}
	if got := storedMentions(t, e, id); len(got) != 1 || got[0] != "person:2" {
		t.Fatalf("@anna reached %v, want person:2", got)
	}
}

// An agent reports to the person it works for; only itself is left out.
func TestAnAgentMayMentionItsOwnerButNotItself(t *testing.T) {
	e := authorityFixture(t)
	agent := e.agent("a-anna")
	id, err := sendText(t, agent, e.room, "@anna done with the review", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := storedMentions(t, e, id); len(got) != 1 || got[0] != "person:2" {
		t.Fatalf("agent -> owner reached %v, want person:2", got)
	}
	id, err = sendText(t, agent, e.room, "@a-anna talking to myself", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := storedMentions(t, e, id); len(got) != 0 {
		t.Fatalf("self mention reached %v", got)
	}
}

// A guest does not see the member list, so no suggestion may come from it.
func TestMentionHandlesAreEmptyForAGuest(t *testing.T) {
	e := guestEnv(t)
	handles, err := e.human("person:5").MentionHandles(e.room)
	if err != nil {
		t.Fatal(err)
	}
	if len(handles) != 0 {
		t.Fatalf("guest got %v", handles)
	}
}
