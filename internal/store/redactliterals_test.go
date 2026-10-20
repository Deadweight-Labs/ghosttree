package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/redact"
)

// Alle Werte in diesen Tests sind erfunden. testPassword und seine Teile
// stehen in redactsessions_test.go; dazu ein Wert mit Zeichen, die ein
// JSON-Encoder als Escape schreibt.
const litEscValue = "Pa\"ss&w0rd<Fake>"

func literalsFor(t *testing.T) *redact.Literals {
	t.Helper()
	l, err := redact.NewLiterals([]string{testPassword, "fakeWRD", "7kTest", "QDzt", litEscValue})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func jsonEscaped(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b[1 : len(b)-1])
}

func redactThinkingLine(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": "2026-10-19T10:00:03Z", "message": map[string]any{"role": "assistant",
		"content": []any{map[string]any{"type": "thinking", "thinking": text}}}})
	return string(b)
}

func redactResultLine(output string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "timestamp": "2026-10-19T10:00:04Z", "message": map[string]any{"role": "user",
		"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": output}}}})
	return string(b)
}

// seedLiterals legt Zeilen an, in denen der bekannte Wert ohne Beschriftung
// steht: in einer Befehlszeile, als Fragment, kleingeschrieben, in einem
// Denkblock, einer Werkzeugausgabe, im Titel und in Zitaten. Sitzung c hat
// einen Prompt, dessen Titel mitten im Wert abgeschnitten ist; Sitzung d hat
// nur einen Titel.
func seedLiterals(t *testing.T, s *Store) (sessions [4]int64) {
	t.Helper()
	var ids [4]int64
	for i, name := range []string{"lit-a", "lit-b", "lit-c", "lit-d"} {
		id, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: name})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	a, b, c, d := ids[0], ids[1], ids[2], ids[3]
	filler := strings.Repeat("x", 70)
	cut := filler + " " + testPassword + " danach"
	if len([]rune(cut)) <= 80 {
		t.Fatal("the title of session c is not cut inside the value")
	}
	rows := []struct {
		session   int64
		seq       int
		text, raw string
	}{
		{a, 0, "Merk dir: sed 's/" + testPassword + "/x/' conf", redactUserLine("Merk dir: sed 's/" + testPassword + "/x/' conf")},
		{a, 1, "Notiert.", redactAssistantLine("Notiert.")},
		{a, 2, "", redactToolLine("grep -r \"" + strings.ToLower(testPassword) + "\" /etc; echo '" + litEscValue + "'")},
		{a, 3, "", redactThinkingLine("Der durchgesickerte Wert fakeWRD steht auch in der Konfiguration")},
		{a, 4, "", redactResultLine(strings.ToLower(testPassword) + " passt\nfakewrd\n" + litEscValue)},
		{b, 0, "Ausgabe: 7ktest und fakewrd je Zeile", redactUserLine("Ausgabe: 7ktest und fakewrd je Zeile")},
		{b, 1, "Passwort: " + testPassword + " ok", redactAssistantLine("Passwort: " + testPassword + " ok")},
		{b, 2, "Zeile ohne Geheimnis", redactAssistantLine("Zeile ohne Geheimnis")},
		{c, 0, cut, redactUserLine(cut)},
	}
	for _, r := range rows {
		if _, err := s.db.Exec(`INSERT INTO session_chunks(session_id, seq, role, text, raw) VALUES(?,?,?,?,?)`, r.session, r.seq, "", r.text, r.raw); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int64{a, b, c} {
		if err := s.IndexSession(id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`UPDATE sessions SET title='Notizen zu fakeWRD' WHERE id=?`, d); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{7, 8} {
		if _, err := s.db.Exec(`INSERT INTO knowledge(id,type,title,body,project,origin,created_at,updated_at) VALUES(?,'note','Eintrag','Text','p','distilled','x','x')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO requests(id,type,title,description,created_at,updated_at) VALUES(5,'change','Auftrag','Text','x','x')`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		knowledge int
		session   int64
		seq       int
		quote     string
	}{
		{7, a, 0, "sed 's/" + testPassword + "/x/'"}, // Zitat des Chunks: bleibt ein Teil des neuen Textes
		{7, a, 99, litEscValue},                      // der Chunk ist nicht (mehr) da
		{8, b, 0, "7ktest und fakewrd"},              // nur Fragmente
	} {
		if _, err := s.db.Exec(`INSERT INTO knowledge_evidence(knowledge_id, session_id, chunk_seq, quote) VALUES(?,?,?,?)`, q.knowledge, q.session, q.seq, q.quote); err != nil {
			t.Fatal(err)
		}
	}
	// Ein Zitat, das nur einen Teil des Wertes schneidet: im neuen Text steht es nicht mehr.
	if _, err := s.db.Exec(`INSERT INTO request_sightings(request_id,session_id,chunk_seq,quote) VALUES(5,?,0,?)`, a, "fakeWRD%7kTest!"); err != nil {
		t.Fatal(err)
	}
	return ids
}

// literalForms sind die Schreibweisen, die nach dem Lauf in keiner Kopie mehr
// stehen dürfen: roh, kleingeschrieben und so, wie JSON sie schreibt.
func literalForms(t *testing.T) []string {
	t.Helper()
	var forms []string
	for _, v := range []string{testPassword, "fakeWRD", "7kTest", "QDzt", litEscValue} {
		for _, f := range []string{v, strings.ToLower(v), jsonEscaped(t, v), jsonEscaped(t, strings.ToLower(v))} {
			forms = append(forms, f)
		}
	}
	return forms
}

// fileNeedles sind Teile der Werte, wie sie in der Datei stehen würden.
var fileNeedles = []string{"fakeWRD", "fakewrd", "7kTest", "7ktest", "QDzt", "qdzt", "w0rd", "Fake"}

// literalTokens sind die Wörter, unter denen der Volltextindex die Werte führt.
var literalTokens = []string{"fakewrd", "7ktest", "qdzt", "w0rd"}

func TestRedactSessionsLiteralsCleanEveryCopy(t *testing.T) {
	for name, opts := range map[string]RedactSessionsOptions{
		"rows":    {Batch: 2},
		"bytes":   {Batch: 1000, BatchBytes: 1},
		"default": {},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ghosttree.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			sessions := seedLiterals(t, s)

			// Vorher steht alles da, in jedem Index und jeder Spalte, und die Prüfungen sehen es.
			for _, probe := range []struct{ table, query string }{
				{"chunks_fts", "fakewrd"}, {"chunks_fts", "7ktest"}, {"chunks_fts", "qdzt"},
				{"sess_fts", "fakewrd"}, {"sess_fts", "thinking:fakewrd"}, {"sess_fts", "tool_output:fakewrd"},
				{"sess_fts", "tool_input:w0rd"}, {"sess_fts", "tool_output:w0rd"}, {"sess_fts", "text:7ktest"},
			} {
				if ftsHits(t, s.db, probe.table, probe.query) == 0 {
					t.Fatalf("%s MATCH %q finds nothing before the run: the test checks nothing", probe.table, probe.query)
				}
			}
			if segmentHits(t, s.db, "chunks_fts", "fakewrd") == 0 || segmentHits(t, s.db, "sess_fts", "w0rd") == 0 {
				t.Fatal("the segment probe finds nothing before the run")
			}
			before := dumpCopies(t, s.db)
			for _, want := range []string{testPassword, strings.ToLower(testPassword), jsonEscaped(t, litEscValue), "Notizen zu fakeWRD"} {
				if !strings.Contains(before, want) {
					t.Fatalf("seed lacks %q", want)
				}
			}
			// The file starts without dead space from the seeding.
			if _, err := s.db.Exec(`VACUUM`); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if len(fileHolds(t, path, fileNeedles...)) == 0 {
				t.Fatal("the file does not hold the values before the run: the file check proves nothing")
			}

			read, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer read.Close()
			before = dumpCopies(t, read)

			lits := literalsFor(t)
			// --dry-run: counts, changes nothing.
			dry := opts
			dry.DryRun, dry.Literals = true, lits
			dryRep, err := RedactSessions(context.Background(), path, dry)
			if err != nil {
				t.Fatal(err)
			}
			if got := dumpCopies(t, read); got != before {
				t.Fatalf("a dry run changed the database:\n%s", got)
			}
			if dryRep.Changed != 7 || dryRep.Scanned != 9 {
				t.Fatalf("dry run: %+v", dryRep)
			}

			opts.Literals = lits
			rep, err := RedactSessions(context.Background(), path, opts)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(rep.Tables, dryRep.Tables) || !reflect.DeepEqual(rep.Kinds, dryRep.Kinds) || rep.Changed != dryRep.Changed || !reflect.DeepEqual(rep.Holders, dryRep.Holders) {
				t.Fatalf("dry run %+v differs from the real run %+v", dryRep, rep)
			}
			if rep.Kinds["text/literal"] == 0 || rep.Kinds["raw/literal"] == 0 {
				t.Fatalf("kinds = %v", rep.Kinds)
			}
			// Text: rows 0 of a and of b, 1 of b and 0 of c. Raw: those and the three
			// rows of a that hold the value only in raw (tool call, thinking, result).
			if rep.Tables["session_chunks.text"] != 4 || rep.Tables["session_chunks.raw"] != 7 || rep.Tables["chunks_fts"] != 4 {
				t.Fatalf("tables = %v", rep.Tables)
			}
			for _, table := range []string{"chunk_index", "sess_fts", "sessions.title", "knowledge_evidence.quote", "request_sightings.quote"} {
				if rep.Tables[table] == 0 {
					t.Errorf("nothing counted for %s: %v", table, rep.Tables)
				}
			}
			if rep.Tables["knowledge_evidence.quote"] != 3 || rep.Tables["request_sightings.quote"] != 1 {
				t.Errorf("quotes = %v", rep.Tables)
			}
			if !reflect.DeepEqual(rep.Optimized, []string{"chunks_fts", "sess_fts"}) || rep.Checkpoint == "" {
				t.Fatalf("optimized %v, checkpoint %q", rep.Optimized, rep.Checkpoint)
			}

			after := dumpCopies(t, read)
			for _, form := range literalForms(t) {
				if strings.Contains(after, form) {
					t.Fatalf("a table still holds %q:\n%s", form, after)
				}
			}
			// What was changed is replaced, not emptied, and raw is still JSON.
			var text, raw string
			if err := read.QueryRow(`SELECT text, raw FROM session_chunks WHERE session_id=? AND seq=0`, sessions[0]).Scan(&text, &raw); err != nil {
				t.Fatal(err)
			}
			if want := "Merk dir: sed 's/[REDACTED:literal]/x/' conf"; text != want || raw != redactUserLine(want) {
				t.Fatalf("text %q raw %s", text, raw)
			}
			// The label rule is not asked first: a known value behind a label is a literal.
			if err := read.QueryRow(`SELECT text FROM session_chunks WHERE session_id=? AND seq=1`, sessions[1]).Scan(&text); err != nil || text != "Passwort: [REDACTED:literal] ok" {
				t.Fatalf("text %q, %v", text, err)
			}
			rows, err := read.Query(`SELECT raw FROM session_chunks ORDER BY id`)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				if err := rows.Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if !json.Valid([]byte(raw)) {
					t.Fatalf("raw is not JSON: %s", raw)
				}
			}
			rows.Close()
			for _, q := range []string{
				`SELECT raw FROM session_chunks WHERE session_id=? AND seq=2`,
				`SELECT raw FROM session_chunks WHERE session_id=? AND seq=3`,
				`SELECT raw FROM session_chunks WHERE session_id=? AND seq=4`,
			} {
				if err := read.QueryRow(q, sessions[0]).Scan(&raw); err != nil || !strings.Contains(raw, redact.LiteralMarker) {
					t.Fatalf("%s: %s, %v", q, raw, err)
				}
			}
			// Titles: from the prompt (cut inside the value), and one that only had a title.
			var title string
			if err := read.QueryRow(`SELECT title FROM sessions WHERE id=?`, sessions[2]).Scan(&title); err != nil || strings.Contains(title, "QDzt") || strings.Contains(title, "fake") {
				t.Fatalf("title of the cut prompt: %q, %v", title, err)
			}
			if err := read.QueryRow(`SELECT title FROM sessions WHERE id=?`, sessions[3]).Scan(&title); err != nil || title != "Notizen zu [REDACTED:literal]" {
				t.Fatalf("title: %q, %v", title, err)
			}
			// Quotes: one stays a part of the new text, one is replaced as a whole,
			// the one without a chunk is cleaned too.
			quotes := dumpTable(t, read, `SELECT chunk_seq, quote FROM knowledge_evidence ORDER BY id`)
			for _, want := range []string{"0 sed 's/[REDACTED:literal]/x/'", "99 [REDACTED:literal]", "0 [REDACTED:literal] und [REDACTED:literal]"} {
				if !strings.Contains(quotes, want) {
					t.Errorf("quotes lack %q:\n%s", want, quotes)
				}
			}
			if sight := dumpTable(t, read, `SELECT quote FROM request_sightings`); strings.TrimSpace(sight) != redactQuoteMarker {
				t.Errorf("sighting: %q", sight)
			}
			if !strings.Contains(after, redactAssistantLine("Notiert.")) || !strings.Contains(after, redactAssistantLine("Zeile ohne Geheimnis")) {
				t.Fatal("a clean row changed")
			}

			// No search finds the values, in no index and not in the segments either.
			for _, table := range []string{"chunks_fts", "sess_fts"} {
				for _, word := range literalTokens {
					if n := ftsHits(t, read, table, word); n != 0 {
						t.Errorf("%s MATCH %q: %d hits", table, word, n)
					}
					if n := segmentHits(t, read, table, word); n != 0 {
						t.Errorf("%s segments still hold %q in %d blocks", table, word, n)
					}
				}
			}
			if ftsHits(t, read, "chunks_fts", "notiert") != 1 || ftsHits(t, read, "sess_fts", "durchgesickerte") != 1 {
				t.Error("the index lost entries that were not touched")
			}
			checkIntegrity(t, read)

			// The file and its journal have no trace either.
			read.Close()
			if found := fileHolds(t, path, fileNeedles...); len(found) > 0 {
				t.Errorf("the database file still holds %v", found)
			}
			read, err = sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer read.Close()

			// A second run has nothing left to do.
			again, err := RedactSessions(context.Background(), path, RedactSessionsOptions{Batch: 3, Literals: lits})
			if err != nil {
				t.Fatal(err)
			}
			if again.Changed != 0 || len(again.Tables) != 0 || len(again.Kinds) != 0 {
				t.Fatalf("second run: %+v", again)
			}
			if got := dumpCopies(t, read); got != after {
				t.Fatal("the second run changed rows")
			}
			checkIntegrity(t, read)
		})
	}
}

