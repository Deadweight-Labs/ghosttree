package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const measuredDefaultFileMaxOpenConns = 1

func TestPoolWALReaderDoesNotWaitForWriterConnection(t *testing.T) {
	for _, maxOpen := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("pool_%d", maxOpen), func(t *testing.T) {
			s := openPoolTestStore(t, maxOpen)
			if _, err := s.PutGhostFile(GhostFile{Project: "pool", Path: "seed.go", Description: "before"}); err != nil {
				t.Fatal(err)
			}

			ctx := context.Background()
			conn, err := s.DB().Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
				t.Fatal(err)
			}
			rolledBack := false
			defer func() {
				if !rolledBack {
					_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
				}
			}()
			if _, err := conn.ExecContext(ctx, `UPDATE ghost_files SET description='uncommitted' WHERE project='pool' AND path='seed.go'`); err != nil {
				t.Fatal(err)
			}

			readDone := make(chan error, 1)
			go func() {
				got, err := s.GhostFileByPath("pool", "seed.go")
				if err == nil && got.Description != "before" {
					err = fmt.Errorf("reader saw %q, want prior WAL snapshot", got.Description)
				}
				readDone <- err
			}()

			select {
			case err := <-readDone:
				if maxOpen == 1 {
					t.Fatal("single-connection read completed while writer held the only connection")
				}
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(250 * time.Millisecond):
				if maxOpen > 1 {
					t.Fatal("WAL reader blocked behind writer with a multi-connection pool")
				}
			}

			if _, err := conn.ExecContext(ctx, `ROLLBACK`); err != nil {
				t.Fatal(err)
			}
			rolledBack = true
			if maxOpen == 1 {
				if err := conn.Close(); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-readDone:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("single-connection read did not resume after writer rollback")
				}
			}
		})
	}
}

func TestPoolConcurrentRepresentativeWritersStayAtomic(t *testing.T) {
	s := openPoolTestStore(t, measuredDefaultFileMaxOpenConns)
	const batches = 9
	start := make(chan struct{})
	errs := make(chan error, batches)
	var wg sync.WaitGroup
	for i := range batches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- representativeWriteBatch(s, i)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	assertTableCount(t, s.DB(), "ghost_files", batches)
	assertTableCount(t, s.DB(), "ghost_file_versions", 0)
	assertTableCount(t, s.DB(), "sessions", batches)
	assertTableCount(t, s.DB(), "session_chunks", batches)
	assertTableCount(t, s.DB(), "migration_runs", batches)
	assertTableCount(t, s.DB(), "migration_artifacts", batches)
}

func TestOpenUsesMeasuredFilePoolDefault(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ghosttree.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if got := s.DB().Stats().MaxOpenConnections; got != measuredDefaultFileMaxOpenConns {
		t.Fatalf("default = %d, want %d", got, measuredDefaultFileMaxOpenConns)
	}

	memory, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memory.Close() })
	if got := memory.DB().Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("memory default = %d, want 1", got)
	}
}

func BenchmarkPoolWrites(b *testing.B) {
	benchmarkPools(b, false)
}

func BenchmarkPoolMixed(b *testing.B) {
	benchmarkPools(b, true)
}

func benchmarkPools(b *testing.B, mixed bool) {
	for _, maxOpen := range []int{1, 2, 4} {
		b.Run(fmt.Sprintf("pool_%d", maxOpen), func(b *testing.B) {
			s, err := OpenWithOptions(filepath.Join(b.TempDir(), "ghosttree.db"), OpenOptions{MaxOpenConns: maxOpen})
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			if err := representativeWriteBatch(s, -1); err != nil {
				b.Fatal(err)
			}

			before := s.RuntimeStats().DB
			var sequence atomic.Int64
			var failures atomic.Int64
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					n := int(sequence.Add(1))
					var err error
					if !mixed || n%4 == 0 {
						err = representativeWriteBatch(s, n)
					} else if n%2 == 0 {
						_, err = s.GhostFileByPath("pool", "file--1.go")
					} else {
						_, err = s.SessionByID(1)
					}
					if err != nil {
						message := strings.ToLower(err.Error())
						if !strings.Contains(message, "database is locked") && !strings.Contains(message, "sqlite_busy") {
							b.Errorf("operation %d: unexpected error: %v", n, err)
							return
						}
						failures.Add(1)
					}
				}
			})
			b.StopTimer()
			after := s.RuntimeStats().DB
			b.ReportMetric(float64(after.WaitCount-before.WaitCount)/float64(b.N), "waits/op")
			b.ReportMetric(float64((after.WaitDuration-before.WaitDuration).Nanoseconds())/1e6/float64(b.N), "wait_ms/op")
			b.ReportMetric(float64(failures.Load())/float64(b.N), "errors/op")
		})
	}
}

func openPoolTestStore(t testing.TB, maxOpen int) *Store {
	t.Helper()
	s, err := OpenWithOptions(filepath.Join(t.TempDir(), "ghosttree.db"), OpenOptions{MaxOpenConns: maxOpen})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func representativeWriteBatch(s *Store, n int) error {
	key := fmt.Sprintf("%d", n)
	if _, err := s.PutGhostFile(GhostFile{Project: "pool", Path: "file-" + key + ".go", Description: "benchmark " + key}); err != nil {
		return fmt.Errorf("put ghost: %w", err)
	}
	sessionID, err := s.UpsertSession(Session{Harness: "codex", ExternalID: "pool-" + key, Scope: scope.Axes{Project: "pool"}})
	if err != nil {
		return fmt.Errorf("upsert session: %w", err)
	}
	if err := s.AppendChunks(sessionID, []Chunk{{Seq: 0, Role: "user", Text: "pool", Raw: `{}`}}); err != nil {
		return fmt.Errorf("append chunks: %w", err)
	}
	if _, err := s.BeginMigration("pool-"+key, map[string]string{"source.md": "digest-" + key}); err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	return nil
}

func assertTableCount(t testing.TB, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s rows = %d, want %d", table, got, want)
	}
}
