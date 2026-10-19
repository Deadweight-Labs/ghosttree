package store

import "testing"

func TestCreateStandingRollsBackMessageWhenStandingInsertFails(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("standing-atomic")
	registerAccessAgent(t, s, "person:1", "sess-owner", room)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_standing BEFORE INSERT ON coord_standing BEGIN SELECT RAISE(ABORT,'standing failed'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := s.CoordinationFor(Principal{ID: "person:1", Label: "Robin"}, "").CreateStanding(StandingInput{
		RoomKey: room, ClientID: "form-1", Body: "No breaking changes",
	})
	if err == nil {
		t.Fatal("standing trigger failure was ignored")
	}
	messages, err := s.CoordMessagesSince(DestinationRoom, room, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("message escaped rolled-back standing transaction: %+v", messages)
	}
}

func TestCreateStandingRetryIsIdempotentAcrossMessageAndInstruction(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("standing-idempotent")
	registerAccessAgent(t, s, "person:1", "sess-owner", room)
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "sess-peer", PrincipalID: "person:2", DisplayName: "Peer", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	access := s.CoordinationFor(Principal{ID: "person:1", Label: "Robin"}, "")
	in := StandingInput{RoomKey: room, ClientID: "stable-form", Body: "No breaking changes", Mentions: []string{"sess-peer"}}
	first, err := access.CreateStanding(in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := access.CreateStanding(in)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("retry ids differ: %d != %d", first, second)
	}
	messages, err := s.CoordMessagesSince(DestinationRoom, room, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	standing, err := access.Standing(room)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || len(standing) != 1 {
		t.Fatalf("messages=%d standing=%d", len(messages), len(standing))
	}
	mentions, err := s.CoordMessageMentions(first)
	if err != nil || len(mentions) != 1 || mentions[0] != "sess-peer" {
		t.Fatalf("mentions=%v err=%v", mentions, err)
	}
}

func TestCreateStandingRetryRejectsChangedMentionsOrExpiryWithoutDrift(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("standing-retry-mismatch")
	registerAccessAgent(t, s, "person:1", "sess-owner", room)
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "sess-peer", PrincipalID: "person:2", DisplayName: "Peer", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	access := s.CoordinationFor(Principal{ID: "person:1", Label: "Robin"}, "")
	original := StandingInput{RoomKey: room, ClientID: "stable", Body: "Hold", ExpiresAt: "2026-09-19T10:00:00Z", Mentions: []string{"sess-peer"}}
	id, err := access.CreateStanding(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []StandingInput{
		{RoomKey: room, ClientID: "stable", Body: "Hold", ExpiresAt: original.ExpiresAt},
		{RoomKey: room, ClientID: "stable", Body: "Hold", ExpiresAt: "2026-09-20T10:00:00Z", Mentions: []string{"sess-peer"}},
	} {
		if _, err := access.CreateStanding(changed); err == nil {
			t.Fatalf("changed retry accepted: %+v", changed)
		}
	}
	mentions, err := s.CoordMessageMentions(id)
	if err != nil {
		t.Fatal(err)
	}
	standing, err := access.Standing(room)
	if err != nil {
		t.Fatal(err)
	}
	if len(mentions) != 1 || mentions[0] != "sess-peer" || len(standing) != 1 || len(standing[0].Targets) != 1 || standing[0].Targets[0] != "sess-peer" {
		t.Fatalf("retry drifted: mentions=%v standing=%+v", mentions, standing)
	}
}
