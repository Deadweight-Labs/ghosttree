package store

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"testing"

	"modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

func TestBeginMigrationDoesNotAggregateBeyondSQLiteValueLimit(t *testing.T) {
	for _, runtime := range []bool{false, true} {
		t.Run(fmt.Sprintf("runtime=%t", runtime), func(t *testing.T) {
			s := openMigrationArtifactsTest(t, filepath.Join(t.TempDir(), "migration.db"), runtime)
			defer s.Close()
			conn, err := s.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			_, limitErr := sqlite.Limit(conn, sqlitelib.SQLITE_LIMIT_LENGTH, 4<<20)
			closeErr := conn.Close()
			if limitErr != nil || closeErr != nil {
				t.Fatalf("limit=%v; close=%v", limitErr, closeErr)
			}
			artifacts := map[string]string{}
			for i := range 8 {
				artifacts[fmt.Sprint(i)] = strings.Repeat("\x00", 100000)
			}
			id, err := s.BeginMigration("p", artifacts)
			if err != nil {
				t.Fatalf("individually valid artifacts rejected: %v", err)
			}
			tx, err := s.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			got, err := migrationArtifactsTx(tx, id)
			if err != nil || !maps.Equal(got, artifacts) {
				t.Fatalf("artifact roundtrip changed: equal=%t; err=%v", maps.Equal(got, artifacts), err)
			}
		})
	}
}

func TestBeginMigrationPreservesArtifactBytes(t *testing.T) {
	large := map[string]string{}
	for i := range 2000 {
		large[fmt.Sprintf("path/%04d.md", i)] = fmt.Sprintf("%064d", i)
	}
	cases := []struct {
		name      string
		artifacts map[string]string
	}{
		{"nil", nil},
		{"empty", map[string]string{}},
		{"text", map[string]string{
			"": "", "a\x00b": "c\x00d", "a": "prefix", "\x00": "\x00",
			"quote\"\\\r\n\t": "quote\"\\\r\n\t", "🌳/ä/\u2028\u2029": "𝄞\u2028\u2029",
			"null": "null", "true": "true", "123": "123", "{}": "[]",
			strings.Repeat("long/", 2000): strings.Repeat("digest🌳", 2000),
		}},
		{"invalid-path", map[string]string{"bad\xff\xfe": "good", "other": "ok"}},
		{"invalid-digest", map[string]string{"good": "bad\xff\xfe", "other": "ok"}},
		{"large", large},
	}
	for _, runtime := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("runtime=%t/%s", runtime, tc.name), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "migration.db")
				s := openMigrationArtifactsTest(t, path, runtime)
				id, err := s.BeginMigration("p", tc.artifacts)
				if err != nil {
					t.Fatal(err)
				}
				reused, err := s.BeginMigration("p", tc.artifacts)
				if err != nil || reused != id {
					t.Fatalf("reuse=%d, want %d; err=%v", reused, id, err)
				}
				other, err := s.BeginMigration("other", tc.artifacts)
				if err != nil || other == id {
					t.Fatalf("other project run=%d; err=%v", other, err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s = openMigrationArtifactsTest(t, path, runtime)
				defer s.Close()
				tx, err := s.db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				for _, run := range []int64{id, other} {
					got, err := migrationArtifactsTx(tx, run)
					if err != nil {
						t.Fatal(err)
					}
					if !maps.Equal(got, tc.artifacts) {
						t.Fatalf("artifact bytes changed: got %#v; want %#v", got, tc.artifacts)
					}
				}
			})
		}
	}
}

func TestBeginMigrationArtifactFailureRollsBackRun(t *testing.T) {
	for _, runtime := range []bool{false, true} {
		for _, invalid := range []bool{false, true} {
			t.Run(fmt.Sprintf("runtime=%t/invalid=%t", runtime, invalid), func(t *testing.T) {
				s := openMigrationArtifactsTest(t, filepath.Join(t.TempDir(), "migration.db"), runtime)
				defer s.Close()
				if _, err := s.db.Exec(`CREATE TRIGGER reject_artifact BEFORE INSERT ON migration_artifacts WHEN NEW.path='z-rejected' BEGIN SELECT RAISE(ABORT,'injected artifact failure'); END`); err != nil {
					t.Fatal(err)
				}
				artifacts := map[string]string{"a-kept": "digest", "z-rejected": "digest"}
				if invalid {
					artifacts["a-kept"] = "\xff"
				}
				id, err := s.BeginMigration("p", artifacts)
				if err == nil || id != 0 || !strings.Contains(err.Error(), "injected artifact failure") {
					t.Fatalf("run=%d; err=%v", id, err)
				}
				for _, table := range []string{"migration_runs", "migration_artifacts"} {
					var count int
					if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
						t.Fatalf("%s count=%d; err=%v", table, count, err)
					}
				}
				if _, err := s.db.Exec(`DROP TRIGGER reject_artifact`); err != nil {
					t.Fatal(err)
				}
				if _, err := s.BeginMigration("p", artifacts); err != nil {
					t.Fatalf("retry: %v", err)
				}
			})
		}
	}
}

func openMigrationArtifactsTest(t *testing.T, path string, runtime bool) *Store {
	t.Helper()
	var s *Store
	var err error
	if runtime {
		s, err = OpenRuntime(path, DefaultWriterConfig())
	} else {
		s, err = Open(path)
	}
	if err != nil {
		t.Fatal(err)
	}
	return s
}
