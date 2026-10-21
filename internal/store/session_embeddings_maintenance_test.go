package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embed/embedtest"
	"github.com/Deadweight-Labs/ghosttree/internal/redact"
)

// ctx redact-sessions and ctx strip-images change what is stored for a chunk. A
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
	// session is a tombstone, or the session would not be looked at again.
	if n := rowsIn(t, raw, `SELECT count(*) FROM session_embeddings`); n != 2 {
		t.Fatalf("%d vectors left, want the two of the unchanged chunks", n)
	}
	if n := rowsIn(t, raw, `SELECT count(*) FROM session_embeddings e JOIN session_chunks c ON c.id = e.chunk_id WHERE c.seq = 0`); n != 0 {
		t.Fatal("the vector of the changed chunk is still there")
	}
	if n := rowsIn(t, raw, `SELECT count(*) FROM session_embedding_sync WHERE session_id = ? AND version = 0`, session); n != 1 {
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
	if n := rowsIn(t, raw, `SELECT count(*) FROM session_embedding_sync WHERE session_id = ? AND version = 0`, session); n != 1 {
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

// The look finds a session complete; a redaction then drops the vector of a chunk
// and the mark of the session; and the touch for that look arrives afterwards. It
// must not bring the old mark back: the session would not be looked at again until
// it got a chunk, and the vector that is gone would not be made again.
func TestSessionMarkOfALookMadeBeforeARedactionDoesNotComeBack(t *testing.T) {
	const leak = "Kakadu77Fake"
	path, session, _, c := seedMaintained(t, leak)
	lits, err := redact.NewLiterals([]string{leak})
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// A look that is due: the session has no mark under these rules yet.
	if _, err := s.db.Exec(`DELETE FROM session_embedding_sync`); err != nil {
		t.Fatal(err)
	}
	due, err := s.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false)
	if err != nil || len(due.Items) != 0 || !slices.Equal(due.Unchanged, []int64{session}) {
		t.Fatalf("the look finds the session complete: %+v %v", due, err)
	}

	// The redaction comes after the look, the touch after the redaction.
	if _, err := RedactSessions(context.Background(), path, RedactSessionsOptions{Literals: lits}); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchSessionEmbeddings(modelOf(c), due.Seen, due.ReadAt); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := s.db.QueryRow(`SELECT version FROM session_embedding_sync WHERE session_id = ?`, session).Scan(&version); err != nil || version != 0 {
		t.Fatalf("the touch of a look before the redaction renewed the mark: version %d, %v", version, err)
	}
	// So the session is looked at again, and the chunk that lost its vector is due.
	again, err := s.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false)
	if err != nil || len(again.Items) != 1 || len(again.Unchanged) != 0 {
		t.Fatalf("the session is looked at again: %+v %v", again, err)
	}
	if got := embedSessionTexts(t, s, c, time.Now()); got != 1 {
		t.Fatalf("%d texts made again", got)
	}
}

// A database whose mark of a session has no generation (the first shape of the
// table, or the one with the model and the version of the rules) cannot hold a
// tombstone: the command deletes the mark, as it always did, and does not fail on a
// column that is not there. The server gives the table its columns when it starts,
// and the session is looked at again.
func TestMaintenanceCommandsDeleteTheMarkOfATableWithoutGenerations(t *testing.T) {
	for name, ddl := range map[string]string{
		"first shape":         `CREATE TABLE session_embedding_sync(session_id INTEGER NOT NULL PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE, read_at TEXT NOT NULL) WITHOUT ROWID`,
		"with the rules only": `CREATE TABLE session_embedding_sync(session_id INTEGER NOT NULL PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE, read_at TEXT NOT NULL, version INTEGER NOT NULL DEFAULT 0, model TEXT NOT NULL DEFAULT '', prefix_version TEXT NOT NULL DEFAULT '') WITHOUT ROWID`,
	} {
		t.Run(name, func(t *testing.T) {
			const leak = "Kakadu77Fake"
			path, session, _, c := seedMaintained(t, leak)
			lits, err := redact.NewLiterals([]string{leak})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			for _, q := range []string{`DROP TABLE session_embedding_sync`, ddl} {
				if _, err := raw.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := raw.Exec(`INSERT INTO session_embedding_sync(session_id, read_at) VALUES(?, '2099-01-01T00:00:00Z')`, session); err != nil {
				t.Fatal(err)
			}
			rep, err := RedactSessions(context.Background(), path, RedactSessionsOptions{Literals: lits})
			if err != nil || rep.Tables["session_embeddings"] != 1 {
				t.Fatalf("%v %+v", err, rep.Tables)
			}
			if n := rowsIn(t, raw, `SELECT count(*) FROM session_embedding_sync WHERE session_id = ?`, session); n != 0 {
				t.Fatal("the mark of the earlier shape stayed")
			}
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			due, err := s.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false)
			if err != nil || len(due.Items) != 1 {
				t.Fatalf("the session is looked at again: %+v %v", due, err)
			}
		})
	}
}

