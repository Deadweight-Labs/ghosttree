package store

import (
	"encoding/json"
	"errors"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

func TestRuntimeRequestMutationsUseAdmission(t *testing.T) {
	for _, op := range []string{"create", "criterion", "criterion_state", "complete", "drop", "relation", "remove_relation", "start_work", "finish_work", "update"} {
		t.Run(op, func(t *testing.T) {
			s := runtimeDomainStore(t)
			input := requestdomain.CreateInput{Request: requestdomain.Request{Type: "change", Title: "original", Person: "author"}, IdempotencyKey: "original"}
			if op == "criterion_state" {
				input.Criteria = []string{"observable outcome"}
			}
			initial, err := s.CreateRequest(input)
			if err != nil {
				t.Fatal(err)
			}
			id := initial.Request.ID
			sid, err := s.UpsertSession(Session{Harness: "test", ExternalID: "work"})
			if err != nil {
				t.Fatal(err)
			}
			var relationID, workID int64
			if op == "remove_relation" {
				r, err := s.AddRequestRelation(id, requestdomain.Relation{Kind: "external", ExternalRef: "https://example.invalid/issue"}, "author")
				if err != nil {
					t.Fatal(err)
				}
				relationID = r.ID
			}
			if op == "finish_work" {
				w, _, err := s.StartRequestWork(id, sid, "primary", "author")
				if err != nil {
					t.Fatal(err)
				}
				workID = w.ID
			}
			before, err := s.RequestByID(id)
			if err != nil {
				t.Fatal(err)
			}
			originalJSON, err := json.Marshal(before)
			if err != nil {
				t.Fatal(err)
			}
			evidence := requestdomain.Evidence{Kind: "test", Ref: "go test ./...", Person: "operator"}
			write := func() error {
				switch op {
				case "create":
					_, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "change", Title: "new"}, IdempotencyKey: "created-once"})
					return err
				case "criterion":
					_, err := s.AddCriterion(id, "new outcome", "operator")
					return err
				case "criterion_state":
					return s.SetCriterionState(initial.Criteria[0].ID, "met", evidence)
				case "complete":
					return s.CompleteRequest(id, evidence)
				case "drop":
					return s.DropRequest(id, "no longer needed", "operator")
				case "relation":
					_, err := s.AddRequestRelation(id, requestdomain.Relation{Kind: "external", ExternalRef: "https://example.invalid/new"}, "operator")
					return err
				case "remove_relation":
					return s.RemoveRequestRelation(relationID, "operator", "incorrect")
				case "start_work":
					w, _, err := s.StartRequestWork(id, sid, "primary", "operator")
					workID = w.ID
					return err
				case "finish_work":
					_, err := s.FinishRequestWork(workID, "paused", "handoff", "operator")
					return err
				case "update":
					return s.UpdateRequest(id, map[string]string{"title": "edited"}, "operator", "corrected")
				}
				panic("unknown operation")
			}
			release := holdRuntimeWriter(t, s.writer, 0)
			if err := write(); !errors.Is(err, ErrWriterOperationsFull) {
				t.Errorf("%s bypassed admission: %v", op, err)
			}
			release()
			after, err := s.RequestByID(id)
			if err != nil {
				t.Fatal(err)
			}
			afterJSON, err := json.Marshal(after)
			if err != nil {
				t.Fatal(err)
			}
			if string(afterJSON) != string(originalJSON) {
				t.Fatalf("rejected %s altered request/history/work", op)
			}
			var count int
			if err := s.db.QueryRow("SELECT COUNT(*) FROM requests").Scan(&count); err != nil || count != 1 {
				t.Fatalf("rejected create: %d %v", count, err)
			}
			if err := write(); err != nil {
				t.Fatalf("%s after release: %v", op, err)
			}
			if op == "create" {
				if err := write(); err != nil {
					t.Fatalf("create retry: %v", err)
				}
			}
			if op == "finish_work" {
				w, _, err := s.StartRequestWork(id, sid, "primary", "operator")
				if err != nil || w.ID != workID || w.State != "active" || w.Summary != "handoff" {
					t.Fatalf("resume lost identity/handoff: %+v %v", w, err)
				}
			}
		})
	}
}
