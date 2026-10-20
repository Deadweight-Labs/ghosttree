package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestRedactSessionsLiteralsScale measures ctx redact-sessions --literals-file
// on a large database. It does nothing unless GHOSTTREE_REDACT_SCALE_CHUNKS is
// set, for example
//
//	GHOSTTREE_REDACT_SCALE_CHUNKS=200000 go test ./internal/store/ -run RedactSessionsLiteralsScale -v -timeout 60m
//
// GHOSTTREE_REDACT_SCALE_DIR names a directory in which the database is kept
// (it is built there once and reused); without it the database lives in a
// temporary directory. The chunks are made up: text, tool calls, tool results,
// thinking and some very large results, with every 250th chunk holding the
// invented values in one of five forms. Disk space needed: about 10 KB per chunk.
func TestRedactSessionsLiteralsScale(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("GHOSTTREE_REDACT_SCALE_CHUNKS"))
	if n <= 0 {
		t.Skip("set GHOSTTREE_REDACT_SCALE_CHUNKS to run the measurement")
	}
	dir := os.Getenv("GHOSTTREE_REDACT_SCALE_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	path := filepath.Join(dir, "scale-"+strconv.Itoa(n)+".db")
	planted := (n - 7 + 249) / 250
	if _, err := os.Stat(path); err != nil {
		start := time.Now()
		buildScaleDB(t, path, n)
		t.Logf("built %d chunks in %s", n, time.Since(start).Round(time.Second))
	}
	if info, err := os.Stat(path); err == nil {
		t.Logf("database file: %d MiB", info.Size()>>20)
	}

	lits := literalsFor(t)
	timeRun := func(name string, opts RedactSessionsOptions) RedactSessionsReport {
		start := time.Now()
		rep, err := RedactSessions(context.Background(), path, opts)
		if err != nil {
			t.Fatal(err)
		}
		el := time.Since(start)
		t.Logf("%-34s %8s  %7.0f chunks/s  checked %d, changed %d, optimized %v", name, el.Round(time.Millisecond), float64(rep.Scanned)/el.Seconds(), rep.Scanned, rep.Changed, rep.Optimized)
		return rep
	}
	// Both dry runs read every row and apply every rule; the second adds the values.
	timeRun("dry run, labels only", RedactSessionsOptions{DryRun: true})
	dry := timeRun("dry run, with the values", RedactSessionsOptions{DryRun: true, Literals: lits})
	if dry.Changed < int64(planted) {
		t.Fatalf("the dry run would change %d chunks, %d hold a value", dry.Changed, planted)
	}
	rep := timeRun("real run, with the values", RedactSessionsOptions{Literals: lits})
	if rep.Changed != dry.Changed {
		t.Fatalf("the real run changed %d chunks, the dry run counted %d", rep.Changed, dry.Changed)
	}
	t.Logf("tables %v", rep.Tables)
	t.Logf("kinds %v", rep.Kinds)
	again := timeRun("second run, with the values", RedactSessionsOptions{DryRun: true, Literals: lits})
	if again.Changed != 0 {
		t.Fatalf("a second run still finds %d chunks", again.Changed)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"chunks_fts", "sess_fts"} {
		for _, word := range literalTokens {
			if hits := ftsHits(t, db, table, word); hits != 0 {
				t.Errorf("%s MATCH %q: %d hits", table, word, hits)
			}
		}
	}
	checkIntegrity(t, db)
}