// The text of a vector is made from the stored line alone. A redaction that changes
// only the text column of a chunk (a value that is in the text and not in the line)
// leaves its vector and the mark of its session as they are; one that changes the
// line drops the vector and leaves a tombstone.
func TestRedactSessionsDropsTheVectorOnlyOfAChunkWhoseLineChanged(t *testing.T) {
	const leak = "Kakadu77Fake"
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, c := newFakeEmbedder(t)
	textOnly, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "text-only"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChunks(textOnly, []Chunk{
		{Seq: 0, Role: "user", Text: "please keep the value " + leak + " in mind", Raw: promptLine(0, "please keep the value in mind for the s1 s2 job")},
		{Seq: 1, Role: "user", Text: "and a second prompt, s3 s4", Raw: promptLine(1, "and a second prompt without any secret in it, s3 s4")},
	}); err != nil {
		t.Fatal(err)
	}
	inLine, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "in-line"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChunks(inLine, chunksOf(
		promptLine(0, "please keep the value "+leak+" in mind for the s1 s2 job"),
		promptLine(1, "and a second prompt without any secret in it, s3 s4"),
	)); err != nil {
		t.Fatal(err)
	}
	if got := embedSessionTexts(t, s, c, time.Now()); got != 4 {
		t.Fatalf("%d texts embedded", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	lits, err := redact.NewLiterals([]string{leak})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := RedactSessions(context.Background(), path, RedactSessionsOptions{Literals: lits})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Tables["session_chunks.text"] < 1 || rep.Tables["session_embeddings"] != 1 {
		t.Fatalf("tables: %v", rep.Tables)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if n := rowsIn(t, raw, `SELECT count(*) FROM session_chunks WHERE session_id = ? AND text LIKE ?`, textOnly, "%"+leak+"%"); n != 0 {
		t.Fatal("the text column was not redacted: the test checks nothing")
	}
	vectors := func(session int64) int {
		return rowsIn(t, raw, `SELECT count(*) FROM session_embeddings e JOIN session_chunks c ON c.id = e.chunk_id WHERE c.session_id = ?`, session)
	}
	if got := vectors(textOnly); got != 2 {
		t.Fatalf("a change of the text column dropped a vector: %d left of 2", got)
	}
	if got := vectors(inLine); got != 1 {
		t.Fatalf("a change of the line left %d vectors of 2, want one", got)
	}
	marked := func(session int64) (version int, gen int64) {
		t.Helper()
		if err := raw.QueryRow(`SELECT version, gen FROM session_embedding_sync WHERE session_id = ?`, session).Scan(&version, &gen); err != nil {
			t.Fatal(err)
		}
		return version, gen
	}
	if v, gen := marked(textOnly); v != sessionTextVersion || gen != 0 {
		t.Fatalf("the mark of the session whose text column changed: version %d, generation %d", v, gen)
	}
	if v, gen := marked(inLine); v != 0 || gen != 1 {
		t.Fatalf("the mark of the session whose line changed is no tombstone: version %d, generation %d", v, gen)
	}
}
