package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embed/embedtest"
	"github.com/Deadweight-Labs/ghosttree/internal/redact"
)

// ctx redact-sessions and ctx strip-blobs change what is stored for a chunk. A
// vector that was made from the chunk before is made from what they take out, so
// it goes with the change, and the job makes a new one from the text as it is
// now. These tests run the commands on a database file, as they run in service.

// seedMaintained stores a session with two prompts and an answer, embeds all
// three texts and closes the store. leak is in the first prompt.
func seedMaintained(t *testing.T, leak string) (path string, session int64, srv *embedtest.Server, c *embed.Client) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	srv, c = newFakeEmbedder(t)
	session, err = s.UpsertSession(Session{Harness: "claude-code", ExternalID: "maintained"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChunks(session, chunksOf(
		promptLine(0, "please keep the value "+leak+" in mind for the s1 s2 job"),
		answerLine(1, "kept in mind, nothing else about s3 s4 to say"),
		promptLine(2, "and a second prompt without any secret in it, s1 s3"),
	)); err != nil {
		t.Fatal(err)
	}
	if got := embedSessionTexts(t, s, c, time.Now()); got != 3 {
		t.Fatalf("%d texts embedded", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path, session, srv, c
}

func rowsIn(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRedactSessionsDropsTheVectorsOfTheChunksItChanges(t *testing.T) {
	const leak = "Kakadu77Fake"
	path, session, srv, c := seedMaintained(t, leak)
	if !strings.Contains(strings.Join(flatten(srv.Requests()), "\n"), leak) {
		t.Fatal("the value that no pattern knows was not in the embedded text: the test checks nothing")
	}
	lits, err := redact.NewLiterals([]string{leak})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	// A dry run counts what it would drop and drops nothing.
	dry, err := RedactSessions(context.Background(), path, RedactSessionsOptions{DryRun: true, Literals: lits})
	if err != nil || dry.Tables["session_embeddings"] != 1 {
		t.Fatalf("dry run: %v %+v", err, dry.Tables)
	}
	if n := rowsIn(t, raw, `SELECT count(*) FROM session_embeddings`); n != 3 {
		t.Fatalf("a dry run dropped vectors: %d", n)
	}

	rep, err := RedactSessions(context.Background(), path, RedactSessionsOptions{Literals: lits})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Tables["session_embeddings"] != 1 {
		t.Fatalf("tables: %v", rep.Tables)
	}
	// Only the vector of the chunk that changed is gone; the mark "looked at" of its
	// session with it, or the session would not be looked at again.
	if n := rowsIn(t, raw, `SELECT count(*) FROM session_embeddings`); n != 2 {
		t.Fatalf("%d vectors left, want the two of the unchanged chunks", n)
	}
	if n := rowsIn(t, raw, `SELECT count(*) FROM session_embeddings e JOIN session_chunks c ON c.id = e.chunk_id WHERE c.seq = 0`); n != 0 {
		t.Fatal("the vector of the changed chunk is still there")
	}
	if n := rowsIn(t, raw, `SELECT count(*) FROM session_embedding_sync WHERE session_id = ?`, session); n != 0 {
		t.Fatal("the session is still marked complete")
	}

	// The server makes the vector again, from the text as it is now.
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fresh, c2 := newFakeEmbedder(t)
	if got := embedSessionTexts(t, s, c2, time.Now()); got != 1 {
		t.Fatalf("%d texts made again", got)
	}
	inputs := strings.Join(flatten(fresh.Requests()), "\n")
	if strings.Contains(inputs, leak) || !strings.Contains(inputs, "[REDACTED:literal]") {
		t.Fatalf("the new vector was made from %q", inputs)
	}
	if n := rowsIn(t, s.db, `SELECT count(*) FROM session_embeddings`); n != 3 {
		t.Fatalf("%d vectors", n)
	}
	_ = c
}

func flatten(reqs [][]string) []string {
	var out []string
	for _, r := range reqs {
		out = append(out, r...)
	}
	return out
}

func TestStripBlobsDropsTheVectorsOfTheChunksItChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	srv, c := newFakeEmbedder(t)
	session, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "pictures"})
	if err != nil {
		t.Fatal(err)
	}
	payload := pixels(4096, 7)
	withImage, _ := json.Marshal(map[string]any{"type": "user", "timestamp": "2026-10-01T10:00:00Z", "message": map[string]any{"role": "user",
		"content": []any{
			map[string]any{"type": "text", "text": "look at this screenshot and tell me what is wrong with it"},
			map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": payload}},
		}}})
	// What an old client left: written without the images replaced.
	legacyChunks(t, s, session, []Chunk{
		{Seq: 0, Raw: string(withImage)},
		{Seq: 1, Raw: promptLine(1, "a prompt without a picture, about s1 s2 and s3")},
	})
	resetBackfill(t, s)
	if err := s.RunIndexBackfill(context.Background(), BackfillOptions{Batch: 10}); err != nil {
		t.Fatal(err)
	}
	if got := embedSessionTexts(t, s, c, time.Now()); got != 2 {
		t.Fatalf("%d texts embedded", got)
	}
	// The picture is never part of what is sent.
	if strings.Contains(strings.Join(flatten(srv.Requests()), "\n"), payload[:64]) {
		t.Fatal("the image was embedded")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	dry, err := StripSessionBlobs(context.Background(), path, StripBlobsOptions{DryRun: true})
	if err != nil || dry.Changed != 1 || dry.DroppedVectors != 0 {
		t.Fatalf("dry run: %v %+v", err, dry)
	}
	if n := rowsIn(t, raw, `SELECT count(*) FROM session_embeddings`); n != 2 {
		t.Fatalf("a dry run dropped vectors: %d", n)
	}
	rep, err := StripSessionBlobs(context.Background(), path, StripBlobsOptions{})
	if err != nil || rep.Changed != 1 || rep.DroppedVectors != 1 {
		t.Fatalf("%v %+v", err, rep)
	}
	if n := rowsIn(t, raw, `SELECT count(*) FROM session_embeddings`); n != 1 {
		t.Fatalf("%d vectors left, want the one of the chunk without a picture", n)
	}
	if n := rowsIn(t, raw, `SELECT count(*) FROM session_embedding_sync WHERE session_id = ?`, session); n != 0 {
		t.Fatal("the session is still marked complete")
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fresh, c2 := newFakeEmbedder(t)
	if got := embedSessionTexts(t, s, c2, time.Now()); got != 1 {
		t.Fatalf("%d texts made again", got)
	}
	for _, in := range flatten(fresh.Requests()) {
		if strings.Contains(in, payload[:64]) || strings.Contains(in, "[ghosttree: ") || !strings.Contains(in, "screenshot") {
			t.Fatalf("the new vector was made from %q", in)
		}
	}
	// A second run finds nothing to change and drops nothing.
	if rep, err := StripSessionBlobs(context.Background(), path, StripBlobsOptions{}); err != nil || rep.Changed != 0 || rep.DroppedVectors != 0 {
		t.Fatalf("%v %+v", err, rep)
	}
	if n := rowsIn(t, s.db, `SELECT count(*) FROM session_embeddings`); n != 2 {
		t.Fatalf("%d vectors", n)
	}
}

// A database that no server has started with the vectors of the sessions has no
// tables for them: the commands must not fail on it.
func TestMaintenanceCommandsRunOnADatabaseWithoutSessionVectors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "old"})
	if err != nil {
		t.Fatal(err)
	}
	payload := pixels(4096, 9)
	line, _ := json.Marshal(map[string]any{"type": "user", "timestamp": "2026-10-01T10:00:00Z", "message": map[string]any{"role": "user",
		"content": []any{map[string]any{"type": "text", "text": "the old password: hunter2hunter2 and a picture"},
			map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": payload}}}}})
	legacyChunks(t, s, session, []Chunk{{Seq: 0, Raw: string(line)}})
	for _, table := range []string{"session_embeddings", "session_embedding_failures", "session_embedding_sync"} {
		if _, err := s.db.Exec(`DROP TABLE ` + table); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if rep, err := RedactSessions(context.Background(), path, RedactSessionsOptions{}); err != nil || rep.Changed == 0 || rep.Tables["session_embeddings"] != 0 {
		t.Fatalf("redact: %v %+v", err, rep)
	}
	if rep, err := StripSessionBlobs(context.Background(), path, StripBlobsOptions{}); err != nil || rep.Changed != 1 || rep.DroppedVectors != 0 {
		t.Fatalf("strip: %v %+v", err, rep)
	}
}
