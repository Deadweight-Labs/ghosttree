package store

import "testing"

// Dieselbe ClientID zweimal ist ein Wiederholungsversuch, keine zweite
// Nachricht. Ohne das erzeugt jeder Netzwerk-Timeout ein Duplikat, und der
// Empfänger liest dieselbe Bitte zweimal — bei einem Agenten heißt das
// zweimal handeln.
func TestAppendDeduplicatesByClientID(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("github.com/deadweight-labs/ghosttree")
	m := CoordMessage{RoomKey: room, SenderExternalID: "sess-a", SenderKind: "agent",
		ClientID: "c-1", Body: "ich nehme das Backend"}

	first, err := s.AppendCoordMessage(m)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := s.AppendCoordMessage(m)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if first != second {
		t.Fatalf("retry created a second message: %d then %d", first, second)
	}

	got, err := s.CoordMessagesSince(room, 0, 50)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 stored message, got %d", len(got))
	}
}

// Ein Wiederholungsversuch überschreibt nicht. Eine gespeicherte Nachricht
// ändert sich nicht, weil jemand sie erneut sendet — sonst könnte ein zweiter
// Aufruf mit gleicher ClientID den Inhalt unter dem Empfänger austauschen.
func TestRetryDoesNotRewriteTheStoredBody(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("p")
	if _, err := s.AppendCoordMessage(CoordMessage{RoomKey: room,
		SenderExternalID: "sess-a", SenderKind: "agent", ClientID: "c-1",
		Body: "original"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := s.AppendCoordMessage(CoordMessage{RoomKey: room,
		SenderExternalID: "sess-a", SenderKind: "agent", ClientID: "c-1",
		Body: "untergeschoben"}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	got, err := s.CoordMessagesSince(room, 0, 50)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(got) != 1 || got[0].Body != "original" {
		t.Fatalf("a retry rewrote the stored message: %+v", got)
	}
}

// Ein Cursor liefert nur, was der Leser noch nicht hat. Das ist der
// Wiederaufsetzpunkt nach einem Neustart.
func TestMessagesSinceIsACursor(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("github.com/deadweight-labs/ghosttree")
	var ids []int64
	for _, body := range []string{"eins", "zwei", "drei"} {
		id, err := s.AppendCoordMessage(CoordMessage{RoomKey: room,
			SenderExternalID: "sess-a", SenderKind: "agent", ClientID: body, Body: body})
		if err != nil {
			t.Fatalf("append %s: %v", body, err)
		}
		ids = append(ids, id)
	}

	got, err := s.CoordMessagesSince(room, ids[0], 50)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(got) != 2 || got[0].Body != "zwei" || got[1].Body != "drei" {
		t.Fatalf("cursor returned the wrong window: %+v", got)
	}
}

// Eine Nachricht kann auf ein bestehendes Ghosttree-Objekt zeigen. Das ist
// der Unterschied zwischen Chat und diesem System: "ich habe die
// Token-Rotation geändert, lies DEC-31" bleibt auffindbar, "das Auth-Ding ist
// fertig" nicht.
func TestMessageKeepsItsReferences(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("p")
	id, err := s.AppendCoordMessage(CoordMessage{RoomKey: room,
		SenderExternalID: "sess-a", SenderKind: "agent", ClientID: "c-1",
		Body: "Vertrag geändert",
		Refs: []CoordRef{{Kind: "request", ID: "350"}, {Kind: "knowledge", ID: "2071"}}})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	refs, err := s.CoordMessageRefs(id)
	if err != nil {
		t.Fatalf("refs: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("want 2 refs, got %+v", refs)
	}
}

// Räume trennen. Eine Nachricht im Maschinenraum taucht nicht im Projektraum
// auf, auch wenn dieselbe Session sie geschrieben hat.
func TestMessagesDoNotLeakAcrossRooms(t *testing.T) {
	s := openTest(t)
	project := RoomKeyForProject("p")
	machine := RoomKeyForMachine("mainex")
	if _, err := s.AppendCoordMessage(CoordMessage{RoomKey: machine,
		SenderExternalID: "sess-a", SenderKind: "agent", ClientID: "c-1",
		Body: "Postgres neu gestartet"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := s.CoordMessagesSince(project, 0, 50)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("machine message leaked into the project room: %+v", got)
	}
}
