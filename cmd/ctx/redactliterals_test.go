package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Alle Werte sind erfunden. redactTestPassword steht in redactsessions_test.go.
const redactTestEscValue = "Pa\"ss&w0rd<Fake>"

// redactTestForms sind die Schreibweisen, die nie in einer Ausgabe stehen dürfen.
func redactTestForms(t *testing.T) []string {
	t.Helper()
	var forms []string
	for _, v := range []string{redactTestPassword, "fakeWRD", "7kTest", "QDzt", redactTestEscValue, "Zq7", "fine-value-1"} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		forms = append(forms, v, strings.ToLower(v), string(b[1:len(b)-1]))
	}
	return forms
}

func redactTestLines(text string) (user, thinking, result string) {
	msg := func(typ, role string, content any) string {
		b, _ := json.Marshal(map[string]any{"type": typ, "message": map[string]any{"role": role, "content": content}})
		return string(b)
	}
	return redactTestLine(text),
		msg("assistant", "assistant", []any{map[string]any{"type": "thinking", "thinking": text}}),
		msg("user", "user", []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": text}})
}

// seedLiteralDB legt eine Datenbank an, in der der Wert ohne Beschriftung steht:
// in einer Befehlszeile, als Fragment, kleingeschrieben, im Denkblock, in der
// Werkzeugausgabe, im JSON-escapten Wert und in einem Titel.
func seedLiteralDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "literals"})
	if err != nil {
		t.Fatal(err)
	}
	cmdText := "Merk dir: sed 's/" + redactTestPassword + "/x/' conf"
	lowText := "grep " + strings.ToLower(redactTestPassword) + " ausgabe, nur fakewrd und 7ktest"
	_, thinking, _ := redactTestLines("Der Wert fakeWRD steht in der Konfiguration")
	_, _, result := redactTestLines("Treffer: " + strings.ToLower(redactTestPassword) + "\n" + redactTestEscValue)
	rows := []struct{ text, raw string }{
		{cmdText, redactTestLine(cmdText)},
		{"Notiert.", redactTestLine("Notiert.")},
		{lowText, redactTestLine(lowText)},
		{"", thinking},
		{"", result},
		{redactTestEscValue, redactTestLine(redactTestEscValue)},
	}
	for i, r := range rows {
		if _, err := s.DB().Exec(`INSERT INTO session_chunks(session_id, seq, role, text, raw) VALUES(?,?,?,?,?)`, id, i, "", r.text, r.raw); err != nil {
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

func readLiteralState(t *testing.T, path string) (dump string, hits map[string]int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sb strings.Builder
	for _, q := range []string{
		`SELECT text || '|' || raw FROM session_chunks ORDER BY id`,
		`SELECT text || '|' || thinking || '|' || tool_input || '|' || tool_output FROM chunk_index ORDER BY chunk_id`,
		`SELECT title FROM sessions ORDER BY id`,
	} {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			sb.WriteString(s + "\n")
		}
		rows.Close()
	}
	hits = map[string]int{}
	for _, table := range []string{"chunks_fts", "sess_fts"} {
		for _, word := range []string{"fakewrd", "7ktest", "qdzt", "w0rd", "notiert"} {
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM `+table+` WHERE `+table+` MATCH ?`, word).Scan(&n); err != nil {
				t.Fatal(err)
			}
			hits[table+" "+word] = n
		}
	}
	var ok string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
		t.Fatalf("integrity_check: %q, %v", ok, err)
	}
	return sb.String(), hits
}

func writeLiterals(t *testing.T, mode os.FileMode, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known-values.txt")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile gives the mode only to a new file and not past the umask.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

const redactTestLiterals = "# the leaked value and its parts\n" +
	redactTestPassword + "\nfakeWRD\n7kTest\nQDzt\n" + redactTestEscValue + "\n"

// runRedactSessions runs the command and collects everything it writes: to the
// writer it was given and, in case something is written past it, to the
// process's own standard streams.
func runRedactSessions(t *testing.T, args ...string) (code int, out string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = w, w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	var buf bytes.Buffer
	code = cmdRedactSessions(args, &buf)
	w.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	return code, buf.String() + <-done
}

func TestRedactSessionsLiteralsCommand(t *testing.T) {
	path := seedLiteralDB(t)
	list := writeLiterals(t, 0o600, redactTestLiterals)
	before, hitsBefore := readLiteralState(t, path)
	if hitsBefore["chunks_fts fakewrd"] == 0 || hitsBefore["sess_fts fakewrd"] == 0 || hitsBefore["sess_fts w0rd"] == 0 || !strings.Contains(before, redactTestPassword) {
		t.Fatalf("the seed does not hold the values where the test looks: %v", hitsBefore)
	}

	// Without the switch the label rule has nothing to take: nothing changes.
	code, out := runRedactSessions(t, "--db", path)
	if code != 0 || !strings.Contains(out, "chunks with secrets replaced: 0") {
		t.Fatalf("without the list: code %d\n%s", code, out)
	}
	if st, _ := readLiteralState(t, path); st != before {
		t.Fatal("a run without --literals-file changed the database")
	}

	// --dry-run counts and changes nothing.
	code, out = runRedactSessions(t, "--db", path, "--dry-run", "--literals-file", list)
	if code != 0 {
		t.Fatalf("dry run: code %d: %s", code, out)
	}
	for _, want := range []string{"dry run: nothing is written", "known values: 5", "would replace: 5", "raw/literal", "text/literal", "chunk_index", "sess_fts", "sessions.title", "texts written by people and agents that hold a known value: none"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "merged") || strings.Contains(out, "vacuum") {
		t.Errorf("a dry run reports writes:\n%s", out)
	}
	if st, _ := readLiteralState(t, path); st != before {
		t.Fatal("a dry run changed the database")
	}

	// The real run.
	code, out = runRedactSessions(t, "--db", path, "--batch", "1", "--sleep", "0s", "--literals-file", list)
	if code != 0 {
		t.Fatalf("code %d: %s", code, out)
	}
	for _, want := range []string{"chunks with secrets replaced: 5", "raw/literal", "full-text indexes merged: chunks_fts, sess_fts", "write-ahead log:", "ctx db vacuum --db " + path} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sqlite3") {
		t.Errorf("the output still points to the sqlite3 CLI:\n%s", out)
	}
	after, hits := readLiteralState(t, path)
	for _, form := range redactTestForms(t) {
		if strings.Contains(out, form) {
			t.Errorf("the output holds %q:\n%s", form, out)
		}
		if strings.Contains(after, form) {
			t.Errorf("a table still holds %q:\n%s", form, after)
		}
	}
	for k, n := range hits {
		if strings.HasSuffix(k, "notiert") {
			if k == "chunks_fts notiert" && n != 1 {
				t.Errorf("%s: %d hits, the untouched words must stay searchable", k, n)
			}
			continue
		}
		if n != 0 {
			t.Errorf("%s: %d hits after the run", k, n)
		}
	}
	if !strings.Contains(after, "Merk dir: sed 's/[REDACTED:literal]/x/' conf") || !strings.Contains(after, "Notiert.") {
		t.Errorf("text not redacted in place:\n%s", after)
	}

	// Second run: nothing to do, nothing changes.
	code, out = runRedactSessions(t, "--db", path, "--literals-file", list)
	if code != 0 || !strings.Contains(out, "chunks with secrets replaced: 0") || strings.Contains(out, "cleaned per table") || strings.Contains(out, "vacuum") {
		t.Fatalf("second run: code %d\n%s", code, out)
	}
	if again, _ := readLiteralState(t, path); again != after {
		t.Fatal("the second run changed rows")
	}
}

func TestRedactSessionsLiteralsFileIsChecked(t *testing.T) {
	path := seedLiteralDB(t)
	before, _ := readLiteralState(t, path)
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o620, 0o666, 0o660, 0o606} {
		list := writeLiterals(t, mode, redactTestLiterals)
		code, out := runRedactSessions(t, "--db", path, "--literals-file", list)
		if code != 2 || !strings.Contains(out, "chmod 600") {
			t.Errorf("mode %04o: code %d, want 2 and a hint\n%s", mode, code, out)
		}
		for _, form := range redactTestForms(t) {
			if strings.Contains(out, form) {
				t.Errorf("mode %04o: the output holds %q", mode, form)
			}
		}
	}
	// Owner only is fine, whatever the owner may do.
	for _, mode := range []os.FileMode{0o600, 0o400} {
		list := writeLiterals(t, mode, redactTestLiterals)
		if code, out := runRedactSessions(t, "--db", path, "--dry-run", "--literals-file", list); code != 0 {
			t.Errorf("mode %04o: code %d\n%s", mode, code, out)
		}
	}
	if st, _ := readLiteralState(t, path); st != before {
		t.Fatal("a refused list changed the database")
	}
}

func TestRedactSessionsLiteralsRefusesBadLists(t *testing.T) {
	path := seedLiteralDB(t)
	before, _ := readLiteralState(t, path)
	dir := t.TempDir()
	for name, c := range map[string]struct {
		list func() string
		want string
	}{
		"too short":     {func() string { return writeLiterals(t, 0o600, redactTestLiterals+"Zq7\n") }, "line 7"},
		"empty":         {func() string { return writeLiterals(t, 0o600, "") }, "no value"},
		"only comments": {func() string { return writeLiterals(t, 0o600, "# nothing\n\n") }, "no value"},
		"invalid UTF-8": {func() string { return writeLiterals(t, 0o600, "fine-value-1\n\xff\xfe\xfd\xfc-xyz\n") }, "line 2"},
		"marker":        {func() string { return writeLiterals(t, 0o600, "fine-value-1\nliteral\n") }, "marker"},
		"missing":       {func() string { return filepath.Join(dir, "missing.txt") }, "no such file"},
		"a directory":   {func() string { return dir }, "not a regular file"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out := runRedactSessions(t, "--db", path, "--literals-file", c.list())
			if code != 2 || !strings.Contains(out, c.want) {
				t.Fatalf("code %d, want 2 and %q\n%s", code, c.want, out)
			}
			for _, form := range redactTestForms(t) {
				if strings.Contains(out, form) {
					t.Errorf("the output holds %q:\n%s", form, out)
				}
			}
		})
	}
	// Four characters are the least.
	list := writeLiterals(t, 0o600, "QDzt\n")
	if code, out := runRedactSessions(t, "--db", path, "--dry-run", "--literals-file", list); code != 0 {
		t.Fatalf("a value of four characters: code %d\n%s", code, out)
	}
	if st, _ := readLiteralState(t, path); st != before {
		t.Fatal("a refused list changed the database")
	}
}
