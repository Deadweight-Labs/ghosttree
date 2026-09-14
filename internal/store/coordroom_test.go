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
