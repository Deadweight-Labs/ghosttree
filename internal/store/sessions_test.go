package store

import (
	"strconv"
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

// The distiller works a backlog, not a recent window. Selecting by newest-first
// would pin it to the sessions it already processed and leave the archive
// permanently out of reach, however often it runs.
func TestPendingDistillationReturnsOldestUnprocessedFirst(t *testing.T) {
	s := openTest(t)
	seen := map[string]string{"old": "2026-01-01T00:00:00Z", "mid": "2026-02-01T00:00:00Z", "new": "2026-03-01T00:00:00Z"}
	ids := map[string]int64{}
	for name, ts := range seen {
		// The fixture needs a project: selection deliberately skips sessions
		// that ran outside a repository, and this test is about ordering.
		id, err := s.UpsertSession(Session{Harness: "codex", ExternalID: name,
			Scope: scope.Axes{Project: "github.com/x/y"}, StartedAt: ts})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`UPDATE sessions SET last_seen_at=? WHERE id=?`, ts, id); err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	// The newest session is already distilled, exactly the state a newest-first
	// selection gets stuck in.
	if _, err := s.db.Exec(`INSERT INTO session_distillations(session_id,digest,prompt_version,item_count,created_at) VALUES(?,?,?,?,?)`,
		ids["new"], "d1", "v1", 0, "2026-03-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	got, err := s.SessionsPendingDistillation(scope.Axes{}, "2026-06-01T00:00:00Z", "v1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("pending = %d, want 2 (the undistilled ones)", len(got))
	}
	if got[0].ID != ids["old"] || got[1].ID != ids["mid"] {
		t.Errorf("order = %d,%d, want oldest first %d,%d", got[0].ID, got[1].ID, ids["old"], ids["mid"])
	}
}

// A session still being written to must not be distilled mid-flight.
func TestPendingDistillationExcludesSessionsNewerThanCutoff(t *testing.T) {
	s := openTest(t)
	id, err := s.UpsertSession(Session{Harness: "codex", ExternalID: "busy",
		Scope: scope.Axes{Project: "github.com/x/y"}, StartedAt: "2026-08-24T09:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE sessions SET last_seen_at=? WHERE id=?`, "2026-08-24T09:00:00Z", id); err != nil {
		t.Fatal(err)
	}
	got, err := s.SessionsPendingDistillation(scope.Axes{}, "2026-08-24T08:00:00Z", "v1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("pending = %d, want 0: session is still active", len(got))
	}
}

// Claude Code deletes its transcripts after 30 days, so the raw JSONL we hold
// is the only long-term copy. Export must return every line, in file order,
// including the ones the parser never understood.
func TestSessionRawReturnsEveryLineInOrder(t *testing.T) {
	s := openTest(t)
	id, _ := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "raw1", StartedAt: "2026-08-23T00:00:00Z"})
	s.AppendChunks(id, []Chunk{
		{Seq: 2, Role: "assistant", Text: "third", Raw: `{"n":2}`},
		{Seq: 0, Role: "user", Text: "first", Raw: `{"n":0}`},
		{Seq: 1, Role: "other", Text: "", Raw: `{"n":1,"type":"queue-operation"}`},
	})
	lines, err := s.SessionRaw(id)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`{"n":0}`, `{"n":1,"type":"queue-operation"}`, `{"n":2}`}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %v", len(lines), len(want), lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

// ReadSession caps at 200 rows by default; an export that inherited that cap
// would silently truncate every real session.
func TestSessionRawIsNotCappedByReadLimit(t *testing.T) {
	s := openTest(t)
	id, _ := s.UpsertSession(Session{Harness: "codex", ExternalID: "raw2", StartedAt: "2026-08-23T00:00:00Z"})
	var chunks []Chunk
	for i := 0; i < 250; i++ {
		chunks = append(chunks, Chunk{Seq: i, Role: "user", Text: "x", Raw: `{"i":` + strconv.Itoa(i) + `}`})
	}
	s.AppendChunks(id, chunks)
	lines, err := s.SessionRaw(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 250 {
		t.Errorf("got %d lines, want all 250", len(lines))
	}
}

func TestSearchSessions(t *testing.T) {
	s := openTest(t)
	id, _ := s.UpsertSession(Session{Harness: "codex", ExternalID: "x1", Scope: scope.Axes{Project: "github.com/x/y"}, StartedAt: "2026-08-23T00:00:00Z"})
	s.AppendChunks(id, []Chunk{{Seq: 0, Role: "user", Text: "the livekit sfu drops connections", Raw: "{}"}})
	hits, err := s.SearchSessions("livekit", scope.Axes{}, "", 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits = %v, err = %v", hits, err)
	}
	if hits[0].Session.ExternalID != "x1" || hits[0].Snippet == "" {
		t.Errorf("bad hit: %+v", hits[0])
	}
	if hits, _ := s.SearchSessions("livekit", scope.Axes{Project: "github.com/other/z"}, "", 10); len(hits) != 0 {
		t.Error("project filter must exclude")
	}
}

func TestSearchSessionsExcludesTheAskingSession(t *testing.T) {
	s := openTest(t)
	for _, externalID := range []string{"current", "earlier"} {
		id, err := s.UpsertSession(Session{Harness: "codex", ExternalID: externalID,
			Scope: scope.Axes{Project: "github.com/x/y"}, StartedAt: "2026-08-23T00:00:00Z"})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AppendChunks(id, []Chunk{{Seq: 0, Role: "user", Text: "selbstbeleg suchwort", Raw: "{}"}}); err != nil {
			t.Fatal(err)
		}
	}

	hits, err := s.SearchSessions("selbstbeleg", scope.Axes{Project: "github.com/x/y"}, "current", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Session.ExternalID != "earlier" {
		t.Fatalf("hits = %+v, want only the earlier session", hits)
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
