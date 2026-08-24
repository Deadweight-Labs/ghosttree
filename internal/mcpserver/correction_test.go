package mcpserver

import (
	"context"
	"reflect"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func ledgerEntry(t *testing.T, st *store.Store, title, description string) int64 {
	t.Helper()
	detail, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "bug", Title: title, Description: description,
		Scope: scope.Axes{Project: "github.com/x/y"}, Person: "robin",
	}, Criteria: []string{"eins"}})
	if err != nil {
		t.Fatal(err)
	}
	return detail.Request.ID
}

func TestCorrectionGoesThroughTheTool(t *testing.T) {
	c, st := newTestClient(t)
	id := ledgerEntry(t, st, "Titel", "Eine Begründung, die auf einer wertlosen Messung beruht.")
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}}

	_, _, err := s.handleRequestProgress(context.Background(), nil, RequestProgressInput{
		RequestID: id, Action: "correct",
		Description: "Die Begründung ist gestrichen.",
		Reason:      "Die Messung lag vor der Einführung des Werkzeugs",
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := st.RequestByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail.Request.Description, "gestrichen") {
		t.Errorf("description = %q", detail.Request.Description)
	}
	if detail.Request.Title != "Titel" {
		t.Errorf("a correction that named only the description changed the title to %q", detail.Request.Title)
	}
}

func TestCorrectionWithoutAReasonIsRefusedByTheTool(t *testing.T) {
	c, st := newTestClient(t)
	id := ledgerEntry(t, st, "Titel", "Beschreibung")
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}}

	if _, _, err := s.handleRequestProgress(context.Background(), nil, RequestProgressInput{
		RequestID: id, Action: "correct", Description: "still",
	}); err == nil {
		t.Error("a silent correction went through")
	}
}

func TestRelationRemovalGoesThroughTheTool(t *testing.T) {
	c, st := newTestClient(t)
	a := ledgerEntry(t, st, "A", "a")
	b := ledgerEntry(t, st, "B", "b")
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}}

	if _, _, err := s.handleRequestProgress(context.Background(), nil, RequestProgressInput{
		RequestID: a, Action: "relation_add", RelationKind: "blocks", OtherRequestID: b,
	}); err != nil {
		t.Fatal(err)
	}
	detail, err := st.RequestByID(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Relations) != 1 {
		t.Fatalf("relations = %+v", detail.Relations)
	}

	if _, _, err := s.handleRequestProgress(context.Background(), nil, RequestProgressInput{
		RequestID: a, Action: "relation_remove", RelationID: detail.Relations[0].ID,
		Reason: "Richtung war verdreht",
	}); err != nil {
		t.Fatal(err)
	}
	detail, err = st.RequestByID(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Relations) != 0 {
		t.Errorf("the edge survived removal: %+v", detail.Relations)
	}
}

// The direction of a directed edge has to be readable off the tool, not guessed
// — three edges were set backwards because it was not.
func TestTheToolSaysWhichWayARelationPoints(t *testing.T) {
	schema := fieldDoc(t, RequestProgressInput{}, "RelationKind")
	for _, want := range []string{"request_id blocks other_request_id", "request_id <kind> other_request_id"} {
		if !strings.Contains(schema, want) {
			t.Errorf("the relation_kind description does not state %q: %s", want, schema)
		}
	}
}

// fieldDoc reads the description an agent is shown for one input field.
func fieldDoc(t *testing.T, v any, field string) string {
	t.Helper()
	f, ok := reflect.TypeOf(v).FieldByName(field)
	if !ok {
		t.Fatalf("no field %q", field)
	}
	return f.Tag.Get("jsonschema")
}
