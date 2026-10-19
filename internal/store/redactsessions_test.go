package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// Alle Werte in diesen Tests sind erfunden.
const (
	testPassword = "QDzt#fakeWRD%7kTest!"
	testToken    = "5n5n5n5n7p7p7p7p9q9q"
	// Teile des Passworts, wie der Volltextindex sie als Wörter führt.
	pwWord1, pwWord2 = "fakewrd", "7ktest"
)

func redactUserLine(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "timestamp": "2026-10-19T10:00:00Z", "message": map[string]any{"role": "user", "content": text}})
	return string(b)
}

func redactToolLine(command string) string {
	b, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": "2026-10-19T10:00:01Z", "message": map[string]any{"role": "assistant",
		"content": []any{map[string]any{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{"command": command}}}}})
	return string(b)
}

func redactAssistantLine(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": "2026-10-19T10:00:02Z", "message": map[string]any{"role": "assistant",
		"content": []any{map[string]any{"type": "text", "text": text}}}})
	return string(b)
}

func TestAppendChunksRedactsTextAndRawOnArrival(t *testing.T) {
	prompt := "Merk dir das Passwort " + testPassword + " für das Mailkonto bitte"
	chunks := func() []Chunk {
		return []Chunk{
			// ein alter Client schickt ungeschwärzt
			{Seq: 0, Role: "user", Text: prompt, Raw: redactUserLine(prompt)},
			{Seq: 1, Role: "assistant", Text: "Notiert.", Raw: redactAssistantLine("Notiert.")},
			{Seq: 2, Role: "tool", Text: "", Raw: redactToolLine("mysql --password=Sommer2026! -u app")},
			{Seq: 3, Role: "user", Text: "export API_KEY=k7k7k7k7m2m2m2m2\nnext", Raw: redactUserLine("export API_KEY=k7k7k7k7m2m2m2m2\nnext")},
		}
	}
	for name, open := range map[string]func(t *testing.T) *Store{
		"direct": openTest,
		"writer": func(t *testing.T) *Store {
			s, err := Open(filepath.Join(t.TempDir(), "ghosttree.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			return s
		},
	} {
		for _, batched := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s batched=%v", name, batched), func(t *testing.T) {
				s := open(t)
				id, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "redact-" + name})
				if err != nil {
					t.Fatal(err)
				}
				in := chunks()
				if batched {
					err = s.AppendChunkBatches([]ChunkBatch{{SessionID: id, Chunks: in}})
				} else {
					err = s.AppendChunks(id, in)
				}
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(in, chunks()) {
					t.Fatal("the caller's chunks were changed")
				}
				got, err := s.ReadSession(id, 0, 10)
				if err != nil || len(got) != 4 {
					t.Fatalf("chunks %d, %v", len(got), err)
				}
				for _, c := range got {
					for _, secret := range []string{testPassword, "fakeWRD", "Sommer2026", "k7k7k7k7m2m2m2m2"} {
						if strings.Contains(c.Text, secret) || strings.Contains(c.Raw, secret) {
							t.Fatalf("chunk %d still holds %q: text %q raw %.200s", c.Seq, secret, c.Text, c.Raw)
						}
					}
					if !json.Valid([]byte(c.Raw)) {
						t.Fatalf("chunk %d raw is not JSON: %s", c.Seq, c.Raw)
					}
				}
				if want := "Merk dir das Passwort [REDACTED:labeled] für das Mailkonto bitte"; got[0].Text != want {
					t.Errorf("text = %q, want %q", got[0].Text, want)
				}
				if !strings.Contains(got[0].Raw, "Passwort [REDACTED:labeled] für") || !strings.Contains(got[2].Raw, "--password=[REDACTED:labeled] -u app") {
					t.Errorf("raw not redacted in place:\n%s\n%s", got[0].Raw, got[2].Raw)
				}
				if got[1].Raw != redactAssistantLine("Notiert.") {
					t.Errorf("a clean line changed: %s", got[1].Raw)
				}
				// Kein Volltextindex kennt die Wörter.
				for _, q := range []string{
					`SELECT count(*) FROM chunks_fts WHERE chunks_fts MATCH '` + pwWord1 + `'`,
					`SELECT count(*) FROM sess_fts WHERE sess_fts MATCH '` + pwWord2 + `'`,
					`SELECT count(*) FROM sess_fts WHERE sess_fts MATCH 'sommer2026'`,
				} {
					var n int
					if err := s.db.QueryRow(q).Scan(&n); err != nil || n != 0 {
						t.Errorf("%s = %d, %v", q, n, err)
					}
				}
				// Der Titel stammt aus dem Prompt und trägt das Passwort nicht.
				sess, err := s.SessionByID(id)
				if err != nil || strings.Contains(sess.Title, "fakeWRD") || !strings.Contains(sess.Title, "[REDACTED:labeled]") {
					t.Errorf("title %q, %v", sess.Title, err)
				}
			})
		}
	}
}

