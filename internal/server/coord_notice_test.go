package server

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// AC-12 von REQ-350: automatisch eingebrachter Inhalt ist nach Geltungsart
// unterscheidbar. Eine Agentennachricht darf nicht wie eine Projektregel
// aussehen — sonst handelt ein Modell nach einer Vermutung, die niemand
// freigegeben hat.
func TestInjectedContentCarriesItsStanding(t *testing.T) {
	got := renderCoordNotices([]store.CoordMessage{
		{SenderExternalID: "sess-codex", AuthorKind: store.AuthorAgent,
			Body: "ich glaube der Parser ist schuld"},
		{SenderExternalID: "robin", AuthorKind: store.AuthorHuman,
			Body: "für diesen Release nur additive Änderungen"},
	}, []store.Thread{
		{ID: 42, Title: "Gilt Pitfall #846 noch?"},
	})

	if !strings.Contains(got, "[unconfirmed] sess-codex") {
		t.Errorf("an agent message must be marked unconfirmed: %s", got)
	}
	if !strings.Contains(got, "[rule] robin") {
		t.Errorf("an authenticated human instruction holds: %s", got)
	}
	if !strings.Contains(got, "[open] THR-42") {
		t.Errorf("an open thread must not read as settled: %s", got)
	}
	// Die Bedeutung der Marken muss im selben Block stehen. Eine Marke, die
	// das Modell nicht auflösen kann, ist Dekoration.
	for _, want := range []string{"not an authorisation", "not settled", "holds — follow it"} {
		if !strings.Contains(got, want) {
			t.Errorf("the legend is missing %q: %s", want, got)
		}
	}
}

// Der Block sagt DASS es etwas gibt, nicht den ganzen Inhalt. Zwanzig Zeilen
// Chat im automatischen Kontext verdrängen sonst die eine Rückfrage, auf die
// es ankommt.
func TestInjectedNoticesStayOneLineEach(t *testing.T) {
	long := strings.Repeat("sehr ausführlich ", 60)
	got := renderCoordNotices([]store.CoordMessage{
		{SenderExternalID: "sess-a", Body: long},
	}, nil)
	// Die Legende steht einmal oben und darf lang sein; die BEITRAGSZEILEN
	// nicht. Geprüft wird deshalb nur, was mit "- [" beginnt.
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		if !strings.HasPrefix(line, "- [") {
			continue
		}
		if len([]rune(line)) > 200 {
			t.Fatalf("a notice line grew to %d runes: %q", len([]rune(line)), line)
		}
	}
	if !strings.Contains(got, "…") {
		t.Errorf("a truncated body must show that it was cut: %s", got)
	}
}

// Eine abgelaufene Meldung wird auch hier als Geschichte gekennzeichnet.
func TestExpiredNoticesAreMarkedInInjectedContext(t *testing.T) {
	got := renderCoordNotices([]store.CoordMessage{
		{SenderExternalID: "sess-a", Body: "Port 7447 ist gleich weg", Expired: true},
	}, nil)
	if !strings.Contains(got, "expired") {
		t.Errorf("an expired notice must be marked in injected context too: %s", got)
	}
}

// Nichts zu melden heißt nichts schreiben. Ein leerer Abschnitt kostet
// Zeichen aus einem Budget, das dem Gedächtnis gehört.
func TestNothingToSayWritesNothing(t *testing.T) {
	if got := renderCoordNotices(nil, nil); got != "" {
		t.Fatalf("an empty coordination block must produce no output, got %q", got)
	}
}

// Die Voreinstellung ist die SCHWÄCHSTE Geltung. Wer eine unbekannte Art
// mitbringt, bekommt "unbestätigt" — nicht "Regel".
func TestUnknownStandingFallsBackToUnconfirmed(t *testing.T) {
	if standingTag("erfunden") != standingTags[UnconfirmedPeer] {
		t.Fatalf("an unknown standing must fall back to unconfirmed, got %q", standingTag("erfunden"))
	}
}
