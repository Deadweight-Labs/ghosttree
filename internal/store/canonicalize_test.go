package store

import (
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Canonicalisation arrived at the server boundary but never reached the rows
// already stored, so the same repository sat in the database under several
// spellings and split every project-scoped read.
func TestCanonicalizeScopesRewritesStoredSpellings(t *testing.T) {
	s := openTest(t)
	if _, err := s.InsertKnowledge(Knowledge{Type: "pitfall", Title: "serial console needed",
		Body: "b", Scope: scope.Axes{Project: "github.com/Example/SampleProject"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertSession(Session{Harness: "codex", ExternalID: "s1",
		Scope: scope.Axes{Project: "github.com/Example/SampleProject", Branch: "main"}}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.CanonicalizeScopes(nil); err != nil {
		t.Fatal(err)
	}

	ks, err := s.SearchKnowledge("console", scope.Axes{Project: "github.com/example/sampleproject"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ks) != 1 {
		t.Errorf("knowledge under canonical project = %d, want 1", len(ks))
	}
	sessions, err := s.ListSessions(scope.Axes{Project: "github.com/example/sampleproject"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Errorf("sessions under canonical project = %d, want 1", len(sessions))
	}
}

// A repository that moved owner cannot be derived from its name; the mapping
// has to be supplied.
func TestCanonicalizeScopesAppliesProjectAliases(t *testing.T) {
	s := openTest(t)
	if _, err := s.InsertKnowledge(Knowledge{Type: "pitfall", Title: "serial console needed",
		Body: "b", Scope: scope.Axes{Project: "github.com/Deadweight-Labs/SampleProject"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CanonicalizeScopes(map[string]string{
		"github.com/deadweight-labs/sampleproject": "github.com/example/sampleproject"}); err != nil {
		t.Fatal(err)
	}
	ks, _ := s.SearchKnowledge("console", scope.Axes{Project: "github.com/example/sampleproject"}, 10)
	if len(ks) != 1 {
		t.Errorf("aliased knowledge = %d, want 1", len(ks))
	}
}

// The search projection is a derived copy; leaving it on the old spelling
// would keep the duplicates alive in every FTS result.
func TestCanonicalizeScopesRewritesSearchProjection(t *testing.T) {
	s := openTest(t)
	if _, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "change", Title: "raspi preflight", Description: "verify checksums",
		Scope: scope.Axes{Project: "github.com/Example/SampleProject"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CanonicalizeScopes(nil); err != nil {
		t.Fatal(err)
	}
	page, err := s.SearchRequests(requestdomain.SearchFilter{
		Query: "preflight", Scope: scope.Axes{Project: "github.com/example/sampleproject"}, State: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 1 {
		t.Fatalf("FTS hits under canonical project = %d, want 1", len(page.Results))
	}
}

// Three migration runs under three spellings produced three copies of the same
// backlog entry. Merging keeps the oldest, which is the one work may already
// reference, and reports what it removed.
func TestCanonicalizeScopesMergesDuplicateRequests(t *testing.T) {
	s := openTest(t)
	for _, project := range []string{"github.com/Deadweight-Labs/SampleProject", "github.com/Example/SampleProject", "github.com/example/sampleproject"} {
		if _, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
			Type: "change", Title: "Sieben-Tage-Appliance-Soak", Description: "soak the appliance",
			Scope: scope.Axes{Project: project}}}); err != nil {
			t.Fatal(err)
		}
	}
	report, err := s.CanonicalizeScopes(map[string]string{
		"github.com/deadweight-labs/sampleproject": "github.com/example/sampleproject"})
	if err != nil {
		t.Fatal(err)
	}
	if report.RequestsMerged != 2 {
		t.Errorf("merged = %d, want 2", report.RequestsMerged)
	}
	page, _ := s.SearchRequests(requestdomain.SearchFilter{
		Scope: scope.Axes{Project: "github.com/example/sampleproject"}, State: "open"})
	if len(page.Results) != 1 {
		t.Fatalf("open requests after merge = %d, want 1", len(page.Results))
	}
}

// Requests filed before the write path was fixed carry the branch and machine
// of the session that created them, which hides them from every scope-exact
// read made anywhere else.
func TestCanonicalizeScopesWidensRequestsToProjectScope(t *testing.T) {
	s := openTest(t)
	detail, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "bug", Title: "narrow scope", Description: "d",
		Scope: scope.Axes{Project: "github.com/x/y", Branch: "main", Machine: "workstation-a"}}})
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.CanonicalizeScopes(nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.RequestsWidened != 1 {
		t.Errorf("widened = %d, want 1", report.RequestsWidened)
	}
	got, err := s.RequestByID(detail.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Request.Scope.Branch != "" || got.Request.Scope.Machine != "" {
		t.Errorf("scope = %+v, want project only", got.Request.Scope)
	}
}

// Requests that only look alike must survive: merging is by identical title
// and description within one project, not by title alone.
func TestCanonicalizeScopesKeepsDistinctRequests(t *testing.T) {
	s := openTest(t)
	for _, desc := range []string{"verify checksums", "release the installer"} {
		if _, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
			Type: "change", Title: "Raspi-Preflight", Description: desc,
			Scope: scope.Axes{Project: "github.com/example/sampleproject"}}}); err != nil {
			t.Fatal(err)
		}
	}
	report, err := s.CanonicalizeScopes(nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.RequestsMerged != 0 {
		t.Errorf("merged = %d, want 0: descriptions differ", report.RequestsMerged)
	}
}