// Ohne die Liste bleibt alles, wie es ist: kein Wert ohne Beschriftung wird angefasst.
func TestRedactSessionsLiteralsAreOptIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seedLiterals(t, s)
	s.Close()
	rep, err := RedactSessions(context.Background(), path, RedactSessionsOptions{Batch: 3})
	if err != nil {
		t.Fatal(err)
	}
	// Das Passwort mit Beschriftung in Sitzung b ist das Einzige, was die Beschriftungsregel nimmt.
	if rep.Changed != 1 || rep.Kinds["text/labeled:password"] != 1 || rep.Kinds["text/literal"] != 0 || rep.Kinds["raw/literal"] != 0 || rep.Holders != nil {
		t.Fatalf("a run without the list: %+v", rep)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got := dumpCopies(t, db)
	for _, want := range []string{"sed 's/" + testPassword + "/x/'", strings.ToLower(testPassword), "7ktest und fakewrd", "Notizen zu fakeWRD", jsonEscaped(t, litEscValue)} {
		if !strings.Contains(got, want) {
			t.Errorf("a run without the list removed %q", want)
		}
	}
	if ftsHits(t, db, "chunks_fts", "fakewrd") == 0 || ftsHits(t, db, "sess_fts", "tool_output:fakewrd") == 0 {
		t.Error("a run without the list changed the indexes")
	}
}

