package hookbudget

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func collect(t *testing.T, session, channel, text string) string {
	t.Helper()
	var got string
	if err := DeliverChannel(session, channel, text, func(s string) error {
		got = s
		return nil
	}); err != nil {
		t.Fatalf("deliver %s: %v", channel, err)
	}
	return got
}

// AC-10 von REQ-350 und die ausdrückliche Korrektur aus v2 §7: ein
// erschöpftes Gedächtnisbudget darf den Chat nicht für den Rest der Session
// abschalten. Der erste Entwurf ließ beides in dasselbe Lebenszeit-Budget
// zählen — "nach ausreichend langer Arbeit verstummt die Kommunikation".
func TestAnExhaustedMemoryBudgetDoesNotSilenceCoordination(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	session := "sess-a"

	// Gedächtnisbudget bis zum Anschlag füllen.
	collect(t, session, ChannelMemory, strings.Repeat("x", Limit+500))
	if again := collect(t, session, ChannelMemory, "noch etwas"); again != "" {
		t.Fatalf("the memory channel should be exhausted, got %q", again)
	}

	// Koordination kommt trotzdem an.
	got := collect(t, session, ChannelCoord, "Codex ändert gerade den API-Vertrag")
	if !strings.Contains(got, "API-Vertrag") {
		t.Fatalf("coordination was silenced by the memory budget: %q", got)
	}
}

// Und die Gegenrichtung: lebhafte Koordination darf nicht das Wissen
// verdrängen, das eine Session zum Arbeiten braucht.
func TestExhaustedCoordinationDoesNotEatTheMemoryBudget(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	session := "sess-b"

	collect(t, session, ChannelCoord, strings.Repeat("y", CoordLimit+500))
	if again := collect(t, session, ChannelCoord, "noch eine Nachricht"); again != "" {
		t.Fatalf("the coordination channel should be exhausted, got %q", again)
	}

	got := collect(t, session, ChannelMemory, "ein wichtiger Pitfall")
	if !strings.Contains(got, "Pitfall") {
		t.Fatalf("memory was crowded out by coordination: %q", got)
	}
}

// Voll heißt bei Koordination "gerade jetzt zu viel", nicht "für heute
// vorbei": das Fenster erholt sich. Ein Lebenszeitbudget hieße, dass ab
// Nachmittag nichts mehr ankommt, obwohl gerade die Abstimmungen laufen.
func TestTheCoordinationWindowRecovers(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	session := "sess-c"

	collect(t, session, ChannelCoord, strings.Repeat("z", CoordLimit+500))
	if again := collect(t, session, ChannelCoord, "blockiert"); again != "" {
		t.Fatal("the window should be full right now")
	}

	// Das Fenster zurückdatieren, statt fünf Minuten zu warten.
	agePath := func() {
		t.Helper()
		if err := ageCoordWindow(dir, session, CoordWindow+time.Minute); err != nil {
			t.Fatalf("age: %v", err)
		}
	}
	agePath()

	got := collect(t, session, ChannelCoord, "wieder da")
	if !strings.Contains(got, "wieder da") {
		t.Fatalf("the coordination window must recover, got %q", got)
	}
}

// Erschöpft heißt verzögert, nicht gelöscht — und der Hinweis nennt den Weg
// zum Rest. Spec §7 verlangt genau diese Unterscheidung: nicht "zugestellt",
// nicht "gelöscht", nicht "für den Rest der Session abgeschaltet".
func TestAFullCoordinationWindowSaysNothingWasDropped(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	got := collect(t, "sess-d", ChannelCoord, strings.Repeat("w", CoordLimit+500))
	if !strings.Contains(got, "Nothing was dropped") {
		t.Errorf("a full window must say nothing was lost: %q", got)
	}
	if !strings.Contains(got, "coord_inbox") {
		t.Errorf("a full window must name the way to the rest: %q", got)
	}
}

// Die beiden Konten sind einzeln messbar — sonst ließe sich die Trennung
// nicht belegen, sondern nur behaupten.
func TestEachChannelIsMeasuredSeparately(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	session := "sess-e"
	collect(t, session, ChannelMemory, strings.Repeat("m", 100))
	collect(t, session, ChannelCoord, strings.Repeat("c", 200))

	mem, err := ChannelUsage(session, ChannelMemory)
	if err != nil {
		t.Fatalf("memory usage: %v", err)
	}
	coord, err := ChannelUsage(session, ChannelCoord)
	if err != nil {
		t.Fatalf("coord usage: %v", err)
	}
	if mem.ReservedChars != 100 {
		t.Errorf("memory account is %d, want 100", mem.ReservedChars)
	}
	if coord.ReservedChars != 200 {
		t.Errorf("coordination account is %d, want 200", coord.ReservedChars)
	}
}

// Ein emit, das nichts schrieb, gibt seine Reservierung zurück; ein anderer
// Fehler zählt als Schreibversuch und behält sie.
func TestDeliverChannelReleasesReservationWhenNothingWasEmitted(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	text := strings.Repeat("a", 5000)
	for i := 0; i < 10; i++ {
		err := DeliverChannel("s", ChannelCoord, text, func(string) error { return ErrNotEmitted })
		if !errors.Is(err, ErrNotEmitted) {
			t.Fatalf("err = %v", err)
		}
	}
	r, _ := ChannelUsage("s", ChannelCoord)
	if r.ReservedChars != 0 || r.Exhausted {
		t.Fatalf("reservation leaked: %+v", r)
	}
	var got string
	if err := DeliverChannel("s", ChannelCoord, text, func(s string) error { got = s; return nil }); err != nil || got != text {
		t.Fatalf("err=%v full=%v", err, got == text)
	}
	// Ein Schreibfehler verbraucht Budget.
	_ = DeliverChannel("w", ChannelCoord, "abc", func(string) error { return errors.New("pipe broke") })
	if r, _ := ChannelUsage("w", ChannelCoord); r.ReservedChars != 3 {
		t.Fatalf("a write attempt must spend budget: %+v", r)
	}
	// Erschöpftes Konto bleibt erschöpft, auch wenn eine gekürzte Zustellung zurückgerollt wird.
	big := strings.Repeat("b", 20000)
	_ = DeliverChannel("e", ChannelCoord, big, func(string) error { return ErrNotEmitted })
	if r, _ := ChannelUsage("e", ChannelCoord); r.Exhausted || r.ReservedChars != 0 {
		t.Fatalf("rolled-back truncation must not exhaust: %+v", r)
	}
}
