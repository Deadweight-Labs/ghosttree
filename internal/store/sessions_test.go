package store

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestSessionUpsertAndChunks(t *testing.T) {
	s := openTest(t)
	sess := Session{Harness: "claude-code", ExternalID: "abc", Scope: scope.Axes{Project: "github.com/x/y", Machine: "workstation-a"}, CWD: "/home/user", StartedAt: "2026-08-23T00:00:00Z"}
	id, err := s.UpsertSession(sess)
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := s.UpsertSession(sess)
	if id != id2 {
		t.Errorf("upsert must return same id, got %d and %d", id, id2)
	}
	chunks := []Chunk{
		{Seq: 0, Role: "user", Text: "fix the auth bug", Raw: `{"type":"user"}`},
		{Seq: 1, Role: "assistant", Text: "looking at oauth flow", Raw: `{"type":"assistant"}`},
	}
	if err := s.AppendChunks(id, chunks); err != nil {
		t.Fatal(err)
	}
	// Re-sending the same chunks (collector restart) must not error or duplicate.
	if err := s.AppendChunks(id, chunks); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ReadSession(id, 0, 10)
	if len(got) != 2 {
		t.Fatalf("chunks = %d, want 2", len(got))
	}
}

func TestSearchSessions(t *testing.T) {
	s := openTest(t)
	id, _ := s.UpsertSession(Session{Harness: "codex", ExternalID: "x1", Scope: scope.Axes{Project: "github.com/x/y"}, StartedAt: "2026-08-23T00:00:00Z"})
	s.AppendChunks(id, []Chunk{{Seq: 0, Role: "user", Text: "the livekit sfu drops connections", Raw: "{}"}})
	hits, err := s.SearchSessions("livekit", scope.Axes{}, 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits = %v, err = %v", hits, err)
	}
	if hits[0].Session.ExternalID != "x1" || hits[0].Snippet == "" {
		t.Errorf("bad hit: %+v", hits[0])
	}
	if hits, _ := s.SearchSessions("livekit", scope.Axes{Project: "github.com/other/z"}, 10); len(hits) != 0 {
		t.Error("project filter must exclude")
	}
}

func TestListSessions(t *testing.T) {
	s := openTest(t)
	s.UpsertSession(Session{Harness: "codex", ExternalID: "x1", Scope: scope.Axes{Project: "github.com/x/y"}, StartedAt: "2026-08-23T00:00:00Z"})
	s.UpsertSession(Session{Harness: "claude-code", ExternalID: "x2", Scope: scope.Axes{Project: "github.com/other/z"}, StartedAt: "2026-08-23T01:00:00Z"})
	all, err := s.ListSessions(scope.Axes{}, 10)
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %d, err = %v", len(all), err)
	}
	one, _ := s.ListSessions(scope.Axes{Project: "github.com/x/y"}, 10)
	if len(one) != 1 || one[0].ExternalID != "x1" {
		t.Errorf("filtered = %+v", one)
	}
}