func buildScaleDB(t *testing.T, path string, n int) {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(526))
	vocab := make([]string, 4000)
	for i := range vocab {
		w := make([]byte, 3+rng.Intn(8))
		for k := range w {
			w[k] = byte('a' + rng.Intn(26))
		}
		if i%7 == 0 {
			w[0] -= 'a' - 'A'
		}
		if i%11 == 0 {
			w = append(w, byte('0'+rng.Intn(10)), byte('0'+rng.Intn(10)))
		}
		vocab[i] = string(w)
	}
	// prose makes about size bytes of words with the odd line break, quote,
	// backslash and path, so that the JSON lines hold escape sequences.
	prose := func(size int) string {
		var sb strings.Builder
		for sb.Len() < size {
			sb.WriteString(vocab[rng.Intn(len(vocab))])
			switch rng.Intn(40) {
			case 0:
				sb.WriteString("\n")
			case 1:
				sb.WriteString(" \"" + vocab[rng.Intn(len(vocab))] + "\"")
			case 2:
				sb.WriteString(" /usr/local/" + vocab[rng.Intn(len(vocab))] + "\\n")
			case 3:
				sb.WriteString("\t")
			default:
				sb.WriteString(" ")
			}
		}
		return sb.String()
	}
	line := func(typ, role string, content any) string {
		b, _ := json.Marshal(map[string]any{"type": typ, "timestamp": "2026-10-19T10:00:00Z", "message": map[string]any{"role": role, "content": content}})
		return string(b)
	}
	sessions := (n + 199) / 200
	ids := make([]int64, sessions)
	for i := range ids {
		id, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "scale-" + strconv.Itoa(i)})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO session_chunks(session_id, seq, role, text, raw) VALUES(?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		var text, raw string
		if i%250 == 7 {
			switch (i / 250) % 5 {
			case 0:
				text = "Merk dir: sed 's/" + testPassword + "/x/' conf " + prose(200)
				raw = line("user", "user", text)
			case 1:
				raw = line("user", "user", []any{map[string]any{"type": "tool_result", "tool_use_id": "t", "content": prose(300) + strings.ToLower(testPassword) + " passt\nfakewrd\n" + prose(300)}})
			case 2:
				raw = line("assistant", "assistant", []any{map[string]any{"type": "tool_use", "id": "t", "name": "Bash", "input": map[string]any{"command": "echo '" + litEscValue + "' | grep -r fakeWRD /etc"}}})
			case 3:
				raw = line("assistant", "assistant", []any{map[string]any{"type": "thinking", "thinking": prose(300) + " fakeWRD " + prose(300)}})
			default:
				text = "Ausgabe: 7ktest und fakewrd je Zeile " + prose(200)
				raw = line("user", "user", text)
			}
		} else {
			switch r := rng.Intn(100); {
			case r < 35:
				text = prose(100 + rng.Intn(1400))
				raw = line("assistant", "assistant", []any{map[string]any{"type": "text", "text": text}})
			case r < 55:
				raw = line("assistant", "assistant", []any{map[string]any{"type": "tool_use", "id": "t", "name": "Bash", "input": map[string]any{"command": prose(60 + rng.Intn(300))}}})
			case r < 80:
				raw = line("user", "user", []any{map[string]any{"type": "tool_result", "tool_use_id": "t", "content": prose(200 + rng.Intn(8000))}})
			case r < 95:
				raw = line("assistant", "assistant", []any{map[string]any{"type": "thinking", "thinking": prose(300 + rng.Intn(2500))}})
			default:
				raw = line("user", "user", []any{map[string]any{"type": "tool_result", "tool_use_id": "t", "content": prose(20000 + rng.Intn(40000))}})
			}
		}
		if _, err := stmt.Exec(ids[i/200], i%200, "", text, raw); err != nil {
			t.Fatal(err)
		}
		if i%20000 == 19999 {
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if tx, err = s.db.Begin(); err != nil {
				t.Fatal(err)
			}
			if stmt, err = tx.Prepare(`INSERT INTO session_chunks(session_id, seq, role, text, raw) VALUES(?,?,?,?,?)`); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// The index of the session view is built the way the server builds it.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RunIndexBackfill(context.Background(), BackfillOptions{Batch: 1000, Pause: time.Nanosecond}); err != nil {
		t.Fatal(err)
	}
}
