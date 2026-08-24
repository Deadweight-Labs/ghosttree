package store

import (
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestRequestSchemaDefaultsToOpen(t *testing.T) {
	s := openTest(t)
	var state string
	err := s.DB().QueryRow(`INSERT INTO requests(type,title,description,project,created_at,updated_at)
		VALUES('feature','ledger','body','github.com/x/y','2026-08-24T00:00:00Z','2026-08-24T00:00:00Z')
		RETURNING state`).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	if state != "open" {
		t.Fatalf("state = %q, want open", state)
	}
}

func TestRequestSchemaRejectsUnknownType(t *testing.T) {
	s := openTest(t)
	_, err := s.DB().Exec(`INSERT INTO requests(type,title,description,created_at,updated_at)
		VALUES('epic','bad','bad','2026-08-24T00:00:00Z','2026-08-24T00:00:00Z')`)
	if err == nil {
		t.Fatal("unknown request type accepted")
	}
}

func TestRequestSchemaHasCriteriaEvidenceWorkAndActivity(t *testing.T) {
	s := openTest(t)
	for _, table := range []string{
		"request_criteria", "request_evidence", "request_relations",
		"request_work", "request_activity", "search_documents",
	} {
		var name string
		err := s.DB().QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
}

func TestFreshSchemaUsesSeparatedRequestDomain(t *testing.T) {
	s := openTest(t)
	current, err := SchemaHasNewTypes(s.DB())
	if err != nil {
		t.Fatal(err)
	}
	if !current {
		t.Fatal("fresh schema is not the separated request domain")
	}
}

func TestRequestCompletionRequiresSatisfiedEvidencedCriteria(t *testing.T) {
	s := openTest(t)
	detail, err := s.CreateRequest(requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: "ledger", Description: "body"},
		Criteria: []string{"lists work", "resume work"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Request.State != "open" || len(detail.Criteria) != 2 {
		t.Fatalf("detail = %+v", detail)
	}
	if err := s.CompleteRequest(detail.Request.ID, requestdomain.Evidence{Kind: "commit", Ref: "abc", Person: "robin"}); requestdomain.Code(err) != "open_criteria" {
		t.Fatalf("complete error = %v", err)
	}
	if err := s.SetCriterionState(detail.Criteria[0].ID, "met", requestdomain.Evidence{}); requestdomain.Code(err) != "evidence_required" {
		t.Fatalf("criterion error = %v", err)
	}
}

func TestRequestCanCompleteAfterCriterionEvidence(t *testing.T) {
	s := openTest(t)
	detail, err := s.CreateRequest(requestdomain.CreateInput{
		Request: requestdomain.Request{Type: "feature", Title: "ledger"}, Criteria: []string{"works"},
	})
	if err != nil {
		t.Fatal(err)
	}
	proof := requestdomain.Evidence{Kind: "test", Ref: "go test ./...", Person: "robin"}
	if err := s.SetCriterionState(detail.Criteria[0].ID, "met", proof); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRequest(detail.Request.ID, requestdomain.Evidence{Kind: "commit", Ref: "abc", Person: "robin"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.RequestByID(detail.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Request.State != "done" || got.Criteria[0].State != "met" || len(got.Criteria[0].Evidence) != 1 {
		t.Fatalf("completed detail = %+v", got)
	}
}

func TestTerminalRequestRejectsFurtherLifecycleMutations(t *testing.T) {
	s := openTest(t)
	detail, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "terminal"}})
	if err != nil {
		t.Fatal(err)
	}
	proof := requestdomain.Evidence{Kind: "test", Ref: "go test", Person: "robin"}
	if err := s.CompleteRequest(detail.Request.ID, proof); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddCriterion(detail.Request.ID, "late", "robin"); requestdomain.Code(err) != "request_terminal" {
		t.Fatalf("late criterion error = %v", err)
	}
	if err := s.CompleteRequest(detail.Request.ID, proof); requestdomain.Code(err) != "request_terminal" {
		t.Fatalf("repeat completion error = %v", err)
	}
	if err := s.DropRequest(detail.Request.ID, "changed mind", "robin"); requestdomain.Code(err) != "request_terminal" {
		t.Fatalf("done-to-dropped error = %v", err)
	}
}

func TestDroppedRequestCannotComplete(t *testing.T) {
	s := openTest(t)
	detail, _ := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "dropped"}})
	if err := s.DropRequest(detail.Request.ID, "obsolete", "robin"); err != nil {
		t.Fatal(err)
	}
	err := s.CompleteRequest(detail.Request.ID, requestdomain.Evidence{Kind: "test", Ref: "go test"})
	if requestdomain.Code(err) != "request_terminal" {
		t.Fatalf("error = %v", err)
	}
}

