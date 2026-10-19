package store

import (
	"errors"
	"fmt"
	"testing"
)

func seedCoordMessages(t *testing.T, s *Store, room string, count int) {
	t.Helper()
	for i := 1; i <= count; i++ {
		_, err := s.AppendCoordMessage(CoordMessage{
			DestinationKind:  DestinationRoom,
			DestinationID:    room,
			SenderExternalID: "sess-a",
			ClientID:         fmt.Sprintf("%s-message-%d", room, i),
			Body:             "message",
		})
		if err != nil {
			t.Fatalf("append message %d: %v", i, err)
		}
	}
}

func TestLatestWindowReturnsNewestFiftyAscending(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("paging")
	seedCoordMessages(t, s, room, 75)

	page, err := s.CoordMessageWindow(DestinationRoom, room, LatestWindow(50))
	if err != nil {
		t.Fatal(err)
	}
	got := page.Messages
	if len(got) != 50 || got[0].Sequence != 26 || got[49].Sequence != 75 {
		t.Fatalf("window length=%d range=%d..%d", len(got), got[0].Sequence, got[len(got)-1].Sequence)
	}
	if page.HighWater != 75 || !page.HasOlder || page.HasNewer {
		t.Fatalf("page metadata=%+v", page)
	}
}

func TestBeforeAndAfterWindowsUseTargetSequenceWithoutOverlap(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("paging")
	other := RoomKeyForProject("interleaved")
	for i := 1; i <= 8; i++ {
		_, err := s.AppendCoordMessage(CoordMessage{
			DestinationKind: DestinationRoom, DestinationID: room,
			SenderExternalID: "sess-a", ClientID: fmt.Sprintf("room-%d", i), Body: "room",
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.AppendCoordMessage(CoordMessage{
			DestinationKind: DestinationRoom, DestinationID: other,
			SenderExternalID: "sess-b", ClientID: fmt.Sprintf("other-%d", i), Body: "other",
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	before, err := s.CoordMessageWindow(DestinationRoom, room, BeforeWindow(6, 3))
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.CoordMessageWindow(DestinationRoom, room, AfterWindow(5, 3))
	if err != nil {
		t.Fatal(err)
	}
	assertSequences(t, before.Messages, 3, 4, 5)
	assertSequences(t, after.Messages, 6, 7, 8)
	if before.HighWater != 8 || !before.HasOlder || !before.HasNewer {
		t.Fatalf("before metadata=%+v", before)
	}
	if after.HighWater != 8 || !after.HasOlder || after.HasNewer {
		t.Fatalf("after metadata=%+v", after)
	}
}

func TestMessageWindowRejectsInvalidBoundaries(t *testing.T) {
	s := openTest(t)
	for _, window := range []MessageWindow{
		BeforeWindow(0, 50),
		AfterWindow(-1, 50),
		{Mode: "sideways", Limit: 50},
	} {
		if _, err := s.CoordMessageWindow(DestinationRoom, "project:p", window); !errors.Is(err, ErrCoordInvalidSequence) {
			t.Fatalf("window %+v: got %v, want typed validation error", window, err)
		}
	}
}

func TestEmptySequenceWindowsRetainNavigationMetadata(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("empty-window")
	seedCoordMessages(t, s, room, 3)

	after, err := s.CoordMessageWindow(DestinationRoom, room, AfterWindow(3, 50))
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Messages) != 0 || !after.HasOlder || after.HasNewer || after.HighWater != 3 {
		t.Fatalf("empty after page=%+v", after)
	}
	before, err := s.CoordMessageWindow(DestinationRoom, room, BeforeWindow(1, 50))
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Messages) != 0 || before.HasOlder || !before.HasNewer || before.HighWater != 3 {
		t.Fatalf("empty before page=%+v", before)
	}
}

func TestSequenceWindowRejectsFutureAnchorButAllowsRetentionGap(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("window-anchor")
	seedCoordMessages(t, s, room, 5)
	if _, err := s.CoordMessageWindow(DestinationRoom, room, AfterWindow(6, 50)); !errors.Is(err, ErrCoordInvalidSequence) {
		t.Fatalf("future anchor: %v", err)
	}
	if _, err := s.DB().Exec(`DELETE FROM coord_messages WHERE destination_kind=? AND destination_id=? AND sequence=3`, DestinationRoom, room); err != nil {
		t.Fatal(err)
	}
	before, err := s.CoordMessageWindow(DestinationRoom, room, BeforeWindow(3, 50))
	if err != nil {
		t.Fatalf("retained before cursor: %v", err)
	}
	after, err := s.CoordMessageWindow(DestinationRoom, room, AfterWindow(3, 50))
	if err != nil {
		t.Fatalf("retained after cursor: %v", err)
	}
	assertSequences(t, before.Messages, 1, 2)
	assertSequences(t, after.Messages, 4, 5)
}

func TestMessageWindowCannotReadRevokedPrivateRoom(t *testing.T) {
	s := newCoordAccessStore(t)
	group, err := s.CreateCoordGroup(GroupInput{Label: "private", Creator: "sess-b", Members: []string{"sess-a", "sess-b"}})
	if err != nil {
		t.Fatal(err)
	}
	registerAccessAgent(t, s, "person:1", "sess-a", RoomKeyForProject("p"))
	access := s.CoordinationFor(Principal{ID: "person:1"}, "sess-a")
	if err := access.LeaveRoom(group.Key, "sess-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := access.MessageWindow(DestinationRoom, group.Key, LatestWindow(50)); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("revoked room window: %v", err)
	}
}

func TestDestinationSequenceSurvivesCompleteMessageDeletion(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("retained-sequence")
	seedCoordMessages(t, s, room, 3)
	if _, err := s.DB().Exec(`DELETE FROM coord_messages WHERE destination_kind=? AND destination_id=?`, DestinationRoom, room); err != nil {
		t.Fatal(err)
	}
	id, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, SenderExternalID: "sess-a", ClientID: "after-delete", Body: "new"})
	if err != nil {
		t.Fatal(err)
	}
	var sequence int64
	if err := s.DB().QueryRow(`SELECT sequence FROM coord_messages WHERE id=?`, id).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	if sequence != 4 {
		t.Fatalf("sequence=%d, want 4", sequence)
	}
}

func TestCoordAccessAuthorizesMessageWindows(t *testing.T) {
	s := newCoordAccessStore(t)
	allowed := RoomKeyForProject("allowed")
	denied := RoomKeyForProject("denied")
	registerAccessAgent(t, s, "person:1", "sess-a", allowed)
	registerAccessAgent(t, s, "person:2", "sess-b", denied)

	access := s.CoordinationFor(Principal{ID: "person:1"}, "sess-a")
	if _, err := access.MessageWindow(DestinationRoom, allowed, LatestWindow(50)); err != nil {
		t.Fatalf("allowed window: %v", err)
	}
	if _, err := access.MessageWindow(DestinationRoom, denied, LatestWindow(50)); err != ErrCoordForbidden {
		t.Fatalf("denied window: got %v, want %v", err, ErrCoordForbidden)
	}
}

func assertSequences(t *testing.T, got []CoordMessage, want ...int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d messages, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Sequence != want[i] {
			t.Fatalf("message %d sequence=%d, want %d", i, got[i].Sequence, want[i])
		}
	}
}
