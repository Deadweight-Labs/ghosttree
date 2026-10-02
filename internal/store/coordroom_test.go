package store

import (
	"errors"
	"testing"
)

func registerHandoffTarget(t *testing.T, s *Store, externalID string) {
	t.Helper()
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: externalID, Provider: "test", RoomKey: RoomKeyForProject("handoff-test"), DisplayName: externalID, Person: externalID, PrincipalID: "person:" + externalID}); err != nil {
		t.Fatal(err)
	}
}

// A→B und B→A sind dasselbe Gespräch. Ohne sortierte Teilnehmer führen zwei
// Agenten zwei getrennte Hälften und wundern sich, warum die Antwort fehlt.
func TestDirectRoomKeyIsTheSameInBothDirections(t *testing.T) {
	if RoomKeyForDirect([]string{"sess-a", "sess-b"}) != RoomKeyForDirect([]string{"sess-b", "sess-a"}) {
		t.Fatal("a DM must be one room, not two halves")
	}
	if RoomKeyForDirect([]string{"sess-a", "sess-b"}) == RoomKeyForDirect([]string{"sess-a", "sess-c"}) {
		t.Fatal("different participants must be different rooms")
	}
	// Dieselbe Runde ist dieselbe Gruppe: wer dreimal dieselbe Abstimmung
	// eröffnet, bekommt dreimal denselben Verlauf statt drei halber.
	if RoomKeyForGroup([]string{"a", "b", "c"}) != RoomKeyForGroup([]string{"c", "a", "b", "a"}) {
		t.Fatal("the same group must resolve to the same room")
	}
}

// AC-9 von REQ-350: ein Unbeteiligter kommt an einen DM nicht heran. Das ist
// die Grenze, die Spec §9 verlangt — Raumzugehörigkeit ist keine
// Zugriffsberechtigung, und ein Dritter bekommt nichts.
func TestAnOutsiderCannotReadADirectRoom(t *testing.T) {
	s := openTest(t)
	key := RoomKeyForDirect([]string{"sess-a", "sess-b"})
	if err := s.EnsureCoordRoom(CoordRoom{Key: key, Kind: RoomDirect,
		Members: []string{"sess-a", "sess-b"}}); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	for _, member := range []string{"sess-a", "sess-b"} {
		ok, err := s.MayReadCoordRoom(key, member)
		if err != nil {
			t.Fatalf("may read %s: %v", member, err)
		}
		if !ok {
			t.Fatalf("%s is a member and must be able to read", member)
		}
	}

	ok, err := s.MayReadCoordRoom(key, "sess-fremd")
	if err != nil {
		t.Fatalf("may read outsider: %v", err)
	}
	if ok {
		t.Fatal("an outsider must not be able to read a direct room")
	}
}

// Ein erfundener Raumschlüssel ist nicht lesbar. Das ist die sichere
// Richtung: sonst genügte ein geratener Schlüssel, um an der Prüfung
// vorbeizukommen.
func TestAnUnknownRoomIsNotReadable(t *testing.T) {
	s := openTest(t)
	ok, err := s.MayReadCoordRoom("direct:erfunden", "sess-a")
	if err != nil {
		t.Fatalf("may read: %v", err)
	}
	if ok {
		t.Fatal("an unknown direct room must not be readable")
	}
}

// Projekt- und Maschinenräume entscheiden am Perimeter, nicht an einer
// Mitgliederliste: wer im Projekt arbeitet, liest den Projektraum.
func TestProjectAndMachineRoomsDoNotNeedMembership(t *testing.T) {
	s := openTest(t)
	for _, key := range []string{
		RoomKeyForProject("github.com/deadweight-labs/ghosttree"),
		RoomKeyForMachine("mainex"),
	} {
		ok, err := s.MayReadCoordRoom(key, "irgendwer")
		if err != nil {
			t.Fatalf("may read %s: %v", key, err)
		}
		if !ok {
			t.Fatalf("%s must be readable without an explicit membership row", key)
		}
	}
}

// Ein Zweiergespräch mit einem Teilnehmer ist ein Notizzettel. Der Fehler
// fällt sonst erst auf, wenn niemand antwortet.
func TestADirectRoomNeedsTwoMembers(t *testing.T) {
	s := openTest(t)
	if err := s.EnsureCoordRoom(CoordRoom{Key: "direct:x", Kind: RoomDirect,
		Members: []string{"sess-a"}}); err == nil {
		t.Fatal("a direct room with one member must be rejected")
	}
}

