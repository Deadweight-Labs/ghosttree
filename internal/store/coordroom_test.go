package store

import "testing"

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

// Eine Session an sich selbst zu übergeben ist immer ein Fehler im Aufrufer
// und wird abgewiesen, statt als erfolgreicher Handoff durchzugehen.
func TestAHandoffToItselfIsRejected(t *testing.T) {
	s := openTest(t)
	if _, err := s.HandoffCoordRooms("sess-a", "sess-a", nil); err == nil {
		t.Fatal("a handoff to itself must be rejected")
	}
}
