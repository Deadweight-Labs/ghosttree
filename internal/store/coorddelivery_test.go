package store

import (
	"path/filepath"
	"sync"
	"testing"
)

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

// AC-8 von REQ-350 verlangt ausdrücklich, dass ein SERVERNEUSTART nichts
// verliert. Ein In-Memory-Store kann das nicht zeigen: er stirbt mit dem
// Prozess. Deshalb hier eine echte Datei, die geschlossen und neu geöffnet
// wird — das ist der Fall, um den es geht.
func TestStoredMessagesSurviveAServerRestart(t *testing.T) {
	path := t.TempDir() + "/restart.db"
	room := RoomKeyForProject("p")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	id, err := first.AppendCoordMessage(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: room,
		SenderExternalID: "sess-a", ClientID: "c-1", Body: "überlebt den Neustart"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := first.SetCoordCursor("sess-b", DestinationRoom, room, id); err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	got, err := second.CoordMessagesSince(DestinationRoom, room, 0, 50)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(got) != 1 || got[0].Body != "überlebt den Neustart" {
		t.Fatalf("the message did not survive the restart: %+v", got)
	}

	// Und der Lesestand steht noch, sonst bekommt der Empfänger nach dem
	// Neustart alles erneut.
	cursor, err := second.CoordCursor("sess-b", DestinationRoom, room)
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if cursor != id {
		t.Fatalf("cursor was lost across the restart: want %d, got %d", id, cursor)
	}

	// Ein Wiederholungsversuch nach dem Neustart erzeugt keine zweite
	// Nachricht — die Deduplizierung hängt am Bestand, nicht am Prozess.
	again, err := second.AppendCoordMessage(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: room,
		SenderExternalID: "sess-a", ClientID: "c-1", Body: "überlebt den Neustart"})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if again != id {
		t.Fatalf("a retry after restart created a second message: %d then %d", id, again)
	}
}

func TestClaimCoordDeliveryWinsOnceAndNeverMovesBackwards(t *testing.T) {
	s := openTest(t)
	if err := s.MarkCoordDelivery(1, "sess-b", DeliveryFetched); err != nil {
		t.Fatal(err)
	}
	won, err := s.ClaimCoordDelivery(1, "sess-b")
	if err != nil || !won {
		t.Fatalf("first claim: won=%v err=%v", won, err)
	}
	if won, err := s.ClaimCoordDelivery(1, "sess-b"); err != nil || won {
		t.Fatalf("second claim: won=%v err=%v", won, err)
	}
	if err := s.MarkCoordDelivery(1, "sess-b", DeliveryAcked); err != nil {
		t.Fatal(err)
	}
	if won, err := s.ClaimCoordDelivery(1, "sess-b"); err != nil || won {
		t.Fatalf("claim after ack: won=%v err=%v", won, err)
	}
	if got, _ := s.CoordDeliveryState(1, "sess-b"); got != DeliveryAcked {
		t.Fatalf("claim pulled state back to %q", got)
	}
}

func TestClaimCoordDeliveryIsPerRecipientAndFromNothing(t *testing.T) {
	s := openTest(t)
	for _, r := range []string{"sess-a", "sess-b"} {
		if won, err := s.ClaimCoordDelivery(5, r); err != nil || !won {
			t.Fatalf("%s: won=%v err=%v", r, won, err)
		}
	}
	if got, _ := s.CoordDeliveryState(5, "sess-a"); got != DeliveryInjected {
		t.Fatalf("state = %q", got)
	}
}

func TestCoordInjectedMessagesReportsOnlyInjectedOrLater(t *testing.T) {
	s := openTest(t)
	_ = s.MarkCoordDelivery(1, "sess-b", DeliveryFetched)
	_ = s.MarkCoordDelivery(2, "sess-b", DeliveryInjected)
	_ = s.MarkCoordDelivery(3, "sess-b", DeliveryAcked)
	_ = s.MarkCoordDelivery(4, "sess-other", DeliveryInjected)
	got, err := s.CoordInjectedMessages("sess-b", []int64{1, 2, 3, 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("injected = %v", got)
	}
}

func raceClaim(t *testing.T, s *Store) {
	t.Helper()
	const n = 32
	var wg sync.WaitGroup
	results := make(chan bool, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			won, err := s.ClaimCoordDelivery(42, "sess-race")
			if err != nil {
				t.Error(err)
			}
			results <- won
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for won := range results {
		if won {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners)
	}
}

func TestClaimCoordDeliveryRaceHasExactlyOneWinner(t *testing.T) {
	raceClaim(t, openTest(t))
}

func TestClaimCoordDeliveryRaceHasExactlyOneWinnerThroughRuntimeWriter(t *testing.T) {
	s, err := OpenRuntime(filepath.Join(t.TempDir(), "claim.db"), DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	raceClaim(t, s)
}