// Wissen, seine Fassungen und Aufträge werden gezählt und nicht angefasst.
func TestRedactSessionsLiteralsCountTheTextsOfPeopleAndAgents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO knowledge(id,type,title,body,project,origin,created_at,updated_at) VALUES(7,'note','Eintrag','Rest: fakewrd steht noch da','p','agent','x','x')`,
		`INSERT INTO knowledge(id,type,title,body,project,origin,created_at,updated_at) VALUES(8,'note','Titel mit 7kTest','Text','p','agent','x','x')`,
		`INSERT INTO knowledge(id,type,title,body,project,origin,created_at,updated_at) VALUES(9,'note','Sauber','Text','p','agent','x','x')`,
		`INSERT INTO knowledge_versions(knowledge_id,type,title,body,changed_at) VALUES(7,'note','Eintrag','alt: ` + strings.ToLower(testPassword) + `','x')`,
		`INSERT INTO knowledge_versions(knowledge_id,type,title,body,changed_at) VALUES(7,'note','Eintrag','noch eine Fassung mit QDzt','x')`,
		`INSERT INTO requests(id,type,title,description,created_at,updated_at) VALUES(5,'change','Auftrag','Beschreibung mit ` + testPassword + `','x','x')`,
		`INSERT INTO requests(id,type,title,description,created_at,updated_at) VALUES(6,'change','Sauber','Text','x','x')`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	before := dumpTable(t, s.db, `SELECT id, title, body FROM knowledge ORDER BY id`) +
		dumpTable(t, s.db, `SELECT id, title, body FROM knowledge_versions ORDER BY id`) +
		dumpTable(t, s.db, `SELECT id, title, description FROM requests ORDER BY id`)
	s.Close()

	want := map[string]RedactHolders{
		"knowledge":          {Rows: 2, Owners: []int64{7, 8}},
		"knowledge_versions": {Rows: 2, Owners: []int64{7}},
		"requests":           {Rows: 1, Owners: []int64{5}},
	}
	for _, dryRun := range []bool{true, false} {
		rep, err := RedactSessions(context.Background(), path, RedactSessionsOptions{DryRun: dryRun, Literals: literalsFor(t)})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(rep.Holders, want) {
			t.Fatalf("dryRun=%v: holders = %+v, want %+v", dryRun, rep.Holders, want)
		}
		if rep.Changed != 0 || len(rep.Tables) != 0 {
			t.Fatalf("dryRun=%v: a run with only authored texts changed something: %+v", dryRun, rep)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if after := dumpTable(t, db, `SELECT id, title, body FROM knowledge ORDER BY id`) +
		dumpTable(t, db, `SELECT id, title, body FROM knowledge_versions ORDER BY id`) +
		dumpTable(t, db, `SELECT id, title, description FROM requests ORDER BY id`); after != before {
		t.Fatalf("authored texts were changed:\n%s", after)
	}
	// Nothing found: an empty answer, not a missing one.
	clean := filepath.Join(t.TempDir(), "clean.db")
	c, err := Open(clean)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	rep, err := RedactSessions(context.Background(), clean, RedactSessionsOptions{Literals: literalsFor(t)})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Holders == nil || len(rep.Holders) != 0 {
		t.Fatalf("holders = %#v, want an empty map", rep.Holders)
	}
}

// The report is made of numbers and names. No value may get into it.
func TestRedactSessionsLiteralsReportHoldsNoValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seedLiterals(t, s)
	s.Close()
	rep, err := RedactSessions(context.Background(), path, RedactSessionsOptions{Literals: literalsFor(t)})
	if err != nil {
		t.Fatal(err)
	}
	dump := strings.ToLower(fmt.Sprintf("%+v", rep))
	for _, form := range literalForms(t) {
		if strings.Contains(dump, strings.ToLower(form)) {
			t.Fatalf("the report holds %q: %s", form, dump)
		}
	}
}
