package store

import (
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Eine Trefferliste zeigt ein Snippet — richtig so, 24 volle Beschreibungen
// sprengen jedes Werkzeuglimit. Der Dateispiegel ist aber keine Trefferliste,
// sondern gibt sich als das Dokument aus. Gefunden am 2026-08-25 von einem
// Codex-Prüflauf: REQ-88 endete im Spiegel mitten im Wort bei "un", ohne
// Auslassungszeichen — es sah nicht nach gekürzt aus, sondern nach beschädigt.
func TestSearchCanReturnFullDescriptionsForCallersThatShowTheWholeThing(t *testing.T) {
	s := openTest(t)
	long := "Erste Zeile.\n\n" + strings.Repeat("Ausführliche Begründung mit Beleg. ", 40)
	if _, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "feature", Title: "langer Auftrag", Description: long,
		Scope: scope.Axes{Project: testProject}}}); err != nil {
		t.Fatal(err)
	}

	page, err := s.SearchRequests(requestdomain.SearchFilter{Scope: scope.Axes{Project: testProject}})
	if err != nil {
		t.Fatal(err)
	}
	if got := page.Results[0].Request.Description; len(got) >= len(long) {
		t.Fatalf("a list must stay a list: %d chars", len(got))
	}

	full, err := s.SearchRequests(requestdomain.SearchFilter{
		Scope: scope.Axes{Project: testProject}, FullDescription: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := full.Results[0].Request.Description; got != long {
		t.Fatalf("full description was still cut: %d of %d chars", len(got), len(long))
	}
}