func TestCreateRequestIsIdempotentWithKey(t *testing.T) {
	s := openTest(t)
	in := requestdomain.CreateInput{Request: requestdomain.Request{Type: "change", Title: "same"}, IdempotencyKey: "session-1-task"}
	first, err := s.CreateRequest(in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateRequest(in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Request.ID != second.Request.ID {
		t.Fatalf("ids = %d and %d", first.Request.ID, second.Request.ID)
	}
}

func TestAddCriterionUsesNextStableNumber(t *testing.T) {
	s := openTest(t)
	detail, _ := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "ledger"}, Criteria: []string{"first"}})
	criterion, err := s.AddCriterion(detail.Request.ID, "second", "robin")
	if err != nil {
		t.Fatal(err)
	}
	if criterion.HumanID() != "AC-1.2" {
		t.Fatalf("criterion = %s", criterion.HumanID())
	}
}

func TestSearchRequestsReturnsScopedFTSHit(t *testing.T) {
	s := openTest(t)
	_, _ = s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "feature", Title: "Structured ledger", Description: "associate sessions", Scope: scope.Axes{Project: "github.com/x/y"},
	}})
	_, _ = s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "feature", Title: "Other ledger", Description: "associate sessions", Scope: scope.Axes{Project: "github.com/a/b"},
	}})
	page, err := s.SearchRequests(requestdomain.SearchFilter{Scope: scope.Axes{Project: "github.com/x/y"}, Query: "sessions", State: "open", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 1 || page.Results[0].Request.Title != "Structured ledger" {
		t.Fatalf("results = %+v", page.Results)
	}
}

func TestDropRequestRecordsReason(t *testing.T) {
	s := openTest(t)
	detail, _ := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "ledger"}})
	if err := s.DropRequest(detail.Request.ID, "no longer wanted", "robin"); err != nil {
		t.Fatal(err)
	}
	got, err := s.RequestByID(detail.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Request.State != "dropped" || len(got.Activity) == 0 || got.Activity[len(got.Activity)-1].Data != "no longer wanted" {
		t.Fatalf("detail = %+v", got)
	}
}

func TestAddRequestRelationAppearsInDetail(t *testing.T) {
	s := openTest(t)
	a, _ := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "a"}})
	b, _ := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "change", Title: "b"}})
	_, err := s.AddRequestRelation(a.Request.ID, requestdomain.Relation{Kind: "related", OtherRequestID: b.Request.ID}, "robin")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.RequestByID(a.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Relations) != 1 || got.Relations[0].OtherRequestID != b.Request.ID {
		t.Fatalf("relations = %+v", got.Relations)
	}
}

func TestSessionHasOnePrimaryRequestAndStartIsIdempotent(t *testing.T) {
	s := openTest(t)
	sessionID, err := s.UpsertSession(Session{Harness: "codex", ExternalID: "work-1"})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "a"}})
	b, _ := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "change", Title: "b"}})
	first, _, err := s.StartRequestWork(a.Request.ID, sessionID, "primary", "robin")
	if err != nil {
		t.Fatal(err)
	}
	retry, _, err := s.StartRequestWork(a.Request.ID, sessionID, "primary", "robin")
	if err != nil || retry.ID != first.ID {
		t.Fatalf("retry = %+v, err = %v", retry, err)
	}
	if _, _, err := s.StartRequestWork(b.Request.ID, sessionID, "primary", "robin"); requestdomain.Code(err) != "primary_exists" {
		t.Fatalf("second primary error = %v", err)
	}
}

