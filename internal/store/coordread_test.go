package store

import (
	"errors"
	"fmt"
	"testing"
)

func TestManualUnreadMarkerClearsOnlyWhenReadThroughIt(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("read-state")
	seedCoordMessages(t, s, room, 10)

	if err := s.MarkCoordRead("person:1", DestinationRoom, room, 10); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkCoordUnread("person:1", DestinationRoom, room, 6); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkCoordRead("person:1", DestinationRoom, room, 5); err != nil {
		t.Fatal(err)
	}
	state, err := s.CoordReadState("person:1", DestinationRoom, room)
	if err != nil {
		t.Fatal(err)
	}
	if state.ReadThrough != 10 || state.ManualUnreadFrom != 6 {
		t.Fatalf("state after lower read: %+v", state)
	}
	if err := s.MarkCoordRead("person:1", DestinationRoom, room, 8); err != nil {
		t.Fatal(err)
	}
	state, err = s.CoordReadState("person:1", DestinationRoom, room)
	if err != nil {
		t.Fatal(err)
	}
	if state.ReadThrough != 10 || state.ManualUnreadFrom != 0 {
		t.Fatalf("state after crossing marker: %+v", state)
	}
}

func TestRoomSummariesCountUnreadAndMentionsForActor(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("summary")
	registerAccessAgent(t, s, "person:1", "sess-a", room)
	for i := 1; i <= 4; i++ {
		mentions := []string(nil)
		if i == 3 || i == 4 {
			mentions = []string{"sess-a"}
		}
		_, err := s.AppendCoordMessage(CoordMessage{
			DestinationKind: DestinationRoom, DestinationID: room,
			SenderExternalID: "sess-b", ClientID: fmt.Sprintf("summary-%d", i), Body: "message", Mentions: mentions,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	access := s.CoordinationFor(Principal{ID: "person:1"}, "sess-a")
	if err := access.MarkRead(DestinationRoom, room, 2); err != nil {
		t.Fatal(err)
	}

	got, err := access.RoomSummaries()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("summaries=%+v", got)
	}
	summary := got[0]
	if summary.Room.Key != room || summary.Room.Kind != RoomProject {
		t.Fatalf("room section data lost: %+v", summary.Room)
	}
	if summary.LastSequence != 4 || summary.ReadThrough != 2 || summary.Unread != 2 || summary.MentionUnread != 2 {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestRoomSummariesAreAuthorizedAndOrderedByActivity(t *testing.T) {
	s := newCoordAccessStore(t)
	mentioned := RoomKeyForProject("mentioned")
	active := RoomKeyForMachine("active")
	private := RoomKeyForDirect([]string{"sess-other", "sess-third"})
	registerAccessAgent(t, s, "person:1", "sess-a", mentioned)
	registerAccessAgent(t, s, "person:1", "sess-a", active)
	registerAccessAgent(t, s, "person:2", "sess-other", RoomKeyForProject("other"))
	registerAccessAgent(t, s, "person:3", "sess-third", RoomKeyForProject("third"))
	if err := s.EnsureCoordRoom(CoordRoom{Key: private, Kind: RoomDirect, Members: []string{"sess-other", "sess-third"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: mentioned, SenderExternalID: "sess-x", ClientID: "mention", Body: "mention", Mentions: []string{"sess-a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: active, SenderExternalID: "sess-x", ClientID: "active-1", Body: "active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: active, SenderExternalID: "sess-x", ClientID: "active-2", Body: "newest"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.CoordinationFor(Principal{ID: "person:1"}, "sess-a").RoomSummaries()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Room.Key != active || got[1].Room.Key != mentioned {
		t.Fatalf("ordered/authorized summaries=%+v", got)
	}
}

func TestCoordAccessReadStateDoesNotExposePrivateRoom(t *testing.T) {
	s := newCoordAccessStore(t)
	private := RoomKeyForDirect([]string{"sess-a", "sess-b"})
	if err := s.EnsureCoordRoom(CoordRoom{Key: private, Kind: RoomDirect, Members: []string{"sess-a", "sess-b"}}); err != nil {
		t.Fatal(err)
	}
	registerAccessAgent(t, s, "person:2", "sess-other", RoomKeyForProject("other"))
	access := s.CoordinationFor(Principal{ID: "person:2"}, "sess-other")
	if err := access.MarkRead(DestinationRoom, private, 1); err != ErrCoordNotFound {
		t.Fatalf("mark read leaked private room: %v", err)
	}
	if err := access.MarkUnread(DestinationRoom, private, 1); err != ErrCoordNotFound {
		t.Fatalf("mark unread leaked private room: %v", err)
	}
}

func TestReadStateUsesEffectiveActorAndKeepsSameOwnerSessionsIndependent(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("independent-readers")
	registerAccessAgent(t, s, "person:1", "sess-a", room)
	registerAccessAgent(t, s, "person:1", "sess-b", room)
	seedCoordMessages(t, s, room, 3)

	if err := s.CoordinationFor(Principal{ID: "person:1"}, "sess-a").MarkRead(DestinationRoom, room, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.CoordinationFor(Principal{ID: "person:1"}, "sess-b").MarkRead(DestinationRoom, room, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.CoordinationFor(Principal{ID: "person:1"}, "").MarkRead(DestinationRoom, room, 3); err != nil {
		t.Fatal(err)
	}
	for actor, want := range map[string]int64{"sess-a": 2, "sess-b": 1, "person:1": 3} {
		state, err := s.CoordReadState(actor, DestinationRoom, room)
		if err != nil {
			t.Fatal(err)
		}
		if state.ReadThrough != want {
			t.Fatalf("actor %s read through=%d, want %d", actor, state.ReadThrough, want)
		}
	}
}

func TestReadMutationsValidateExistingTargetSequence(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("validated-read")
	registerAccessAgent(t, s, "person:1", "sess-a", room)
	seedCoordMessages(t, s, room, 2)
	access := s.CoordinationFor(Principal{ID: "person:1"}, "sess-a")
	if err := access.MarkRead(DestinationRoom, room, 3); !errors.Is(err, ErrCoordInvalidSequence) {
		t.Fatalf("future read sequence: %v", err)
	}
	if err := access.MarkUnread(DestinationRoom, room, 3); !errors.Is(err, ErrCoordInvalidSequence) {
		t.Fatalf("missing unread sequence: %v", err)
	}
}

func TestManualUnreadIsUnionedWithNormalUnreadAndMentions(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("unread-union")
	registerAccessAgent(t, s, "person:1", "sess-a", room)
	for i := 1; i <= 10; i++ {
		mentions := []string(nil)
		if i == 6 || i == 8 {
			mentions = []string{"sess-a"}
		}
		if _, err := s.AppendCoordMessage(CoordMessage{
			DestinationKind: DestinationRoom, DestinationID: room,
			SenderExternalID: "other", ClientID: fmt.Sprintf("union-%d", i), Body: "message", Mentions: mentions,
		}); err != nil {
			t.Fatal(err)
		}
	}
	access := s.CoordinationFor(Principal{ID: "person:1"}, "sess-a")
	if err := access.MarkRead(DestinationRoom, room, 5); err != nil {
		t.Fatal(err)
	}
	if err := access.MarkUnread(DestinationRoom, room, 8); err != nil {
		t.Fatal(err)
	}
	got, err := access.RoomSummaries()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Unread != 5 || got[0].MentionUnread != 2 {
		t.Fatalf("summary=%+v", got)
	}
}

func TestRoomSummaryExcludesOwnMessagesFromUnread(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("own-messages")
	registerAccessAgent(t, s, "person:1", "sess-a", room)
	if _, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, SenderExternalID: "sess-a", ClientID: "own", Body: "own", Mentions: []string{"sess-a"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.CoordinationFor(Principal{ID: "person:1"}, "sess-a").RoomSummaries()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Unread != 0 || got[0].MentionUnread != 0 {
		t.Fatalf("own message counted unread: %+v", got)
	}
}

func TestRemovedRoomMembershipDisappearsFromSummaries(t *testing.T) {
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
	got, err := access.RoomSummaries()
	if err != nil {
		t.Fatal(err)
	}
	for _, summary := range got {
		if summary.Room.Key == group.Key {
			t.Fatalf("removed group leaked through summary: %+v", summary)
		}
	}
}

func TestReadStateKeepsRoomAndDiscussionDestinationsIndependent(t *testing.T) {
	s := openTest(t)
	if _, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: "7", SenderExternalID: "other", ClientID: "room-7", Body: "room"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationDiscussion, DestinationID: "7", SenderExternalID: "other", ClientID: "discussion-7", Body: "discussion"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkCoordRead("person:1", DestinationRoom, "7", 1); err != nil {
		t.Fatal(err)
	}
	room, err := s.CoordReadState("person:1", DestinationRoom, "7")
	if err != nil {
		t.Fatal(err)
	}
	discussion, err := s.CoordReadState("person:1", DestinationDiscussion, "7")
	if err != nil {
		t.Fatal(err)
	}
	if room.ReadThrough != 1 || discussion.ReadThrough != 0 {
		t.Fatalf("room=%+v discussion=%+v", room, discussion)
	}
}

func TestRoomSummaryLastMessageAtBelongsToHighestSequence(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("last-message")
	registerAccessAgent(t, s, "person:1", "sess-a", room)
	for _, message := range []CoordMessage{
		{SenderExternalID: "other", ClientID: "older-sequence", Body: "first", CreatedAt: "2026-09-18T12:00:00Z"},
		{SenderExternalID: "other", ClientID: "newer-sequence", Body: "second", CreatedAt: "2026-09-18T10:00:00Z"},
	} {
		message.DestinationKind = DestinationRoom
		message.DestinationID = room
		if _, err := s.AppendCoordMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.CoordinationFor(Principal{ID: "person:1"}, "sess-a").RoomSummaries()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].LastMessageAt != "2026-09-18T10:00:00Z" {
		t.Fatalf("summary=%+v", got)
	}
}

func TestRoomSummariesSortByServerAppendOrderNotClientTimestamp(t *testing.T) {
	s := newCoordAccessStore(t)
	older := RoomKeyForProject("older-append")
	newer := RoomKeyForProject("newer-append")
	registerAccessAgent(t, s, "person:1", "sess-a", older)
	registerAccessAgent(t, s, "person:1", "sess-b", newer)
	if _, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: older, SenderExternalID: "other", ClientID: "older", Body: "older", CreatedAt: "2099-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: newer, SenderExternalID: "other", ClientID: "newer", Body: "newer", CreatedAt: "2000-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.CoordinationFor(Principal{ID: "person:1"}, "").RoomSummaries()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Room.Key != newer || got[1].Room.Key != older {
		t.Fatalf("append-order summaries=%+v", got)
	}
}

func TestReadStateStillSeesPostRetentionMessageAsUnread(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("retention-read")
	registerAccessAgent(t, s, "person:1", "sess-a", room)
	seedCoordMessages(t, s, room, 3)
	access := s.CoordinationFor(Principal{ID: "person:1"}, "sess-a")
	if err := access.MarkRead(DestinationRoom, room, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DELETE FROM coord_messages WHERE destination_kind=? AND destination_id=?`, DestinationRoom, room); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, SenderExternalID: "other", ClientID: "after-retention", Body: "new"}); err != nil {
		t.Fatal(err)
	}
	got, err := access.RoomSummaries()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].LastSequence != 4 || got[0].Unread != 1 {
		t.Fatalf("summary after retention=%+v", got)
	}
}