func TestADirectRoomRejectsMoreThanTwoMembers(t *testing.T) {
	s := openTest(t)
	members := []string{"a", "b", "c"}
	if err := s.EnsureCoordRoom(CoordRoom{Key: RoomKeyForDirect(members), Kind: RoomDirect, Members: members}); err == nil {
		t.Fatal("three-member direct room accepted")
	}
}

// Ein Teilnehmer findet seine Gespräche wieder, ohne sich Schlüssel zu
// merken — Projekt- und Maschinenräume stehen bewusst nicht in der Liste,
// die ergeben sich aus der Umgebung.
func TestAMemberFindsTheirOwnRooms(t *testing.T) {
	s := openTest(t)
	mine := RoomKeyForDirect([]string{"sess-a", "sess-b"})
	theirs := RoomKeyForDirect([]string{"sess-c", "sess-d"})
	for key, members := range map[string][]string{
		mine:   {"sess-a", "sess-b"},
		theirs: {"sess-c", "sess-d"},
	} {
		if err := s.EnsureCoordRoom(CoordRoom{Key: key, Kind: RoomDirect, Members: members}); err != nil {
			t.Fatalf("ensure %s: %v", key, err)
		}
	}
	rooms, err := s.CoordRoomsFor("sess-a")
	if err != nil {
		t.Fatalf("rooms: %v", err)
	}
	if len(rooms) != 1 || rooms[0].Key != mine {
		t.Fatalf("a member must see only their own rooms: %+v", rooms)
	}
	if len(rooms[0].Members) != 2 {
		t.Fatalf("the room must carry its members: %+v", rooms[0])
	}
}

// Der Raum ist idempotent: zweimal eröffnen ergibt denselben Raum mit
// demselben Verlauf, nicht zwei.
func TestEnsuringARoomTwiceKeepsOneRoom(t *testing.T) {
	s := openTest(t)
	key := RoomKeyForGroup([]string{"a", "b", "c"})
	for i := 0; i < 2; i++ {
		if err := s.EnsureCoordRoom(CoordRoom{Key: key, Kind: RoomGroup,
			Label: "API-Vertrag", Members: []string{"a", "b", "c"}}); err != nil {
			t.Fatalf("ensure %d: %v", i, err)
		}
	}
	members, err := s.CoordRoomMembers(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 3 {
		t.Fatalf("want 3 members after two ensures, got %v", members)
	}
}

// AC-11 von REQ-350: eine neue Session erbt NICHTS automatisch, und ein
// ausdrücklicher Handoff überträgt genau das Benannte. Der Punkt stammt aus
// v1 §5 und fehlt in v2 — siehe Wissenseintrag #2077.
func TestANewSessionInheritsNothingWithoutAnExplicitHandoff(t *testing.T) {
	s := openTest(t)
	key := RoomKeyForDirect([]string{"sess-alt", "sess-partner"})
	if err := s.EnsureCoordRoom(CoordRoom{Key: key, Kind: RoomDirect,
		Members: []string{"sess-alt", "sess-partner"}}); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	// Die Nachfolgesession sieht zunächst nichts.
	ok, err := s.MayReadCoordRoom(key, "sess-neu")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a new session must not inherit an old session's inbox")
	}
	registerHandoffTarget(t, s, "sess-neu")

	moved, err := s.HandoffCoordRooms("sess-alt", "sess-neu", []string{key})
	if err != nil {
		t.Fatalf("handoff: %v", err)
	}
	if moved != 1 {
		t.Fatalf("want 1 room handed off, got %d", moved)
	}
	ok, err = s.MayReadCoordRoom(key, "sess-neu")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("after an explicit handoff the successor must be able to read")
	}

	// Die alte Session bleibt Teilnehmerin ihres eigenen Verlaufs.
	ok, err = s.MayReadCoordRoom(key, "sess-alt")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("a handoff adds a member, it does not evict the one who wrote the history")
	}
}

// Ein Handoff ist kein Weg, sich Zugang zu verschaffen: was der Übergebende
// selbst nicht lesen darf, kann er nicht weiterreichen.
func TestAHandoffCannotGrantRoomsTheSenderCannotRead(t *testing.T) {
	s := openTest(t)
	fremd := RoomKeyForDirect([]string{"sess-x", "sess-y"})
	if err := s.EnsureCoordRoom(CoordRoom{Key: fremd, Kind: RoomDirect,
		Members: []string{"sess-x", "sess-y"}}); err != nil {
		t.Fatal(err)
	}
	registerHandoffTarget(t, s, "sess-komplize")
	moved, err := s.HandoffCoordRooms("sess-aussen", "sess-komplize", []string{fremd})
	if err != nil {
		t.Fatalf("handoff: %v", err)
	}
	if moved != 0 {
		t.Fatalf("a non-member must not be able to hand a room on, moved %d", moved)
	}
	ok, err := s.MayReadCoordRoom(fremd, "sess-komplize")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("the room leaked through a handoff by a non-member")
	}
}

