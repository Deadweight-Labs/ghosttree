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
