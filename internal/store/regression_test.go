package store

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func pitfall(t *testing.T, s *Store, title string) int64 {
	t.Helper()
	id, err := s.InsertKnowledge(Knowledge{
		Type: "pitfall", Title: title, Body: "b", Scope: scope.Axes{Project: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Ghost Tree ist nicht dafür zuständig, dass ein gefixter Bug nicht wiederkommt
// — dafür gibt es Regressionstests. Was ein Pitfall leisten kann, ist zu sagen,
// welcher Test das übernimmt.
func TestAPitfallCanNameTheTestThatGuardsIt(t *testing.T) {
	s := openTest(t)
	id := pitfall(t, s, "die Suche verlor den Index")
	if err := s.SetRegressionCover(id, "covered", "TestSearchSurvivesRebuild"); err != nil {
		t.Fatal(err)
	}
	got, err := s.KnowledgeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.RegressionState != "covered" || got.RegressionTest != "TestSearchSurvivesRebuild" {
		t.Errorf("cover = %q/%q, want covered/TestSearchSurvivesRebuild",
			got.RegressionState, got.RegressionTest)
	}
}

// Der dritte Zustand ist der eigentliche Entwurfspunkt: ein Feld, das bei zwei
// Dritteln der Einträge leer bleibt und leer bleiben SOLL, muss "hier ist nichts
// zu testen" von "hat noch niemand angesehen" unterscheiden können. Sonst sieht
// eine bewusste Entscheidung aus wie eine offene Aufgabe.
func TestNotApplicableIsDistinguishableFromUnreviewed(t *testing.T) {
	s := openTest(t)
	quiet := pitfall(t, s, "ctx mcp per Shell-Pipe testen schlägt fehl")
	untouched := pitfall(t, s, "noch niemand hat hingesehen")
	if err := s.SetRegressionCover(quiet, "not_applicable", ""); err != nil {
		t.Fatal(err)
	}

	decided, err := s.KnowledgeByID(quiet)
	if err != nil {
		t.Fatal(err)
	}
	open, err := s.KnowledgeByID(untouched)
	if err != nil {
		t.Fatal(err)
	}
	if decided.RegressionState != "not_applicable" {
		t.Errorf("deliberate state = %q, want not_applicable", decided.RegressionState)
	}
	if open.RegressionState != "" {
		t.Errorf("untouched state = %q, want an empty state meaning nobody judged it",
			open.RegressionState)
	}
	if decided.RegressionState == open.RegressionState {
		t.Error("a considered 'nothing to test here' reads the same as 'never looked at'")
	}
}

// Der eigentliche Ertrag: ein Befund, den sonst niemand erhebt. Hier wurde ein
// Fehler behoben, und kein Test bemerkt seine Wiederkehr.
func TestTheGapQueryFindsFixesNothingGuards(t *testing.T) {
	s := openTest(t)
	gap := pitfall(t, s, "hier fehlt ein Test")
	covered := pitfall(t, s, "hier wacht einer")
	notApplicable := pitfall(t, s, "hier gibt es nichts zu testen")
	pitfall(t, s, "hier hat niemand hingesehen")
	if err := s.SetRegressionCover(gap, "uncovered", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRegressionCover(covered, "covered", "TestSomething"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRegressionCover(notApplicable, "not_applicable", ""); err != nil {
		t.Fatal(err)
	}

	gaps, unreviewed, err := s.RegressionGaps(scope.Axes{Project: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 1 || gaps[0].ID != gap {
		t.Fatalf("gaps = %+v, want only the one nothing guards", gaps)
	}
	// Ohne diese Zahl sähe eine kurze Lückenliste nach Entwarnung aus, während
	// der Bestand in Wahrheit grösstenteils unbeurteilt ist.
	if unreviewed != 1 {
		t.Errorf("unreviewed = %d, want 1 so a short gap list cannot pass for all-clear", unreviewed)
	}
}

// Ein Zustand, den niemand definiert hat, darf nicht still in der Datenbank
// landen — sonst beantwortet die Lückenabfrage später eine andere Frage als die
// gestellte.
func TestAnInventedCoverStateIsRefused(t *testing.T) {
	s := openTest(t)
	id := pitfall(t, s, "irgendwas")
	if err := s.SetRegressionCover(id, "maybe", ""); err == nil {
		t.Error("an unknown regression state was accepted")
	}
}
