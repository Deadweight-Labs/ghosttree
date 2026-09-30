package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRuntimeSessionWritesUseAdmission(t *testing.T) {
	cfg := DefaultWriterConfig()
	cfg.MaxOperations = 1
	path := filepath.Join(t.TempDir(), "sessions.db")
	s, err := OpenRuntime(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	id, err := s.UpsertSession(Session{Harness: "test", ExternalID: "committed"})
	if err != nil {
		t.Fatal(err)
	}
	release := holdRuntimeWriter(t, s.writer, 0)
	if _, err := s.UpsertSession(Session{Harness: "test", ExternalID: "rejected"}); !errors.Is(err, ErrWriterOperationsFull) {
		t.Errorf("upsert bypassed queue: %v", err)
	}
	if err := s.AppendChunks(id, []Chunk{{Seq: 1, Raw: "a"}}); !errors.Is(err, ErrWriterOperationsFull) {
		t.Errorf("chunks bypassed queue: %v", err)
	}
	if err := s.AppendChunkBatches([]ChunkBatch{{SessionID: id, Chunks: []Chunk{{Seq: 2, Raw: "b"}}}}); !errors.Is(err, ErrWriterOperationsFull) {
		t.Errorf("batches bypassed queue: %v", err)
	}
	release()
	if err := s.AppendChunks(id, []Chunk{{Seq: 1, Raw: "durable"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChunks(id, []Chunk{{Seq: 1, Raw: "durable"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertSession(Session{}); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("closed: %v", err)
	}
	reopened, err := OpenWithOptions(path, OpenOptions{MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	chunks, err := reopened.ReadSession(id, 0, 100)
	if err != nil || len(chunks) != 1 || chunks[0].Raw != "durable" {
		t.Fatalf("durability/retry: %v %v", chunks, err)
	}
	var count int
	if err := reopened.DB().QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count); err != nil || count != 1 {
		t.Fatalf("rejected write persisted: %d %v", count, err)
	}
}

func TestRuntimeSessionAcknowledgesAfterCommit(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ack.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	conn, err := s.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	done := make(chan error, 1)
	go func() { _, err := s.UpsertSession(Session{Harness: "test", ExternalID: "after-commit"}); done <- err }()
	for {
		s.writer.mu.Lock()
		count := s.writer.operations
		s.writer.mu.Unlock()
		if count == 1 {
			break
		}
		runtime.Gosched()
	}
	select {
	case err := <-done:
		t.Fatalf("acknowledged before SQLite acquired connection: %v", err)
	default:
	}
	var count int
	if err := s.reader.db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count); err != nil || count != 0 {
		t.Fatalf("uncommitted read=%d err=%v", count, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.reader.db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count); err != nil || count != 1 {
		t.Fatalf("ack without commit: read=%d err=%v", count, err)
	}
}

func TestRuntimeOpenOwnsSeparatePools(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.writer == nil || s.reader == nil {
		t.Fatal("file-backed Open did not create runtime")
	}
	if s.db.Stats().MaxOpenConnections != 1 || s.reader.db.Stats().MaxOpenConnections != 3 {
		t.Fatal("incorrect connection limits")
	}
	if _, err := s.reader.db.Exec("INSERT INTO machines VALUES('x','x','x')"); err == nil {
		t.Fatal("reader is writable")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.reader.db.Ping(); !errors.Is(err, sql.ErrConnDone) && err == nil {
		t.Fatal("reader pool still open")
	}
	if err := s.db.Ping(); err == nil {
		t.Fatal("writer pool still open")
	}
	if _, err := OpenRuntime(":memory:", DefaultWriterConfig()); err == nil {
		t.Fatal("runtime accepted memory DB without WAL isolation")
	}
	memory, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	if memory.writer != nil {
		t.Fatal("memory store unexpectedly uses runtime")
	}
}
