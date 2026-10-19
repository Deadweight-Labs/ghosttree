package store

import "testing"

// §A7: "Nachrichten sind pro Zielsequenz geordnet; Zeitstempel verschiedener
// Rechner sind keine zuverlässige Kausalordnung." Die Sequenz zählt je Ziel
// und beginnt bei 1 — eine globale Zeilennummer taugt dafür nicht, weil §B3
// die Thread-Kontextkarte mit covers_through_sequence gegen genau dieses Ziel
// abgrenzt.
func TestSequenceCountsPerDestination(t *testing.T) {
	s := openTest(t)
	project := RoomKeyForProject("p")
	machine := RoomKeyForMachine("mainex")

	for i, dest := range []string{project, project, machine, project} {
		if _, err := s.AppendCoordMessage(CoordMessage{
			DestinationKind: DestinationRoom, DestinationID: dest,
			SenderExternalID: "sess-a", ClientID: string(rune('a' + i)),
			Body: "x"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	inProject, err := s.CoordMessagesSince(DestinationRoom, project, 0, 50)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	for i, m := range inProject {
		if m.Sequence != int64(i+1) {
			t.Fatalf("project message %d has sequence %d, want %d", i, m.Sequence, i+1)
		}
	}

	inMachine, err := s.CoordMessagesSince(DestinationRoom, machine, 0, 50)
	if err != nil {
		t.Fatalf("machine: %v", err)
	}
	if len(inMachine) != 1 || inMachine[0].Sequence != 1 {
		t.Fatalf("the machine room must start its own sequence at 1: %+v", inMachine)
	}
}

// §A3: native Harness-Zustellung und Ghosttree-Zustellung dürfen dieselbe
// Nachricht nicht zweimal ans Modell liefern. OriginEventID ist der Schlüssel,
// an dem eine bereits gespiegelte Fremdnachricht wiedererkannt wird — ohne
// ihn erzeugt jede Spiegelung des nativen Kanals einen Doppelempfang.
func TestOriginEventIDDeduplicatesMirroredMessages(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("p")
	m := CoordMessage{DestinationKind: DestinationRoom, DestinationID: room,
		SenderExternalID: "sess-a", ClientID: "c-1", Body: "vom nativen Kanal",
		OriginEventID: "claude:evt-9911"}

	first, err := s.AppendCoordMessage(m)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	// Derselbe native Ursprung, anderer Client-Schlüssel: das ist dieselbe
	// Nachricht auf einem zweiten Weg, nicht eine neue.
	m.ClientID = "c-2"
	second, err := s.AppendCoordMessage(m)
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if first != second {
		t.Fatalf("the same native event was stored twice: %d and %d", first, second)
	}
}

// §11: "Ausgelaufenes Neustart-FYI bleibt Geschichte und löst nach einem Tag
// offline keinen neuen Neustart aus." Abgelaufen heißt sichtbar, nicht weg —
// wer die Historie liest, soll den Hinweis finden und erkennen, dass er
// abgelaufen ist.
func TestExpiredMessagesStayReadableButAreMarked(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForMachine("mainex")
	if _, err := s.AppendCoordMessage(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: room,
		SenderExternalID: "sess-a", ClientID: "c-1",
		Body: "API auf :7447 ist 20 Sekunden weg", ExpiresAt: "2020-01-01T00:00:00Z"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := s.CoordMessagesSince(DestinationRoom, room, 0, 50)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("an expired message must remain history, got %d", len(got))
	}
	if !got[0].Expired {
		t.Fatal("an expired message must be marked as expired, not silently served as current")
	}
}

// §3: author_kind wird aus der authentifizierten Verbindung bestimmt, nicht
// aus dem Body. Der Store setzt deshalb keinen menschlichen Autor, wenn
// keiner belegt ist.
func TestAuthorKindDefaultsToAgentNotHuman(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("p")
	if _, err := s.AppendCoordMessage(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: room,
		SenderExternalID: "sess-a", ClientID: "c-1", Body: "x"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := s.CoordMessagesSince(DestinationRoom, room, 0, 50)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if got[0].AuthorKind != AuthorAgent {
		t.Fatalf("author_kind defaulted to %q, want %q", got[0].AuthorKind, AuthorAgent)
	}
}

// §3: dieselbe Primitive adressiert Raum UND Diskussion. Ein Thread-Beitrag
// darf nicht im Raum auftauchen, nur weil beide dieselbe Kennung tragen.
func TestRoomAndDiscussionAreSeparateDestinations(t *testing.T) {
	s := openTest(t)
	if _, err := s.AppendCoordMessage(CoordMessage{
		DestinationKind: DestinationDiscussion, DestinationID: "42",
		SenderExternalID: "sess-a", ClientID: "c-1", Body: "im Thread"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := s.CoordMessagesSince(DestinationRoom, "42", 0, 50)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a discussion post leaked into a room with the same id: %+v", got)
	}
}

// v1 §12: "Ein Link auf einen veränderlichen Head bleibt als solcher
// erkennbar und ist kein Beleg des damaligen Wortlauts." Eine Referenz auf
// Wissenseintrag #2071 zeigt auf einen Head, der sich morgen ändert; eine auf
// Dokument 360 rev 1 nicht. Wer das nicht unterscheidet, zitiert später einen
// Text, den nie jemand geschrieben hat.
func TestReferenceDistinguishesPinnedRevisionFromMutableHead(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("p")
	id, err := s.AppendCoordMessage(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: room,
		SenderExternalID: "sess-a", ClientID: "c-1", Body: "siehe beides",
		Refs: []CoordRef{
			{Kind: "knowledge", ID: "2071"},
			{Kind: "document", ID: "360", Revision: "1"},
		}})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	refs, err := s.CoordMessageRefs(id)
	if err != nil {
		t.Fatalf("refs: %v", err)
	}
	byKind := map[string]CoordRef{}
	for _, r := range refs {
		byKind[r.Kind] = r
	}
	if !byKind["knowledge"].MutableHead {
		t.Error("a reference without a revision points at a mutable head and must say so")
	}
	if byKind["document"].MutableHead {
		t.Error("a reference pinned to revision 1 must not be reported as a mutable head")
	}
}
