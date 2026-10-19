package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Alle Werte sind erfunden.
const redactTestPassword = "QDzt#fakeWRD%7kTest!"

func redactTestLine(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "timestamp": "2026-10-19T10:00:00Z", "message": map[string]any{"role": "user", "content": text}})
	return string(b)
}

// seedRedactDB legt eine Datenbank an, in der ein Passwort in text, raw, chunks_fts,
// chunk_index, sess_fts und dem Titel steht, wie vor der Schwärzung gespeichert.
func seedRedactDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "redact"})
	if err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{
		"Mailkonto Passwort " + redactTestPassword + " und weiter",
		"Notiert.",
		"export API_KEY=k7k7k7k7m2m2m2m2",
	} {
		if _, err := s.DB().Exec(`INSERT INTO session_chunks(session_id, seq, role, text, raw) VALUES(?,?,?,?,?)`, id, i, "", text, redactTestLine(text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.IndexSession(id); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

type redactState struct{ chunks, index, titles string }

func readRedactState(t *testing.T, path string) (st redactState, hits map[string]int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dump := func(q string) string {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var sb strings.Builder
		for rows.Next() {
			var a, b string
			if err := rows.Scan(&a, &b); err != nil {
				t.Fatal(err)
			}
			sb.WriteString(a + "|" + b + "\n")
		}
		return sb.String()
	}
	st = redactState{
		chunks: dump(`SELECT text, raw FROM session_chunks ORDER BY id`),
		index:  dump(`SELECT text, tool_input FROM chunk_index ORDER BY chunk_id`),
		titles: dump(`SELECT title, '' FROM sessions ORDER BY id`),
	}
	hits = map[string]int{}
	for _, q := range []struct{ table, word string }{
		{"chunks_fts", "fakewrd"}, {"chunks_fts", "qdzt"}, {"chunks_fts", "7ktest"}, {"chunks_fts", "k7k7k7k7m2m2m2m2"},
		{"sess_fts", "fakewrd"}, {"sess_fts", "7ktest"}, {"sess_fts", "k7k7k7k7m2m2m2m2"},
		{"chunks_fts", "mailkonto"}, {"sess_fts", "mailkonto"},
	} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM `+q.table+` WHERE `+q.table+` MATCH ?`, q.word).Scan(&n); err != nil {
			t.Fatal(err)
		}
		hits[q.table+" "+q.word] = n
	}
	var ok string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
		t.Fatalf("integrity_check: %q, %v", ok, err)
	}
	for _, fts := range []string{"chunks_fts", "sess_fts"} {
		if _, err := db.Exec(`INSERT INTO ` + fts + `(` + fts + `, rank) VALUES('integrity-check', 1)`); err != nil {
			t.Fatalf("%s integrity-check: %v", fts, err)
		}
	}
	return st, hits
}

func TestRedactSessionsCommand(t *testing.T) {
	path := seedRedactDB(t)
	before, hitsBefore := readRedactState(t, path)
	if hitsBefore["chunks_fts fakewrd"] != 1 || hitsBefore["sess_fts 7ktest"] != 1 || !strings.Contains(before.chunks, redactTestPassword) || !strings.Contains(before.titles, "fakeWRD") {
		t.Fatalf("the seed does not hold the password where the test looks: %+v %v", before, hitsBefore)
	}

	// --dry-run counts and changes nothing.
	var out bytes.Buffer
	if code := cmdRedactSessions([]string{"--db", path, "--dry-run"}, &out); code != 0 {
		t.Fatalf("dry run: code %d: %s", code, out.String())
	}
	for _, want := range []string{"dry run: nothing is written", "chunks with secrets would replace: 2", "of 3 in the table", "session_chunks.raw", "chunks_fts", "chunk_index", "sess_fts", "sessions.title", "labeled:password", "labeled:apikey"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry run output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "merged") || strings.Contains(out.String(), "VACUUM") {
		t.Errorf("a dry run reports writes:\n%s", out.String())
	}
	if st, _ := readRedactState(t, path); st != before {
		t.Fatal("a dry run changed the database")
	}

	// The real run.
	out.Reset()
	if code := cmdRedactSessions([]string{"--db", path, "--batch", "1", "--sleep", "0s"}, &out); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	for _, want := range []string{"chunks with secrets replaced: 2", "full-text indexes merged: chunks_fts, sess_fts", "write-ahead log:", "VACUUM"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	// The output never holds a value.
	for _, secret := range []string{redactTestPassword, "fakeWRD", "k7k7k7k7m2m2m2m2", "Hx7"} {
		if strings.Contains(out.String(), secret) {
			t.Errorf("the output holds %q:\n%s", secret, out.String())
		}
	}
	after, hits := readRedactState(t, path)
	for _, secret := range []string{redactTestPassword, "fakeWRD", "k7k7k7k7m2m2m2m2"} {
		if strings.Contains(after.chunks+after.index+after.titles, secret) {
			t.Errorf("a table still holds %q:\n%+v", secret, after)
		}
	}
	for k, n := range hits {
		if strings.HasSuffix(k, "mailkonto") {
			if n != 1 {
				t.Errorf("%s: %d hits, the untouched words must stay searchable", k, n)
			}
			continue
		}
		if n != 0 {
			t.Errorf("%s: %d hits after the run", k, n)
		}
	}
	if !strings.Contains(after.chunks, "Mailkonto Passwort [REDACTED:labeled] und weiter") || !strings.Contains(after.chunks, "Notiert.") {
		t.Errorf("text not redacted in place:\n%s", after.chunks)
	}

	// Second run: nothing to do, nothing changes.
	out.Reset()
	if code := cmdRedactSessions([]string{"--db", path}, &out); code != 0 {
		t.Fatalf("second run: code %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "chunks with secrets replaced: 0") || strings.Contains(out.String(), "cleaned per table") || strings.Contains(out.String(), "VACUUM") {
		t.Errorf("second run output:\n%s", out.String())
	}
	if again, _ := readRedactState(t, path); again != after {
		t.Fatal("the second run changed rows")
	}
}

func TestRedactSessionsRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	for name, c := range map[string]struct {
		args []string
		code int
	}{
		"no db":          {[]string{}, 2},
		"bad batch":      {[]string{"--db", "x", "--batch", "0"}, 2},
		"bad sleep":      {[]string{"--db", "x", "--sleep", "-1s"}, 2},
		"bad after":      {[]string{"--db", "x", "--after-id", "-3"}, 2},
		"extra arg":      {[]string{"--db", "x", "y"}, 2},
		"unknown flag":   {[]string{"--db", "x", "--nope"}, 2},
		"missing file":   {[]string{"--db", filepath.Join(dir, "typo.db")}, 1},
		"not a ghost db": {[]string{"--db", dir}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if code := cmdRedactSessions(c.args, &out); code != c.code {
				t.Fatalf("code %d, want %d: %s", code, c.code, out.String())
			}
		})
	}
	if _, err := os.Stat(filepath.Join(dir, "typo.db")); err == nil {
		t.Fatal("a mistyped path created a database")
	}
}
