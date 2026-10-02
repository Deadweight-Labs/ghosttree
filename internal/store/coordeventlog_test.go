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

func eventStore(t *testing.T) (*Store, string) {
	t.Helper()
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("github.com/x/y")
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "sess-a", PrincipalID: "person:1", Provider: "test", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	return s, room
}

func appendEvents(t *testing.T, s *Store, room, prefix string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, SenderExternalID: "sess-a", ClientID: prefix + strconv.Itoa(i), Body: "x"}); err != nil {
			t.Fatal(err)
		}
	}
}

func countEvents(t *testing.T, s *Store) (n int) {
	t.Helper()
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM coord_events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Gelöscht wird nach Alter, nicht nach Zahl: auch viele Ereignisse binnen des
// Fensters lassen einen alten Cursor ohne Resync.
func TestCoordEventLogKeepsEverythingInsideTheWindow(t *testing.T) {
	s, room := eventStore(t)
	appendEvents(t, s, room, "inside-", 700)
	if got := countEvents(t, s); got < 700 {
		t.Fatalf("events inside the window were pruned: %d", got)
	}
	replay, err := s.CoordEventsAfter(Principal{ID: "person:1"}, 1, 10)
	if err != nil || replay.Resync {
		t.Fatalf("a cursor inside the window must not resync: %+v %v", replay, err)
	}
}

func TestCoordEventLogDropsOldEventsAndRequestsResyncAfterTheWindow(t *testing.T) {
	s, room := eventStore(t)
	appendEvents(t, s, room, "old-", 10)
	before := countEvents(t, s)
	if _, err := s.db.Exec(`UPDATE coord_events SET created_at='2000-01-01T00:00:00.000Z' WHERE sequence<=?`, before-3); err != nil {
		t.Fatal(err)
	}
	// Ein neues Ereignis löst das Aufräumen aus.
	appendEvents(t, s, room, "new-", 1)
	left := countEvents(t, s)
	if left != 4 {
		t.Fatalf("after the window %d events remain, want the 3 recent plus the new one", left)
	}
	replay, err := s.CoordEventsAfter(Principal{ID: "person:1"}, 1, 10)
	if err != nil || !replay.Resync || len(replay.Events) != 0 {
		t.Fatalf("cursor behind the window must resync: %+v %v", replay, err)
	}
	oldest, _ := s.OldestCoordEventSequence()
	latest, _ := s.LatestCoordEventSequence()
	if r, err := s.CoordEventsAfter(Principal{ID: "person:1"}, oldest-1, 10); err != nil || r.Resync {
		t.Fatalf("oldest-1 must remain replayable: %+v %v", r, err)
	}
	if r, err := s.CoordEventsAfter(Principal{ID: "person:1"}, oldest-2, 10); err != nil || !r.Resync {
		t.Fatalf("oldest-2 must resync: %+v %v", r, err)
	}
	if r, err := s.CoordEventsAfter(Principal{ID: "person:1"}, latest, 10); err != nil || r.Resync || len(r.Events) != 0 || r.ScannedThrough != latest {
		t.Fatalf("latest cursor: %+v %v", r, err)
	}
}

// Ruhe ist kein Resync: Ein Cursor am Ende des Protokolls bleibt gültig, auch
// wenn alle Einträge abgelaufen sind und das nächste Ereignis ihn weiterführt.
func TestCoordEventCursorSurvivesAnIdleWindow(t *testing.T) {
	s, room := eventStore(t)
	appendEvents(t, s, room, "idle-", 3)
	latest, _ := s.LatestCoordEventSequence()
	if _, err := s.db.Exec(`UPDATE coord_events SET created_at='2000-01-01T00:00:00.000Z'`); err != nil {
		t.Fatal(err)
	}
	if r, err := s.CoordEventsAfter(Principal{ID: "person:1"}, latest, 10); err != nil || r.Resync {
		t.Fatalf("idle cursor at the end: %+v %v", r, err)
	}
	appendEvents(t, s, room, "after-idle-", 1)
	if got, _ := s.LatestCoordEventSequence(); got != latest+1 {
		t.Fatalf("latest=%d want %d", got, latest+1)
	}
	if r, err := s.CoordEventsAfter(Principal{ID: "person:1"}, latest, 10); err != nil || r.Resync || len(r.Events) != 1 {
		t.Fatalf("the next event continues the idle cursor: %+v %v", r, err)
	}
	// Alles abgelaufen, Tabelle leer: der Cursor am Ende bleibt gültig.
	if _, err := s.db.Exec(`DELETE FROM coord_events`); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.LatestCoordEventSequence(); got != latest+1 {
		t.Fatalf("latest after emptying = %d", got)
	}
	if r, err := s.CoordEventsAfter(Principal{ID: "person:1"}, latest+1, 10); err != nil || r.Resync {
		t.Fatalf("cursor on an empty log: %+v %v", r, err)
	}
}

// Notbremse gegen unbegrenztes Wachstum, nur für den Fall, dass das Fenster
// nichts löscht. Sie ist die einzige zählende Grenze.
func TestCoordEventLogEmergencyCap(t *testing.T) {
	s, _ := eventStore(t)
	if _, err := s.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?)
		INSERT INTO coord_events(kind,object_kind,object_id,created_at)
		SELECT 'message','room','x',strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM n`, coordEventEmergencyCap+500); err != nil {
		t.Fatal(err)
	}
	if got := countEvents(t, s); got > coordEventEmergencyCap {
		t.Fatalf("emergency cap exceeded: %d", got)
	}
}

func TestCoordEventAgeTriggerReplacesTheCountTrigger(t *testing.T) {
	s, _ := eventStore(t)
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name='coord_events_bound'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the count trigger is still there: %d %v", n, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name='coord_events_age'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the age trigger is missing: %d %v", n, err)
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

// Eine Datenbank mit dem alten Zähl-Trigger wird beim nächsten Öffnen umgestellt.
func TestOpeningDropsTheOldCountTrigger(t *testing.T) {
	path := t.TempDir() + "/events.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER coord_events_bound AFTER INSERT ON coord_events BEGIN
		DELETE FROM coord_events WHERE sequence<=NEW.sequence-512; END`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='coord_events_bound'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("old trigger survived: %d %v", n, err)
	}
}
