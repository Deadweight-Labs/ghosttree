package store

import (
	"errors"
	"slices"
	"testing"
)

// webOnlyEnv: robin (person:1) owns the org and has no agent; anna (2) is a
// project member with no agent; ben (3) is a member with the only agent. cleo
// (4) is in the org without a project role, dev (5) is not in the org.
func webOnlyEnv(t *testing.T) authorityEnv {
	t.Helper()
	st := roleFixture(t)
	for _, who := range []string{"person:2", "person:3"} {
		if err := setRole(st, "person:1", who, RoleMember, false); err != nil {
			t.Fatal(err)
		}
	}
	// Only ben has an agent; it is what makes the project room exist.
	room := RoomKeyForProject(roleProject)
	registerRoleAgent(t, st, "a-ben", "person:3", room, "member")
	return authorityEnv{st: st, room: room, project: roleProject}
}

func TestMentionReachesProjectMemberWithoutAgentInTheRoom(t *testing.T) {
	e := webOnlyEnv(t)
	id, err := sendText(t, e.human("person:3"), e.room, "@anna can you look", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := storedMentions(t, e, id); !slices.Equal(got, []string{"person:2"}) {
		t.Fatalf("mentions %v, want [person:2]", got)
	}
	_, presented, err := e.human("person:2").MessagePresentationWindow(DestinationRoom, e.room, MessageWindow{Mode: messageWindowLatest, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range presented {
		if p.Message.ID == id {
			found = true
			if !slices.Contains(p.Mentions, "person:2") || p.MentionLabels["person:2"] != "anna" {
				t.Errorf("anna reads mentions %v labels %v", p.Mentions, p.MentionLabels)
			}
		}
	}
	if !found {
		t.Fatal("anna does not see the message")
	}
	summaries, err := e.human("person:2").RoomSummaries()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range summaries {
		if s.Room.Key == e.room && s.MentionUnread != 1 {
			t.Errorf("anna's mention count %d, want 1", s.MentionUnread)
		}
	}
	// The org owner is an implicit project owner and is reached as well.
	id, err = sendText(t, e.human("person:3"), e.room, "@robin ping", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := storedMentions(t, e, id); !slices.Equal(got, []string{"person:1"}) {
		t.Fatalf("owner mentions %v", got)
	}
}

func TestMentionDoesNotReachAccountsWithoutProjectAccess(t *testing.T) {
	e := webOnlyEnv(t)
	sender := e.human("person:3")
	for _, body := range []string{"@cleo hello", "@dev hello"} {
		id, err := sendText(t, sender, e.room, body, "")
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if got := storedMentions(t, e, id); len(got) != 0 {
			t.Errorf("%q reached %v", body, got)
		}
	}
	// An explicit target without access is as unknown as before.
	_, err := sender.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: e.room, ClientID: "x1", Body: "hi", Mentions: []string{"person:5"}})
	if !errors.Is(err, ErrCoordUnknownRecipient) {
		t.Fatalf("explicit mention of an outsider: %v", err)
	}
	got, err := sender.UnreachedMentions(e.room, "@cleo and @dev and @anna")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"cleo", "dev"}) {
		t.Fatalf("unreached %v, want [cleo dev]", got)
	}
}

func TestUnreachedMentionsStayHiddenFromGuests(t *testing.T) {
	e := guestEnv(t)
	got, err := e.human("person:5").UnreachedMentions(e.room, "@nobody and @anna")
	if err != nil || len(got) != 0 {
		t.Fatalf("a guest must get no answer: %v %v", got, err)
	}
	got, err = e.human("person:3").UnreachedMentions(e.room, "@nobody and @anna")
	if err != nil || !slices.Equal(got, []string{"nobody"}) {
		t.Fatalf("member: %v %v", got, err)
	}
}
