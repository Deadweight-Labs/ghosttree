package store

import (
	"errors"
	"strconv"
	"testing"
)

func TestCoordMessageEventIsCommittedWithItsObject(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("github.com/x/y")
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "sess-a", PrincipalID: "person:1", Provider: "test", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	messageID, err := s.CoordinationFor(Principal{ID: "person:1"}, "").Send(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: room, ClientID: "event-message", Body: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CoordEventsAfter(Principal{ID: "person:1"}, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, event := range replay.Events {
		if event.Kind == CoordEventMessage && event.ObjectKind == DestinationRoom && event.ObjectID == room {
			found = true
		}
	}
	if !found {
		t.Fatalf("message event missing from %+v", replay.Events)
	}
	messages, err := s.CoordMessagesSince(DestinationRoom, room, 0, 10)
	if err != nil || len(messages) != 1 || messages[0].ID != messageID {
		t.Fatalf("event became visible before its object: messages=%+v err=%v", messages, err)
	}
}

func TestCoordEventReplayUsesCurrentRoomAndThreadAccess(t *testing.T) {
	s := newCoordAccessStore(t)
	owner := Principal{ID: "owner"}
	member := Principal{ID: "member"}
	group, err := s.CreateCoordGroup(GroupInput{Label: "private", Creator: owner.ID, Members: []string{owner.ID, member.ID}})
	if err != nil {
		t.Fatal(err)
	}
	access := s.CoordinationFor(owner, "")
	threadID, err := access.CreateTaskThreadInRoom(group.Key, "secret", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := access.ThreadPost(threadID, CoordMessage{ClientID: "private-thread-event", Body: "secret"}); err != nil {
		t.Fatal(err)
	}
	beforeRemoval, err := s.CoordEventsAfter(member, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !eventTargetPresent(beforeRemoval.Events, DestinationDiscussion, strconv.FormatInt(threadID, 10)) {
		t.Fatalf("member did not receive authorized thread invalidation: %+v", beforeRemoval.Events)
	}
	if err := access.UpdateGroup(GroupUpdate{RoomKey: group.Key, Actor: owner.ID, Remove: []string{member.ID}}); err != nil {
		t.Fatal(err)
	}
	afterRemoval, err := s.CoordEventsAfter(member, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if eventTargetPresent(afterRemoval.Events, DestinationRoom, group.Key) || eventTargetPresent(afterRemoval.Events, DestinationDiscussion, strconv.FormatInt(threadID, 10)) {
		t.Fatalf("revoked private target leaked during replay: %+v", afterRemoval.Events)
	}
	if !eventTargetPresent(afterRemoval.Events, "principal", member.ID) {
		t.Fatalf("revoked member missed opaque visibility invalidation: %+v", afterRemoval.Events)
	}
}

func TestCoordEventLogIsBoundedAndRequestsResyncForOldCursor(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("github.com/x/y")
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "sess-a", PrincipalID: "person:1", Provider: "test", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < coordEventRetentionLimit+1; i++ {
		if _, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, SenderExternalID: "sess-a", ClientID: "bounded-" + strconv.Itoa(i), Body: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	replay, err := s.CoordEventsAfter(Principal{ID: "person:1"}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Resync || len(replay.Events) != 0 {
		t.Fatalf("old cursor replay=%+v", replay)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM coord_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count > coordEventRetentionLimit {
		t.Fatalf("event log retained %d rows, limit %d", count, coordEventRetentionLimit)
	}
	oldest, err := s.OldestCoordEventSequence()
	if err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestCoordEventSequence()
	if err != nil {
		t.Fatal(err)
	}
	atBoundary, err := s.CoordEventsAfter(Principal{ID: "person:1"}, oldest-1, 10)
	if err != nil || atBoundary.Resync {
		t.Fatalf("oldest-1 must remain replayable: replay=%+v err=%v", atBoundary, err)
	}
	tooOld, err := s.CoordEventsAfter(Principal{ID: "person:1"}, oldest-2, 10)
	if err != nil || !tooOld.Resync {
		t.Fatalf("oldest-2 must resync: replay=%+v err=%v", tooOld, err)
	}
	atLatest, err := s.CoordEventsAfter(Principal{ID: "person:1"}, latest, 10)
	if err != nil || atLatest.Resync || len(atLatest.Events) != 0 || atLatest.ScannedThrough != latest {
		t.Fatalf("latest cursor replay=%+v err=%v", atLatest, err)
	}
}

func TestCoordEventCursorRejectsTheFuture(t *testing.T) {
	s := newCoordAccessStore(t)
	empty, err := s.CoordEventsAfter(Principal{ID: "person:1"}, 0, 10)
	if err != nil || empty.Resync || empty.Latest != 0 || len(empty.Events) != 0 {
		t.Fatalf("empty replay=%+v err=%v", empty, err)
	}
	_, err = s.CoordEventsAfter(Principal{ID: "person:1"}, 1, 10)
	if !errors.Is(err, ErrCoordEventCursor) {
		t.Fatalf("err=%v", err)
	}
}

func TestCoordEventReplayAdvancesAcrossFullyFilteredBatch(t *testing.T) {
	s := newCoordAccessStore(t)
	visible := RoomKeyForProject("github.com/x/y")
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "mine", PrincipalID: "person:1", Provider: "test", RoomKey: visible}); err != nil {
		t.Fatal(err)
	}
	start, err := s.LatestCoordEventSequence()
	if err != nil {
		t.Fatal(err)
	}
	private, err := s.CreateCoordGroup(GroupInput{Label: "other", Creator: "other:1", Members: []string{"other:1", "other:2"}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 105; i++ {
		if _, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: private.Key, SenderExternalID: "other:1", ClientID: "filtered-" + strconv.Itoa(i), Body: "secret"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: visible, SenderExternalID: "mine", ClientID: "visible-after-filtered", Body: "visible"}); err != nil {
		t.Fatal(err)
	}
	first, err := s.CoordEventsAfter(Principal{ID: "person:1"}, start, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 0 || first.ScannedThrough <= start || first.ScannedThrough >= first.Latest {
		t.Fatalf("filtered batch did not expose safe progress: %+v", first)
	}
	second, err := s.CoordEventsAfter(Principal{ID: "person:1"}, first.ScannedThrough, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !eventTargetPresent(second.Events, DestinationRoom, visible) {
		t.Fatalf("visible event after filtered batch was skipped: %+v", second)
	}
}

func TestRolledBackCoordMutationDoesNotPublishEvent(t *testing.T) {
	s := newCoordAccessStore(t)
	before, err := s.LatestCoordEventSequence()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appendCoordMessageTx(tx, CoordMessage{DestinationKind: DestinationRoom, DestinationID: "project:rollback", SenderExternalID: "sess", ClientID: "rollback", Body: "never visible"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after, err := s.LatestCoordEventSequence()
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("rolled-back mutation published event: before=%d after=%d", before, after)
	}
	var messages int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM coord_messages WHERE client_id='rollback'`).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if messages != 0 {
		t.Fatalf("rolled-back mutation retained %d objects", messages)
	}
}

func TestExactIdempotentRetriesDoNotCreateCoordEvents(t *testing.T) {
	s := newCoordAccessStore(t)
	principal := Principal{ID: "person:1", Label: "Robin"}
	room := RoomKeyForProject("github.com/x/y")
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "sess-a", PrincipalID: principal.ID, Provider: "test", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	access := s.CoordinationFor(principal, "")
	message := CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "retry-message", Body: "once"}
	if _, err := access.Send(message); err != nil {
		t.Fatal(err)
	}
	assertNoNewCoordEvent(t, s, func() error { _, err := access.Send(message); return err })

	if err := access.MarkRead(DestinationRoom, room, 1); err != nil {
		t.Fatal(err)
	}
	assertNoNewCoordEvent(t, s, func() error { return access.MarkRead(DestinationRoom, room, 1) })

	if err := access.MarkDelivery(1, DeliveryStored); err != nil {
		t.Fatal(err)
	}
	assertNoNewCoordEvent(t, s, func() error { return access.MarkDelivery(1, DeliveryStored) })

	standing := StandingInput{RoomKey: room, ClientID: "retry-standing", Body: "keep it stable"}
	if _, err := access.CreateStanding(standing); err != nil {
		t.Fatal(err)
	}
	assertNoNewCoordEvent(t, s, func() error { _, err := access.CreateStanding(standing); return err })

	threadID, err := access.CreateTaskThreadInRoom(room, "task", "", "")
	if err != nil {
		t.Fatal(err)
	}
	assertNoNewCoordEvent(t, s, func() error { return access.SetThreadState(threadID, ThreadOpen) })
}

func assertNoNewCoordEvent(t *testing.T, s *Store, mutate func() error) {
	t.Helper()
	before, err := s.LatestCoordEventSequence()
	if err != nil {
		t.Fatal(err)
	}
	if err := mutate(); err != nil {
		t.Fatal(err)
	}
	after, err := s.LatestCoordEventSequence()
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("idempotent retry appended event: before=%d after=%d", before, after)
	}
}

func eventTargetPresent(events []CoordEvent, kind, id string) bool {
	for _, event := range events {
		if event.ObjectKind == kind && event.ObjectID == id {
			return true
		}
	}
	return false
}