func TestRepeatedHandoffCountsOnlyNewMembership(t *testing.T) {
	s := openTest(t)
	key := RoomKeyForDirect([]string{"from", "peer"})
	if err := s.EnsureCoordRoom(CoordRoom{Key: key, Kind: RoomDirect, Members: []string{"from", "peer"}}); err != nil {
		t.Fatal(err)
	}
	registerHandoffTarget(t, s, "to")
	first, err := s.HandoffCoordRooms("from", "to", []string{key})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.HandoffCoordRooms("from", "to", []string{key})
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 || second != 0 {
		t.Fatalf("handoff counts first=%d second=%d", first, second)
	}
}

func TestDirectEnsureRemainsIdempotentAfterHandoff(t *testing.T) {
	s := openTest(t)
	members := []string{"from", "peer"}
	key := RoomKeyForDirect(members)
	if err := s.EnsureCoordRoom(CoordRoom{Key: key, Kind: RoomDirect, Members: members}); err != nil {
		t.Fatal(err)
	}
	registerHandoffTarget(t, s, "to")
	if _, err := s.HandoffCoordRooms("from", "to", []string{key}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureCoordRoom(CoordRoom{Key: key, Kind: RoomDirect, Members: members}); err != nil {
		t.Fatalf("canonical pair could not re-ensure after handoff: %v", err)
	}
}

func TestGroupHandoffIsAuditedAndLegacyManagerlessGroupIsReadOnly(t *testing.T) {
	s := openTest(t)
	group, err := s.CreateCoordGroup(GroupInput{Creator: "from", Members: []string{"from", "peer"}})
	if err != nil {
		t.Fatal(err)
	}
	registerHandoffTarget(t, s, "to")
	moved, err := s.HandoffCoordRooms("from", "to", []string{group.Key})
	if err != nil || moved != 1 {
		t.Fatalf("managed handoff moved=%d err=%v", moved, err)
	}
	events, err := s.CoordRoomMembershipEvents(group.Key)
	if err != nil {
		t.Fatal(err)
	}
	var audited bool
	for _, event := range events {
		if event.Action == "join" && event.PrincipalID == "to" && event.ActorID == "from" {
			audited = true
		}
	}
	if !audited {
		t.Fatalf("handoff join not audited: %+v", events)
	}
	legacy := RoomKeyForGroup([]string{"a", "b"})
	if err := s.EnsureCoordRoom(CoordRoom{Key: legacy, Kind: RoomGroup, Members: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	registerHandoffTarget(t, s, "c")
	if _, err := s.HandoffCoordRooms("a", "c", []string{legacy}); !errors.Is(err, ErrLegacyGroupReadOnly) {
		t.Fatalf("legacy handoff err=%v", err)
	}
}

func TestOrdinaryGroupMemberCannotHandoffAccess(t *testing.T) {
	s := openTest(t)
	group, err := s.CreateCoordGroup(GroupInput{Creator: "owner", Members: []string{"owner", "member"}})
	if err != nil {
		t.Fatal(err)
	}
	registerHandoffTarget(t, s, "outsider")
	if _, err := s.HandoffCoordRooms("member", "outsider", []string{group.Key}); err == nil {
		t.Fatal("ordinary group member handed off access")
	}
}

func TestHandoffRejectsUnregisteredTarget(t *testing.T) {
	s := openTest(t)
	key := RoomKeyForDirect([]string{"from", "peer"})
	if err := s.EnsureCoordRoom(CoordRoom{Key: key, Kind: RoomDirect, Members: []string{"from", "peer"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HandoffCoordRooms("from", "claimable-later", []string{key}); !errors.Is(err, ErrCoordHandoffTargetUnbound) {
		t.Fatalf("handoff err=%v, want unbound target", err)
	}
	ok, err := s.MayReadCoordRoom(key, "claimable-later")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("unregistered target received private history")
	}
}

// Eine Session an sich selbst zu übergeben ist immer ein Fehler im Aufrufer
// und wird abgewiesen, statt als erfolgreicher Handoff durchzugehen.
func TestAHandoffToItselfIsRejected(t *testing.T) {
	s := openTest(t)
	if _, err := s.HandoffCoordRooms("sess-a", "sess-a", nil); err == nil {
		t.Fatal("a handoff to itself must be rejected")
	}
}
