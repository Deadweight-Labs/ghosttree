package store

import "testing"

// Der Zustand darf nur vorwärts laufen. Ein spät eintreffendes "abgeholt"
// nach einem "bestätigt" würde sonst eine gelesene Nachricht wieder als
// unterwegs darstellen — und Adapter melden nicht in garantierter
// Reihenfolge.
func TestDeliveryStateOnlyMovesForward(t *testing.T) {
	s := openTest(t)
	if err := s.MarkCoordDelivery(1, "sess-b", DeliveryAcked); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if err := s.MarkCoordDelivery(1, "sess-b", DeliveryFetched); err != nil {
		t.Fatalf("late fetch: %v", err)
	}
	got, err := s.CoordDeliveryState(1, "sess-b")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if got != DeliveryAcked {
		t.Fatalf("state moved backwards: want %q, got %q", DeliveryAcked, got)
	}
}

// Was nie vermerkt wurde, ist unbekannt — nicht zugestellt und nicht
// fehlgeschlagen. Ein Adapter, der einen Schritt nicht belegen kann, darf
// nicht dazu führen, dass das System Zustellung behauptet. Spec §A7.
func TestUnrecordedDeliveryIsUnknownNotDelivered(t *testing.T) {
	s := openTest(t)
	got, err := s.CoordDeliveryState(99, "sess-nobody")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if got != DeliveryUnknown {
		t.Fatalf("want %q for an unrecorded delivery, got %q", DeliveryUnknown, got)
	}
}

// Ein erfundener Zustand wird abgewiesen, statt still als unbekannter Wert
// in der Tabelle zu landen. Sonst steht dort irgendwann "sent" neben
// "stored" und niemand weiß, was davon zugestellt heißt.
func TestUnknownDeliveryStateIsRejected(t *testing.T) {
	s := openTest(t)
	if err := s.MarkCoordDelivery(1, "sess-b", "sent"); err == nil {
		t.Fatal("an invented delivery state must be rejected")
	}
}

// Jeder Empfänger hat seinen eigenen Zustand. Dass A gelesen hat, sagt nichts
// über B — bei einem Raumbeitrag ist das der Normalfall.
func TestDeliveryIsTrackedPerRecipient(t *testing.T) {
	s := openTest(t)
	if err := s.MarkCoordDelivery(7, "sess-a", DeliveryAcked); err != nil {
		t.Fatalf("a: %v", err)
	}
	if err := s.MarkCoordDelivery(7, "sess-b", DeliveryStored); err != nil {
		t.Fatalf("b: %v", err)
	}
	a, err := s.CoordDeliveryState(7, "sess-a")
	if err != nil {
		t.Fatalf("state a: %v", err)
	}
	b, err := s.CoordDeliveryState(7, "sess-b")
	if err != nil {
		t.Fatalf("state b: %v", err)
	}
	if a != DeliveryAcked || b != DeliveryStored {
		t.Fatalf("recipients share a state: a=%q b=%q", a, b)
	}
}

// Der Cursor eines Empfängers überlebt einen Neustart und stellt nichts
// doppelt zu. Das ist AC-8 von REQ-350 und §A7: "Beim Reconnect gelten
// monotone Cursor, Deduplizierung und ein Replay-Protokoll ohne Lücke."
func TestCursorAdvancesMonotonicallyAndSurvivesRestart(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("p")

	if err := s.SetCoordCursor("sess-b", DestinationRoom, room, 12); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Ein verspätetes älteres Ack darf den Cursor nicht zurückziehen; sonst
	// bekommt der Empfänger nach jedem Reconnect dieselben Beiträge erneut.
	if err := s.SetCoordCursor("sess-b", DestinationRoom, room, 5); err != nil {
		t.Fatalf("stale set: %v", err)
	}
	got, err := s.CoordCursor("sess-b", DestinationRoom, room)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != 12 {
		t.Fatalf("cursor moved backwards to %d", got)
	}
}

// Ein unbekannter Cursor ist 0 und damit "von vorn", nicht ein Fehler.
// Eine neue Session hat noch nichts gelesen, und das ist ein gültiger
// Zustand.
func TestUnknownCursorStartsAtTheBeginning(t *testing.T) {
	s := openTest(t)
	got, err := s.CoordCursor("sess-neu", DestinationRoom, RoomKeyForProject("p"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != 0 {
		t.Fatalf("an unknown cursor must start at 0, got %d", got)
	}
}
