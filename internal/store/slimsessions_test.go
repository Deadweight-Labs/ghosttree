package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/slim"
)

type legacyChunk struct {
	seq        int
	role, text string
	raw        string
}

// seedUnslimmed legt Zeilen an, wie sie vor dem Schlanken gespeichert wurden:
// Klartext, slim_version 0, ungekürzt, mit Index und Volltext.
func seedUnslimmed(t *testing.T, s *Store, ext, harness string, chunks []legacyChunk) int64 {
	t.Helper()
	id, err := s.UpsertSession(Session{Harness: harness, ExternalID: ext, StartedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range chunks {
		if _, err := s.db.Exec(`INSERT INTO session_chunks(session_id, seq, role, text, raw) VALUES(?,?,?,?,?)`, id, c.seq, c.role, c.text, c.raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.IndexSession(id); err != nil {
		t.Fatal(err)
	}
	legacySearchLayout(t, s)
	return id
}

func slimChunkID(t *testing.T, db *sql.DB, session int64, seq int) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(`SELECT id FROM session_chunks WHERE session_id=? AND seq=?`, session, seq).Scan(&id); err != nil {
		t.Fatalf("chunk %d/%d: %v", session, seq, err)
	}
	return id
}

func segmentHas(t *testing.T, db *sql.DB, table, word string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM `+table+`_data WHERE instr(block, CAST(? AS BLOB)) > 0`, word).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestSlimSessionsBringsTheStockToTheFormOfNewLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	long := strings.Repeat("compiling package one two three\n", 300) // über slim.CapOver
	middle := strings.Repeat("a", 7000) + " quaggamiddle " + strings.Repeat("b", 9000)
	prompt := `{"type":"user","message":{"role":"user","content":"rename the flag"},"uuid":"u1","timestamp":"2026-11-01T10:00:00.000Z"}`
	thinking := `{"type":"assistant","message":{"role":"assistant","model":"claude-opus-5-5","content":[{"type":"thinking","thinking":"easy","signature":"c2lnbmF0dXJl"},{"type":"text","text":"renamed"}]},"uuid":"a1","timestamp":"2026-11-01T10:00:02.000Z"}`
	output := fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"t1","type":"tool_result","content":%q}]},"uuid":"r1","timestamp":"2026-11-01T10:00:03.000Z"}`, middle)
	blob := `{"type":"api-request-blob","message":{"role":"user","content":"zebraquux copy"},"timestamp":"2026-11-01T10:00:01.000Z"}`
	hook := `{"type":"attachment","attachment":{"type":"hook_success","content":"cited hook output"},"timestamp":"2026-11-01T10:00:04.000Z"}`
	a := seedUnslimmed(t, s, "a", "claude-code", []legacyChunk{
		{0, "user", "rename the flag", prompt},
		{1, "", "zebraquux copy", blob},
		{2, "assistant", "renamed", thinking},
		{3, "user", middle, output},
		{4, "", "", hook},
		{5, "user", long, `{"type":"user","message":{"role":"user","content":"short"},"timestamp":"2026-11-01T10:00:05.000Z"}`},
	})
	b := seedUnslimmed(t, s, "b", "claude-code", []legacyChunk{
		{0, "user", long, `{"type":"user","message":{"role":"user","content":"in a batch"},"timestamp":"2026-11-01T10:00:00.000Z"}`},
	})
	// Ein Beleg zitiert die Hook-Zeile, die sonst wegfiele.
	if _, err := s.db.Exec(`INSERT INTO knowledge(id,type,title,body,project,origin,created_at,updated_at) VALUES(7,'note','Eintrag','Text','p','distilled','x','x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO knowledge_evidence(knowledge_id, session_id, chunk_seq, quote) VALUES(7,?,4,'cited')`, a); err != nil {
		t.Fatal(err)
	}
	// Session b steckt in einem offenen Destillier-Auftrag.
	if _, err := s.db.Exec(`INSERT INTO distill_batches(id, provider_batch_id, state, created_at, updated_at) VALUES(1,'pb','open','x','x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO distill_batch_items(batch_id, custom_id, session_id, digest) VALUES(1,'c',?,'d')`, b); err != nil {
		t.Fatal(err)
	}
	// Ein Vektor an der Zeile, die wegfällt.
	blobID := slimChunkID(t, s.db, a, 1)
	if _, err := s.db.Exec(`INSERT INTO session_embeddings(chunk_id, model, prefix_version, dim, text_hash, scale, vec, read_at, updated_at) VALUES(?,'m','p',1,'h',1,x'00','x','x')`, blobID); err != nil {
		t.Fatal(err)
	}
	// Und einer an einer Zeile, die nur gepackt wird: er bleibt, die Zeile ist
	// entpackt dieselbe (#3404).
	promptID := slimChunkID(t, s.db, a, 0)
	if _, err := s.db.Exec(`INSERT INTO session_embeddings(chunk_id, model, prefix_version, dim, text_hash, scale, vec, read_at, updated_at) VALUES(?,'m','p',1,'h',1,x'00','x','x')`, promptID); err != nil {
		t.Fatal(err)
	}
	if ftsHits(t, s.db, "chunks_fts", "zebraquux") != 1 || ftsHits(t, s.db, "sess_fts", "quaggamiddle") != 1 {
		t.Fatal("seed not indexed")
	}
	before := dumpCopies(t, s.db)

	// Probelauf: nichts ändert sich, auch kein Wörterbuch.
	rep, err := SlimSessions(context.Background(), path, SlimSessionsOptions{DryRun: true, Pause: 1})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Deleted != 1 || rep.KeptCited != 1 || rep.Scanned != 7 || !rep.DictTrained {
		t.Fatalf("dry run %+v", rep)
	}
	if after := dumpCopies(t, s.db); after != before {
		t.Fatalf("dry run changed the database:\n%s\n%s", before, after)
	}
	if d, _ := listRawDicts(context.Background(), s.db); len(d) != 0 {
		t.Fatal("dry run stored a dictionary")
	}

	rep, err = SlimSessions(context.Background(), path, SlimSessionsOptions{Pause: 1})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Chunks != 7 || rep.Scanned != 7 || rep.Deleted != 1 || rep.KeptCited != 1 || rep.TextCut != 2 || rep.TextKeptOpenBatch != 1 ||
		rep.Slimmed < 2 || rep.Tables["session_embeddings"] != 1 || rep.RawAfter >= rep.RawBefore || len(rep.Optimized) != 3 {
		t.Fatalf("report %+v", rep)
	}

	// Die Kopie ist weg, mit Index, Volltext und Vektor; auch aus den Segmenten (#3273).
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM session_chunks WHERE id=?`, blobID).Scan(&n)
	if n != 0 {
		t.Fatal("the dropped line is still there")
	}
	for _, q := range []string{`SELECT count(*) FROM chunk_index WHERE chunk_id=?`, `SELECT count(*) FROM chunks_fts_docsize WHERE id=?`, `SELECT count(*) FROM session_embeddings WHERE chunk_id=?`} {
		if err := s.db.QueryRow(q, blobID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s: %d, %v", q, n, err)
		}
	}
	if ftsHits(t, s.db, "chunks_fts", "zebraquux") != 0 || segmentHas(t, s.db, "chunks_fts", "zebraquux") {
		t.Fatal("the words of the dropped line stay in chunks_fts")
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM session_embeddings WHERE chunk_id=?`, promptID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the vector of an unchanged line is gone: %d, %v", n, err)
	}
	// Die Mitte der langen Ausgabe ist aus raw, text und dem Index heraus.
	if ftsHits(t, s.db, "sess_fts", "quaggamiddle") != 0 || ftsHits(t, s.db, "chunks_fts", "quaggamiddle") != 0 {
		t.Fatal("the cut middle is still found")
	}
	// Jede Zeile liest sich als das, was der Server heute aus ihr machte.
	got, err := s.ReadSession(a, 0, 100)
	if err != nil || len(got) != 5 {
		t.Fatalf("%d lines, %v", len(got), err)
	}
	for _, c := range got {
		var want string
		switch c.Seq {
		case 0:
			want = prompt
		case 2:
			want, _ = slim.Line(thinking)
		case 3:
			want, _ = slim.Line(output)
		case 4:
			want = hook // zitiert: bleibt
		case 5:
			if len(c.Text) > slim.CapOver || !strings.Contains(c.Text, "[ghosttree: gekürzt") {
				t.Fatalf("text not cut: %d bytes", len(c.Text))
			}
			continue
		default:
			t.Fatalf("unexpected line %d", c.Seq)
		}
		if c.Raw != want {
			t.Fatalf("line %d:\n%s\nwant\n%s", c.Seq, c.Raw, want)
		}
	}
	if w, err := s.ReadSession(b, 0, 10); err != nil || w[0].Text != long {
		t.Fatal("text of a session in an open batch was cut")
	}
	var stale int
	if err := s.db.QueryRow(`SELECT count(*) FROM session_chunks WHERE slim_version <> ?`, slim.Version).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("%d lines not at the current version", stale)
	}
	checkFTSAgainstContent(t, s.db)
	var ok string
	if err := s.db.QueryRow(`PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
		t.Fatalf("integrity_check %q %v", ok, err)
	}

	// Ein zweiter Lauf findet nichts.
	again, err := SlimSessions(context.Background(), path, SlimSessionsOptions{Pause: 1})
	if err != nil || again.Scanned != 0 || again.Current != 6 || again.DictTrained {
		t.Fatalf("second run %+v, %v", again, err)
	}
}

// checkFTSAgainstContent: jedes Dokument der Volltextindizes hat seine Zeile.
func checkFTSAgainstContent(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`SELECT count(*) FROM chunks_fts_docsize WHERE id NOT IN (SELECT id FROM session_chunks)`,
		`SELECT (SELECT count(*) FROM session_chunks) - (SELECT count(*) FROM chunks_fts_docsize)`,
		`SELECT count(*) FROM sess_fts_docsize WHERE id NOT IN (SELECT chunk_id FROM chunk_index)`,
		`SELECT count(*) FROM chunk_index WHERE chunk_id NOT IN (SELECT id FROM session_chunks)`,
	} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s: %d, %v", q, n, err)
		}
	}
	for _, fts := range []string{"chunks_fts", "sess_fts"} {
		if _, err := db.Exec(`INSERT INTO ` + fts + `(` + fts + `) VALUES('integrity-check')`); err != nil {
			t.Fatalf("%s: %v", fts, err)
		}
	}
}

// Läuft der Server daneben und schreibt neue Zeilen, packt er sie schon; der
// Lauf überspringt sie, und keine Zeile geht verloren.
func TestSlimSessionsRunsBesideTheServer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := OpenRuntime(path, DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var lines []legacyChunk
	for i := 0; i < 300; i++ {
		for j, l := range realLines(i) {
			lines = append(lines, legacyChunk{seq: 3*i + j, raw: l, text: "x"})
		}
	}
	old := seedUnslimmed(t, s.direct(), "old", "claude-code", lines)
	done := make(chan error, 1)
	go func() {
		_, err := SlimSessions(context.Background(), path, SlimSessionsOptions{Batch: 50, Pause: 1})
		done <- err
	}()
	var live []string
	for i := 0; i < 40; i++ {
		live = append(live, realLines(1000+i)...)
	}
	fresh := appendLines(t, s, "claude-code", "live", live)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for id, n := range map[int64]int{old: len(lines), fresh: len(live)} {
		got, err := s.SessionRaw(id)
		if err != nil || len(got) != n {
			t.Fatalf("session %d: %d of %d lines, %v", id, len(got), n, err)
		}
	}
	var stale int
	if err := s.db.QueryRow(`SELECT count(*) FROM session_chunks WHERE slim_version <> ?`, slim.Version).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("%d lines not at the current version", stale)
	}
	var packed int
	if err := s.db.QueryRow(`SELECT count(*) FROM session_chunks WHERE typeof(raw)='blob'`).Scan(&packed); err != nil || packed < len(lines) {
		t.Fatalf("%d packed", packed)
	}
}

func TestSlimSessionsRefusesAnUnmigratedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE session_chunks(id INTEGER PRIMARY KEY, session_id INTEGER, seq INTEGER, role TEXT, text TEXT, raw TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := SlimSessions(context.Background(), path, SlimSessionsOptions{}); err == nil || !strings.Contains(err.Error(), "start the new ghosttree server") {
		t.Fatalf("err %v", err)
	}
}

// Die Wörterbuch-Stichprobe sieht geschlankte Zeilen, keine Kopien.
func TestDictionarySampleIsSlimmed(t *testing.T) {
	s := openTest(t)
	var lines []legacyChunk
	for i := 0; i < 50; i++ {
		lines = append(lines, legacyChunk{seq: 2 * i, raw: `{"type":"api-request-blob","message":{"role":"user","content":"copy copy copy copy copy copy"}}`},
			legacyChunk{seq: 2*i + 1, raw: realLines(i)[1]})
	}
	seedUnslimmed(t, s, "sample", "claude-code", lines)
	samples, _, err := sampleRawLines(context.Background(), s.db, s.rawState(), 1)
	if err != nil || len(samples) == 0 {
		t.Fatalf("%d samples, %v", len(samples), err)
	}
	for _, l := range samples {
		if strings.Contains(string(l), "api-request-blob") {
			t.Fatal("a dropped line is in the sample")
		}
	}

}

// A server that holds the write lock longer than busy_timeout does not end the
// run: the step waits and is tried again, and its counts are taken only once.
func TestSlimSessionsWaitsOutALongWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var lines []legacyChunk
	for i := 0; i < 40; i++ {
		for j, l := range realLines(i) {
			lines = append(lines, legacyChunk{seq: 3*i + j, raw: l, text: "x"})
		}
	}
	seedUnslimmed(t, s, "old", "claude-code", lines[:30])
	s.Close()
	// A first run learns the dictionary, as on a server that already runs the
	// version: the only writes of the second run are its steps.
	if _, err := SlimSessions(context.Background(), path, SlimSessionsOptions{Pause: 1}); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	seedUnslimmed(t, s, "later", "claude-code", lines[30:])
	s.Close()
	lines = lines[30:]

	oldDSN, oldBackoff := slimDSN, slimBusyBackoff
	slimDSN = func(p string) string { return p + "?_pragma=busy_timeout(50)" }
	slimBusyBackoff = []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 2 * time.Second}
	defer func() { slimDSN, slimBusyBackoff = oldDSN, oldBackoff }()

	holder, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	conn, err := holder.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(2 * time.Second)
		_, _ = conn.ExecContext(context.Background(), `COMMIT`)
		_ = conn.Close()
		close(released)
	}()
	started := time.Now()
	rep, err := SlimSessions(context.Background(), path, SlimSessionsOptions{Batch: 1000, Pause: 1})
	<-released
	if err != nil {
		t.Fatalf("run ended at the lock: %v", err)
	}
	if time.Since(started) < 2*time.Second {
		t.Fatalf("run finished in %v while the lock was held for 2s: it never met the lock", time.Since(started))
	}
	if rep.Packed+rep.Deleted == 0 || rep.Packed+rep.Deleted > int64(len(lines)) {
		t.Fatalf("counts after a retried step: packed %d deleted %d of %d lines", rep.Packed, rep.Deleted, len(lines))
	}
}
