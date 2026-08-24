package store

import (
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func makeRequest(t *testing.T, s *Store, title, description string) int64 {
	t.Helper()
	detail, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "bug", Title: title, Description: description, Priority: "mittel",
		Scope: scope.Axes{Project: "github.com/x/y"}, Person: "robin",
	}, Criteria: []string{"erstes Kriterium"}})
	if err != nil {
		t.Fatal(err)
	}
	return detail.Request.ID
}

func TestADescriptionCanBeCorrected(t *testing.T) {
	s := openTest(t)
	id := makeRequest(t, s, "Falscher Titel", "Begründung, die auf einer wertlosen Messung beruht.")

	err := s.UpdateRequest(id, map[string]string{
		"title":       "Richtiger Titel",
		"description": "Die Begründung ist gestrichen, weil die Messung wertlos war.",
	}, "robin", "Messung lag vor der Einführung des Werkzeugs")
	if err != nil {
		t.Fatal(err)
	}

	detail, err := s.RequestByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Request.Title != "Richtiger Titel" {
		t.Errorf("title = %q", detail.Request.Title)
	}
	if !strings.Contains(detail.Request.Description, "gestrichen") {
		t.Errorf("description = %q", detail.Request.Description)
	}
	// Untouched fields stay untouched.
	if detail.Request.Priority != "mittel" || detail.Request.Type != "bug" {
		t.Errorf("a correction changed fields it was not given: %+v", detail.Request)
	}
}

func TestACorrectionPullsTheSearchProjectionWithIt(t *testing.T) {
	s := openTest(t)
	id := makeRequest(t, s, "Alter Titel", "Der Text, der gefunden werden soll: Wegwerfbegruendung.")

	if err := s.UpdateRequest(id, map[string]string{
		"description": "Ersetzt durch eine belegte Fassung: Nachweis.",
	}, "robin", "Beleg nachgereicht"); err != nil {
		t.Fatal(err)
	}

	page, err := s.SearchRequests(requestdomain.SearchFilter{Query: "Wegwerfbegruendung", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 0 {
		t.Errorf("the search still finds the old wording: %+v", page.Results)
	}
	page, err = s.SearchRequests(requestdomain.SearchFilter{Query: "Nachweis", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 1 {
		t.Errorf("the search does not find the corrected wording: %+v", page.Results)
	}
}

func TestACorrectionIsVisibleAndCannotBeSilent(t *testing.T) {
	s := openTest(t)
	id := makeRequest(t, s, "Titel", "Beschreibung")

	if err := s.UpdateRequest(id, map[string]string{"description": "Neue Beschreibung"}, "robin", ""); err == nil {
		t.Error("a correction without a reason was accepted")
	}
	if err := s.UpdateRequest(id, map[string]string{"description": "Neue Beschreibung"}, "robin", "die alte war falsch"); err != nil {
		t.Fatal(err)
	}

	detail, err := s.RequestByID(id)
	if err != nil {
		t.Fatal(err)
	}
	var found requestdomain.Activity
	for _, a := range detail.Activity {
		if a.Kind == "request.corrected" {
			found = a
		}
	}
	if found.Kind == "" {
		t.Fatalf("no correction in the activity list: %+v", detail.Activity)
	}
	for _, want := range []string{"description", "die alte war falsch"} {
		if !strings.Contains(found.Data, want) {
			t.Errorf("the entry does not say %q: %q", want, found.Data)
		}
	}
}

func TestOnlyTheTextFieldsAreCorrectable(t *testing.T) {
	s := openTest(t)
	id := makeRequest(t, s, "Titel", "Beschreibung")

	if err := s.UpdateRequest(id, map[string]string{"state": "done"}, "robin", "Abkürzung"); err == nil {
		t.Error("state was changed through the correction path, bypassing the evidence rule")
	}
	if err := s.UpdateRequest(id, map[string]string{"type": "erfindung"}, "robin", "warum auch immer"); err == nil {
		t.Error("an invalid type was accepted")
	}
}

// A request that is done can still carry a wrong claim, and that is exactly
// when correcting it matters.
func TestAFinishedRequestCanStillBeCorrected(t *testing.T) {
	s := openTest(t)
	id := makeRequest(t, s, "Titel", "Beschreibung")
	detail, _ := s.RequestByID(id)
	if err := s.SetCriterionState(detail.Criteria[0].ID, "waived", requestdomain.Evidence{Kind: "decision", Ref: "nicht nötig", Person: "robin"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRequest(id, requestdomain.Evidence{Kind: "commit", Ref: "abc123", Person: "robin"}); err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateRequest(id, map[string]string{"description": "Korrigiert nach Abschluss"}, "robin", "Zahl war falsch"); err != nil {
		t.Fatalf("a finished request refused a correction: %v", err)
	}
}

func TestAWrongRelationCanBeUndone(t *testing.T) {
	s := openTest(t)
	a := makeRequest(t, s, "REQ A", "a")
	b := makeRequest(t, s, "REQ B", "b")

	// The direction is the whole point: this says A blocks B.
	relation, err := s.AddRequestRelation(a, requestdomain.Relation{Kind: "blocks", OtherRequestID: b}, "robin")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveRequestRelation(relation.ID, "robin", "Richtung war verdreht"); err != nil {
		t.Fatal(err)
	}

	detail, err := s.RequestByID(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Relations) != 0 {
		t.Errorf("the edge is still there: %+v", detail.Relations)
	}
	// Gone from the graph, kept in the log: what was asserted and withdrawn
	// stays readable without the false edge misleading anyone.
	var removed requestdomain.Activity
	for _, act := range detail.Activity {
		if act.Kind == "relation.removed" {
			removed = act
		}
	}
	if removed.Kind == "" {
		t.Fatalf("the removal left no trace: %+v", detail.Activity)
	}
	for _, want := range []string{"blocks", "Richtung war verdreht"} {
		if !strings.Contains(removed.Data, want) {
			t.Errorf("the entry does not say %q: %q", want, removed.Data)
		}
	}
	// Setting it the right way round afterwards must work.
	if _, err := s.AddRequestRelation(b, requestdomain.Relation{Kind: "blocks", OtherRequestID: a}, "robin"); err != nil {
		t.Fatal(err)
	}
}

func TestRemovingAnUnknownRelationSaysSo(t *testing.T) {
	s := openTest(t)
	if err := s.RemoveRequestRelation(4711, "robin", "Tippfehler"); err == nil {
		t.Error("removing a relation that does not exist reported success")
	}
}
