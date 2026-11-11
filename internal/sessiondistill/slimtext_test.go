package sessiondistill

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/chunktext"
	"github.com/Deadweight-Labs/ghosttree/internal/redact"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The distiller gets the same transcript, digest and prompt after ctx db
// slim-text as before: it reads through ReadSession, which derives the text.
func TestDistillerSeesTheSameSessionAfterSlimText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"We decided to use SQLite because one writer is enough."}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Agreed, SQLite with WAL it is."},{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"PASS"}]}]}}`,
		`{"type":"progress","data":"x"}`,
		`{"type":"user","message":{"role":"user","content":"Ünïcode stays as it is, ok?"}}`,
	}
	id, err := s.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "d", Scope: scope.Axes{Project: "p"}, StartedAt: "2026-11-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	var chunks []store.Chunk
	for i, line := range lines {
		p := chunktext.Parse("claude-code")([]byte(line))
		chunks = append(chunks, store.Chunk{Seq: i, Role: p.Role, Text: redact.Redact(p.Text), Raw: line})
	}
	if err := s.AppendChunks(id, chunks); err != nil {
		t.Fatal(err)
	}
	read := func() []store.Chunk {
		got, err := s.ReadSession(id, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := read()
	titles := []string{"known entry"}
	wantPrompt, wantDigest := Prompt(before, titles), Digest(before)

	// The command runs on the file, like beside a server; the open connection
	// of this test would only make its final checkpoint wait.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	rep, err := store.SlimText(context.Background(), path, store.SlimTextOptions{Pause: time.Millisecond})
	if err != nil || rep.Emptied == 0 {
		t.Fatalf("slim-text: %+v, %v", rep, err)
	}
	if s, err = store.Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after := read()
	if got := Digest(after); got != wantDigest {
		t.Fatalf("digest changed: %s -> %s", wantDigest, got)
	}
	if got := Prompt(after, titles); got != wantPrompt {
		t.Fatalf("prompt changed:\n%s\n---\n%s", wantPrompt, got)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("chunk %d: %+v -> %+v", i, before[i], after[i])
		}
	}
	// A quote from the text the model was given is accepted, a foreign one is not.
	if _, err := Parse(`{"items":[{"type":"decision","title":"t","body":"b","quote":"one writer is enough","chunk_seq":0}]}`, after); err != nil {
		t.Fatalf("a quote from the derived text was refused: %v", err)
	}
	if items, err := Parse(`{"items":[{"type":"decision","title":"t","body":"b","quote":"never said","chunk_seq":0}]}`, after); err == nil && len(items) > 0 {
		t.Fatalf("a foreign quote was accepted: %+v", items)
	}
}