// seedLegacy legt Zeilen so an, wie sie vor dieser Änderung gespeichert wurden:
// unter Umgehung von AppendChunks, mit Passwort in text, raw, dem Volltextindex,
// dem Suchindex der Session, dem Titel und den Zitaten der Belege.
func seedLegacy(t *testing.T, s *Store) (sessions [2]int64) {
	t.Helper()
	a, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "legacy-a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "legacy-b"})
	if err != nil {
		t.Fatal(err)
	}
	prompt := "Merk dir das Passwort " + testPassword + " für das Mailkonto bitte"
	rows := []struct {
		session   int64
		seq       int
		text, raw string
	}{
		{a, 0, prompt, redactUserLine(prompt)},
		{a, 1, "Notiert.", redactAssistantLine("Notiert.")},
		{a, 2, "", redactToolLine("mysql --password=Sommer2026! -u app")},
		{a, 3, "weiter", redactUserLine("weiter")},
		{b, 0, "token: " + testToken + " und mehr", redactUserLine("token: " + testToken + " und mehr")},
		{b, 1, "Zeile ohne Geheimnis", redactAssistantLine("Zeile ohne Geheimnis")},
	}
	for _, r := range rows {
		if _, err := s.db.Exec(`INSERT INTO session_chunks(session_id, seq, role, text, raw) VALUES(?,?,?,?,?)`, r.session, r.seq, "", r.text, r.raw); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int64{a, b} {
		if err := s.IndexSession(id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int{7, 8} {
		if _, err := s.db.Exec(`INSERT INTO knowledge(id,type,title,body,project,origin,created_at,updated_at) VALUES(?,'note','Eintrag','Text','p','distilled','x','x')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO requests(id,type,title,description,created_at,updated_at) VALUES(5,'change','Auftrag','Text','x','x')`); err != nil {
		t.Fatal(err)
	}
	// Belege: ein Zitat mit Beschriftung, eines, das den Wert aus seiner Beschriftung schneidet.
	if err := s.AddEvidence(7, []Evidence{
		{SessionID: a, ChunkSeq: 0, Quote: "das Passwort " + testPassword + " für"},
		{SessionID: a, ChunkSeq: 3, Quote: "weiter"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddEvidence(8, []Evidence{{SessionID: a, ChunkSeq: 0, Quote: testPassword + " für das"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO request_sightings(request_id,session_id,chunk_seq,quote) VALUES(5,?,0,?)`, a, "Passwort "+testPassword); err != nil {
		t.Fatal(err)
	}
	return [2]int64{a, b}
}

func dumpTable(t *testing.T, db *sql.DB, query string) string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var sb strings.Builder
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(&sb, vals...)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

// dumpCopies gives every table that holds session text, in a form that can be compared.
func dumpCopies(t *testing.T, db *sql.DB) string {
	t.Helper()
	var sb strings.Builder
	for _, q := range []string{
		`SELECT id, session_id, seq, role, text, raw FROM session_chunks ORDER BY id`,
		`SELECT chunk_id, text, thinking, tool_input, tool_output FROM chunk_index ORDER BY chunk_id`,
		`SELECT id, title, msg_count FROM sessions ORDER BY id`,
		`SELECT id, knowledge_id, chunk_seq, quote FROM knowledge_evidence ORDER BY id`,
		`SELECT id, request_id, chunk_seq, quote FROM request_sightings ORDER BY id`,
		`SELECT id, sz FROM chunks_fts_docsize ORDER BY id`,
	} {
		sb.WriteString(dumpTable(t, db, q))
		sb.WriteString("--\n")
	}
	return sb.String()
}

func ftsHits(t *testing.T, db *sql.DB, table, word string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM `+table+` WHERE `+table+` MATCH ?`, word).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// segmentHits counts the segment blocks of an index in which the word still stands.
func segmentHits(t *testing.T, db *sql.DB, table, word string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM `+table+`_data WHERE instr(block, CAST(? AS BLOB)) > 0`, word).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func checkIntegrity(t *testing.T, db *sql.DB) {
	t.Helper()
	var ok string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
		t.Fatalf("integrity_check: %q, %v", ok, err)
	}
	for _, fts := range []string{"chunks_fts", "sess_fts"} {
		// rank=1 compares the index with the content table
		if _, err := db.Exec(`INSERT INTO ` + fts + `(` + fts + `, rank) VALUES('integrity-check', 1)`); err != nil {
			t.Fatalf("%s integrity-check: %v", fts, err)
		}
	}
}

func fileHolds(t *testing.T, path string, needles ...string) []string {
	t.Helper()
	var found []string
	for _, p := range []string{path, path + "-wal"} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, n := range needles {
			if bytes.Contains(b, []byte(n)) {
				found = append(found, filepath.Base(p)+":"+n)
			}
		}
	}
	return found
}

func TestRedactSessionsCleansEveryCopy(t *testing.T) {
	// Schritte nach Zeilen, nach Bytes (jede Zeile allein) und die Voreinstellung
	// kommen zum selben Ergebnis.
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
			sessions := seedLegacy(t, s)

			// Vorher steht alles da, und die Prüfungen sehen es.
			if ftsHits(t, s.db, "chunks_fts", pwWord1) != 1 || ftsHits(t, s.db, "sess_fts", pwWord2) != 1 {
				t.Fatal("the password is not in the indexes before the run: the test checks nothing")
			}
			if segmentHits(t, s.db, "chunks_fts", pwWord1)+segmentHits(t, s.db, "chunks_fts", "qdzt") == 0 {
				t.Fatal("the segment probe finds nothing before the run")
			}
			checkIntegrity(t, s.db)
			before := dumpCopies(t, s.db)
			for _, want := range []string{testPassword, "Sommer2026", testToken} {
				if !strings.Contains(before, want) {
					t.Fatalf("seed lacks %q", want)
				}
			}
			// The file starts without dead space from the seeding: what the run
			// frees is what must be overwritten.
			if _, err := s.db.Exec(`VACUUM`); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if len(fileHolds(t, path, "fakeWRD", "fakewrd", "7ktest", "Sommer2026", "sommer2026", testToken)) == 0 {
				t.Fatal("the file does not hold the secrets before the run: the file check proves nothing")
			}

			read, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer read.Close()

			// --dry-run: zählt, ändert nichts.
			dry := opts
			dry.DryRun = true
			dryRep, err := RedactSessions(context.Background(), path, dry)
			if err != nil {
				t.Fatal(err)
			}
			if got := dumpCopies(t, read); got != before {
				t.Fatalf("a dry run changed the database:\n%s", got)
			}
			if dryRep.Chunks != 6 || dryRep.Scanned != 6 || dryRep.Changed != 3 {
				t.Fatalf("dry run: %+v", dryRep)
			}

			steps := 0
			opts.Progress = func(RedactSessionsReport) { steps++ }
			rep, err := RedactSessions(context.Background(), path, opts)
			if err != nil {
				t.Fatal(err)
			}
			if name != "default" && steps < 2 {
				t.Fatalf("only %d steps: not walked in small batches", steps)
			}
			// Der Probelauf zählt dasselbe wie der echte.
			if !reflect.DeepEqual(rep.Tables, dryRep.Tables) || !reflect.DeepEqual(rep.Kinds, dryRep.Kinds) || rep.Changed != dryRep.Changed {
				t.Fatalf("dry run %+v differs from the real run %+v", dryRep, rep)
			}
			wantTables := map[string]int64{
				"session_chunks.text": 2, "session_chunks.raw": 3, "chunks_fts": 2,
				"chunk_index": 3, "sess_fts": 3, "sessions.title": 2,
				"knowledge_evidence.quote": 2, "request_sightings.quote": 1,
			}
			if !reflect.DeepEqual(rep.Tables, wantTables) {
				t.Fatalf("tables = %v, want %v", rep.Tables, wantTables)
			}
			if rep.Kinds["raw/labeled:password"] != 2 || rep.Kinds["raw/labeled:token"] != 1 || rep.Kinds["text/labeled:password"] != 1 || rep.Kinds["text/labeled:token"] != 1 {
				t.Fatalf("kinds = %v", rep.Kinds)
			}
			if !reflect.DeepEqual(rep.CitedKnowledge, []int64{7, 8}) || !reflect.DeepEqual(rep.CitedRequests, []int64{5}) {
				t.Fatalf("cited = %v %v", rep.CitedKnowledge, rep.CitedRequests)
			}
			if !reflect.DeepEqual(rep.Optimized, []string{"chunks_fts", "sess_fts"}) || rep.Checkpoint == "" {
				t.Fatalf("optimized %v, checkpoint %q", rep.Optimized, rep.Checkpoint)
			}

			after := dumpCopies(t, read)
			for _, secret := range []string{testPassword, "fakeWRD", "Sommer2026", testToken} {
				if strings.Contains(after, secret) {
					t.Fatalf("a table still holds %q:\n%s", secret, after)
				}
			}
			// text, raw, Titel und Zitate sind geschwärzt, nicht geleert.
			var text, raw, title string
			if err := read.QueryRow(`SELECT text, raw FROM session_chunks WHERE session_id=? AND seq=0`, sessions[0]).Scan(&text, &raw); err != nil {
				t.Fatal(err)
			}
			if text != "Merk dir das Passwort [REDACTED:labeled] für das Mailkonto bitte" || !json.Valid([]byte(raw)) || raw != redactUserLine(text) {
				t.Fatalf("text %q raw %s", text, raw)
			}
			if err := read.QueryRow(`SELECT title FROM sessions WHERE id=?`, sessions[0]).Scan(&title); err != nil || title != text {
				t.Fatalf("title %q, %v", title, err)
			}
			quotes := dumpTable(t, read, `SELECT quote FROM knowledge_evidence ORDER BY id`)
			if !strings.Contains(quotes, "das Passwort [REDACTED:labeled] für") || !strings.Contains(quotes, redactQuoteMarker) || !strings.Contains(quotes, "weiter") {
				t.Fatalf("quotes: %s", quotes)
			}
			if sight := dumpTable(t, read, `SELECT quote FROM request_sightings`); strings.TrimSpace(sight) != "Passwort [REDACTED:labeled]" {
				t.Fatalf("sighting: %q", sight)
			}
			// Untouched rows stay byte for byte.
			if !strings.Contains(after, redactAssistantLine("Notiert.")) || !strings.Contains(after, redactAssistantLine("Zeile ohne Geheimnis")) {
				t.Fatal("a clean row changed")
			}

			// Keine Suche findet die Werte mehr, in keinem Index, auch nicht in den Segmenten.
			for _, probe := range []struct{ table, word string }{
				{"chunks_fts", pwWord1}, {"chunks_fts", pwWord2}, {"chunks_fts", "qdzt"}, {"chunks_fts", "sommer2026"}, {"chunks_fts", testToken},
				{"sess_fts", pwWord1}, {"sess_fts", pwWord2}, {"sess_fts", "sommer2026"}, {"sess_fts", testToken},
			} {
				if n := ftsHits(t, read, probe.table, probe.word); n != 0 {
					t.Errorf("%s MATCH %q: %d hits", probe.table, probe.word, n)
				}
			}
			for _, table := range []string{"chunks_fts", "sess_fts"} {
				for _, word := range []string{pwWord1, "qdzt", "sommer", testToken} {
					if n := segmentHits(t, read, table, word); n != 0 {
						t.Errorf("%s segments still hold %q in %d blocks", table, word, n)
					}
				}
			}
			// Was nicht geschwärzt wurde, bleibt auffindbar.
			if ftsHits(t, read, "chunks_fts", "notiert") != 1 || ftsHits(t, read, "chunks_fts", "mailkonto") != 1 || ftsHits(t, read, "sess_fts", "mailkonto") != 1 {
				t.Error("the index lost entries that were not touched")
			}
			checkIntegrity(t, read)

			// Auch die Datei selbst (und ihr Journal) hat die Werte nicht mehr.
			read.Close()
			if found := fileHolds(t, path, "fakeWRD", "fakewrd", "7ktest", "Sommer2026", "sommer2026", testToken); len(found) > 0 {
				t.Errorf("the database file still holds %v", found)
			}
			read, err = sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer read.Close()

			// Zweiter Lauf: nichts mehr zu tun, nichts ändert sich.
			again, err := RedactSessions(context.Background(), path, RedactSessionsOptions{Batch: 3})
			if err != nil {
				t.Fatal(err)
			}
			if again.Changed != 0 || len(again.Tables) != 0 || len(again.Kinds) != 0 || len(again.CitedKnowledge) != 0 || len(again.CitedRequests) != 0 {
				t.Fatalf("second run: %+v", again)
			}
			if got := dumpCopies(t, read); got != after {
				t.Fatal("the second run changed rows")
			}
			checkIntegrity(t, read)
		})
	}
}

func TestRedactSessionsStopsOnCancelAndContinues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seedLegacy(t, s)
	s.Close()

	ctx, cancel := context.WithCancel(context.Background())
	rep, err := RedactSessions(ctx, path, RedactSessionsOptions{Batch: 1, Progress: func(r RedactSessionsReport) {
		if r.Scanned >= 2 {
			cancel()
		}
	}})
	if err != context.Canceled {
		t.Fatalf("err = %v", err)
	}
	if rep.Scanned < 2 || rep.Scanned >= 6 || rep.LastID == 0 {
		t.Fatalf("report after the stop: %+v", rep)
	}
	// The run can go on where it stopped, and from the start: the result is the same.
	if _, err := RedactSessions(context.Background(), path, RedactSessionsOptions{AfterID: rep.LastID}); err != nil {
		t.Fatal(err)
	}
	final, err := RedactSessions(context.Background(), path, RedactSessionsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if final.Changed != 0 {
		t.Fatalf("a run after the continued one still changed %d rows", final.Changed)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if after := dumpCopies(t, db); strings.Contains(after, "fakeWRD") || strings.Contains(after, testToken) {
		t.Fatalf("secrets left:\n%s", after)
	}
	checkIntegrity(t, db)
}

func TestRedactSessionsRunsBesideALiveWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "live"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 120; i++ {
		text := fmt.Sprintf("note %d Passwort: Hx7!kQ2p%d", i, i)
		if _, err := s.db.Exec(`INSERT INTO session_chunks(session_id, seq, role, text, raw) VALUES(?,?,?,?,?)`, id, i, "", text, redactUserLine(text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.IndexSession(id); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	var appendErr error
	appended := 0
	running := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for seq := 1000; ; seq++ {
			select {
			case <-stop:
				return
			default:
			}
			// An old client sends it unredacted; the server redacts on arrival.
			text := fmt.Sprintf("live %d pw=Sommer2026!", seq)
			if err := s.AppendChunks(id, []Chunk{{Seq: seq, Text: text, Raw: redactUserLine(text)}}); err != nil {
				appendErr = err
				if appended == 0 {
					close(running)
				}
				return
			}
			if appended++; appended == 1 {
				close(running)
			}
		}
	}()
	<-running
	rep, err := RedactSessions(context.Background(), path, RedactSessionsOptions{Batch: 10})
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if appendErr != nil {
		t.Fatalf("the live writer failed: %v", appendErr)
	}
	if appended == 0 || rep.Changed != 120 {
		t.Fatalf("appended %d, changed %d: no overlap", appended, rep.Changed)
	}
	for _, q := range []string{`SELECT text FROM session_chunks`, `SELECT raw FROM session_chunks`, `SELECT text FROM chunk_index`} {
		if dump := dumpTable(t, s.db, q); strings.Contains(dump, "Hx7!kQ2p") || strings.Contains(dump, "Sommer2026") {
			t.Fatalf("%s still holds a secret", q)
		}
	}
	checkIntegrity(t, s.db)
}

func TestRedactSessionsOnADatabaseWithoutTheNewerIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE sessions(id INTEGER PRIMARY KEY, harness TEXT NOT NULL, title TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE session_chunks(id INTEGER PRIMARY KEY, session_id INTEGER NOT NULL, seq INTEGER NOT NULL, role TEXT NOT NULL DEFAULT '', text TEXT NOT NULL DEFAULT '', raw TEXT NOT NULL)`,
		`CREATE VIRTUAL TABLE chunks_fts USING fts5(text, content='session_chunks', content_rowid='id')`,
		`CREATE TRIGGER chunks_ai AFTER INSERT ON session_chunks BEGIN INSERT INTO chunks_fts(rowid, text) VALUES (new.id, new.text); END`,
		`INSERT INTO sessions(id, harness, title) VALUES(1, 'claude-code', 'Passwort: Hx7!kQ2pz')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	text := "Passwort: Hx7!kQ2pz"
	if _, err := db.Exec(`INSERT INTO session_chunks(session_id, seq, text, raw) VALUES(1, 0, ?, ?)`, text, redactUserLine(text)); err != nil {
		t.Fatal(err)
	}
	db.Close()

	rep, err := RedactSessions(context.Background(), path, RedactSessionsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Changed != 1 || rep.Tables["session_chunks.text"] != 1 || rep.Tables["chunks_fts"] != 1 || rep.Tables["sessions.title"] != 1 {
		t.Fatalf("report: %+v", rep)
	}
	if !reflect.DeepEqual(rep.Skipped, []string{"chunk_index", "sess_fts", "knowledge_evidence", "request_sightings"}) || !reflect.DeepEqual(rep.Optimized, []string{"chunks_fts"}) {
		t.Fatalf("skipped %v, optimized %v", rep.Skipped, rep.Optimized)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if n := ftsHits(t, db, "chunks_fts", "hx7"); n != 0 {
		t.Fatalf("%d hits", n)
	}
}

func TestRedactSessionsRefusesWhatIsNotAGhosttreeDatabase(t *testing.T) {
	dir := t.TempDir()
	if _, err := RedactSessions(context.Background(), filepath.Join(dir, "typo.db"), RedactSessionsOptions{}); err == nil {
		t.Fatal("a missing file was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "typo.db")); err == nil {
		t.Fatal("the run created a database")
	}
	if _, err := RedactSessions(context.Background(), dir, RedactSessionsOptions{}); err == nil {
		t.Fatal("a directory was accepted")
	}
	other := filepath.Join(dir, "other.db")
	db, _ := sql.Open("sqlite", other)
	if _, err := db.Exec(`CREATE TABLE x(a)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := RedactSessions(context.Background(), other, RedactSessionsOptions{}); err == nil || !strings.Contains(err.Error(), "not a ghosttree database") {
		t.Fatalf("err = %v", err)
	}
}

// The report is made of numbers and names. No value may get into it.
func TestRedactSessionsReportHoldsNoValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seedLegacy(t, s)
	s.Close()
	rep, err := RedactSessions(context.Background(), path, RedactSessionsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dump := fmt.Sprintf("%+v", rep)
	for _, secret := range []string{testPassword, "fakeWRD", "Sommer2026", testToken, "Hx7"} {
		if strings.Contains(dump, secret) {
			t.Fatalf("the report holds %q: %s", secret, dump)
		}
	}
}
