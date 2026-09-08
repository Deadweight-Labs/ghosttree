package store

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRuntimeChunkBatchFIFOAndBoundaries(t *testing.T) {
	cfg := DefaultWriterConfig()
	cfg.MaxBatch = 3
	s, err := OpenRuntime(filepath.Join(t.TempDir(), "batch.db"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.UpsertSession(Session{Harness: "test", ExternalID: "batch"})
	if err != nil {
		t.Fatal(err)
	}
	release := holdRuntimeWriter(t, s.writer, 0)
	var pending []*writerRequest
	appendOne := func(seq int) {
		r, err := s.writer.admitChunks(t.Context(), ChunkBatch{SessionID: id, Chunks: []Chunk{{Seq: seq, Raw: "ok"}}})
		if err != nil {
			t.Fatal(err)
		}
		pending = append(pending, r)
	}
	for i := 0; i < 5; i++ {
		appendOne(i)
	}
	fence, err := s.writer.admit(t.Context(), 0, func() error {
		var n int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM session_chunks").Scan(&n); err != nil {
			return err
		}
		if n != 5 {
			return errors.New("chunk batching crossed a non-chunk operation")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pending = append(pending, fence)
	for i := 5; i < 7; i++ {
		appendOne(i)
	}
	release()
	for _, r := range pending {
		if err := <-r.done; err != nil {
			t.Fatal(err)
		}
	}
	s.writer.mu.Lock()
	batches, operations := s.writer.batches, s.writer.batchedOperations
	s.writer.mu.Unlock()
	if batches != 3 || operations != 7 {
		t.Fatalf("got %d batches / %d operations, want 3/7", batches, operations)
	}
}

func TestRuntimePublicChunksBatchAt64WithoutWaitingForMore(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "public-batch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.UpsertSession(Session{Harness: "test", ExternalID: "public-batch"})
	if err != nil {
		t.Fatal(err)
	}
	release := holdRuntimeWriter(t, s.writer, 0)
	done := make(chan error, 66)
	for i := 0; i < cap(done); i++ {
		go func() { done <- s.AppendChunks(id, []Chunk{{Seq: i, Raw: "batch"}}) }()
		for {
			s.writer.mu.Lock()
			n := s.writer.operations
			s.writer.mu.Unlock()
			if n == i+2 {
				break
			}
			runtime.Gosched()
		}
	}
	release()
	for i := 0; i < cap(done); i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	s.writer.mu.Lock()
	batches := s.writer.batches
	s.writer.mu.Unlock()
	if batches != 2 {
		t.Fatalf("public chunks produced %d groups, want 64+2", batches)
	}
	if err := s.AppendChunks(id, []Chunk{{Seq: 66, Raw: "alone"}}); err != nil {
		t.Fatal(err)
	}
	chunks, err := s.ReadSession(id, 0, 100)
	if err != nil || len(chunks) != 67 {
		t.Fatalf("chunks=%d err=%v", len(chunks), err)
	}
}

func TestRuntimeChunkBatchRollbackAndIsolatedRetry(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "fallback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.UpsertSession(Session{Harness: "test", ExternalID: "fallback"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`CREATE TABLE attempts(seq INTEGER); CREATE TRIGGER record_attempt BEFORE INSERT ON session_chunks BEGIN INSERT INTO attempts VALUES(new.seq); END;
	CREATE TRIGGER reject_bad BEFORE INSERT ON session_chunks WHEN new.raw='bad' BEGIN SELECT RAISE(ABORT,'bad chunk'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	release := holdRuntimeWriter(t, s.writer, 0)
	var pending []*writerRequest
	for i, raw := range []string{"first", "bad", "last"} {
		r, err := s.writer.admitChunks(t.Context(), ChunkBatch{SessionID: id, Chunks: []Chunk{{Seq: i, Raw: raw}}})
		if err != nil {
			t.Fatal(err)
		}
		pending = append(pending, r)
	}
	release()
	for i, r := range pending {
		err := <-r.done
		if (err != nil) != (i == 1) {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	for _, table := range []string{"session_chunks", "attempts"} {
		var n int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 2 {
			t.Fatalf("%s has %d rows: %v", table, n, err)
		}
	}
	s.writer.mu.Lock()
	fallback := s.writer.fallbackOperations
	s.writer.mu.Unlock()
	if fallback != 3 {
		t.Fatalf("fallback requests=%d", fallback)
	}
}