func TestFinishWorkRequiresHandoffAndRequestSearchFindsTranscript(t *testing.T) {
	s := openTest(t)
	sessionID, _ := s.UpsertSession(Session{Harness: "codex", ExternalID: "work-2"})
	_ = s.AppendChunks(sessionID, []Chunk{{Seq: 3, Role: "assistant", Text: "the migration checksum now matches", Raw: `{}`}})
	detail, _ := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "change", Title: "migration"}})
	work, _, _ := s.StartRequestWork(detail.Request.ID, sessionID, "primary", "robin")
	if _, err := s.FinishRequestWork(work.ID, "paused", "", "robin"); requestdomain.Code(err) != "summary_required" {
		t.Fatalf("missing summary error = %v", err)
	}
	finished, err := s.FinishRequestWork(work.ID, "paused", "schema copied; deploy remains", "robin")
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != "paused" || finished.EndedAt == "" {
		t.Fatalf("finished = %+v", finished)
	}
	page, err := s.SearchRequestSessions(detail.Request.ID, "checksum", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 1 || page.Results[0].ChunkSeq != 3 || page.Results[0].Handoff == "" {
		t.Fatalf("hits = %+v", page.Results)
	}
}

// A session that works a backlog has several main tasks in sequence. The guard
// against two primaries was written as "one per session, ever" rather than "one
// at a time", so the second request onwards was forced into role=related and
// every later reading of "what did this session mainly work on" was wrong.
// Measured in session 1828: REQ-74 was completed, and starting REQ-84 as primary
// was still refused, naming the finished request as the blocker.
func TestFinishedPrimaryWorkFreesTheSessionForTheNextOne(t *testing.T) {
	s := openTest(t)
	a, _ := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "first task"}})
	b, _ := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "change", Title: "second task"}})
	first, second := a.Request.ID, b.Request.ID
	sessionID, _ := s.UpsertSession(Session{Harness: "codex", ExternalID: "backlog"})

	work, _, err := s.StartRequestWork(first, sessionID, "primary", "robin")
	if err != nil {
		t.Fatal(err)
	}
	// An active primary still blocks a second one: the rule is one at a time.
	if _, _, err := s.StartRequestWork(second, sessionID, "primary", "robin"); requestdomain.Code(err) != "primary_exists" {
		t.Fatalf("second concurrent primary was accepted: %v", err)
	}
	if _, err := s.FinishRequestWork(work.ID, "completed", "done", "robin"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartRequestWork(second, sessionID, "primary", "robin"); err != nil {
		t.Fatalf("finished primary still blocks the next one: %v", err)
	}
}

// Pausing and resuming the same request in one session is the normal shape of
// long work, not a constraint violation.
func TestPausedPrimaryWorkCanBeResumed(t *testing.T) {
	s := openTest(t)
	created, _ := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "long task"}})
	id := created.Request.ID
	sessionID, _ := s.UpsertSession(Session{Harness: "codex", ExternalID: "resumed"})
	work, _, err := s.StartRequestWork(id, sessionID, "primary", "robin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRequestWork(work.ID, "paused", "back later", "robin"); err != nil {
		t.Fatal(err)
	}
	resumed, _, err := s.StartRequestWork(id, sessionID, "primary", "robin")
	if err != nil {
		t.Fatalf("resuming paused work failed: %v", err)
	}
	if resumed.ID != work.ID {
		t.Fatalf("resume created work %d, want the existing %d", resumed.ID, work.ID)
	}
	if resumed.State != "active" {
		t.Fatalf("resumed work is %q, want active: a session with no active primary while it works reads as idle", resumed.State)
	}
}

// A search page is a list. It used to carry every description in full, so
// twenty-four requests came to 64,462 characters — past what a tool result may
// return, and past what a card or a terminal row can show. The snippet is what
// a list wants; the whole text belongs to the single-request view.
func TestSearchPageCarriesASnippetNotTheWholeDescription(t *testing.T) {
	s := openTest(t)
	long := strings.Repeat("Ausführliche Begründung mit Beleg und Abwägung. ", 80)
	if _, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "bug", Title: "Langer Befund", Description: long,
		Scope: scope.Axes{Project: "p"}}}); err != nil {
		t.Fatal(err)
	}
	page, err := s.SearchRequests(requestdomain.SearchFilter{Scope: scope.Axes{Project: "p"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 1 {
		t.Fatalf("results = %d", len(page.Results))
	}
	got := page.Results[0].Request.Description
	if len([]rune(got)) > 240 {
		t.Errorf("snippet is %d runes, want a snippet", len([]rune(got)))
	}
	if !strings.HasPrefix(got, "Ausführliche Begründung") {
		t.Errorf("snippet lost its beginning: %.60q", got)
	}
	// The full text stays reachable where it belongs.
	detail, err := s.RequestByID(page.Results[0].Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Request.Description) != len(long) {
		t.Errorf("detail description = %d chars, want the full %d", len(detail.Request.Description), len(long))
	}
}
