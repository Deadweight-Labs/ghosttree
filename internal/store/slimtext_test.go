package store

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/chunktext"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// textDB is a file with the fixtures of chunktext_test.go in two sessions, plus
// one row whose text differs from the derived one for no known reason.
func textDB(t *testing.T) (path string, sessions []int64) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sessions = []int64{storeFixture(t, s, "claude", claudeFixture()), storeFixture(t, s, "codex", codexFixture())}
	if _, err := s.db.Exec(`UPDATE session_chunks SET text = 'edited by hand' WHERE session_id = ? AND seq = 1`, sessions[0]); err != nil {
		t.Fatal(err)
	}
	return path, sessions
}

func slimText(t *testing.T, path string, opts SlimTextOptions) SlimTextReport {
	t.Helper()
	if opts.Pause == 0 {
		opts.Pause = time.Millisecond
	}
	rep, err := SlimText(context.Background(), path, opts)
	if err != nil {
		t.Fatalf("slim-text %+v: %v", opts, err)
	}
	return rep
}

func rowsWithText(t *testing.T, path string) map[int64]string {
	t.Helper()
	db := openRaw(t, path)
	rows, err := db.Query(`SELECT id, text FROM session_chunks`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var text string
		if err := rows.Scan(&id, &text); err != nil {
			t.Fatal(err)
		}
		out[id] = text
	}
	return out
}

func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func hasAnyText(t *testing.T, path string) bool {
	t.Helper()
	for _, text := range rowsWithText(t, path) {
		if text != "" {
			return true
		}
	}
	return false
}

// The dry run names the classes of the measurement and writes nothing.
func TestSlimTextDryRunCountsByClass(t *testing.T) {
	path, _ := textDB(t)
	before := rowsWithText(t, path)
	withText := 0
	for _, text := range before {
		if text != "" {
			withText++
		}
	}
	rep := slimText(t, path, SlimTextOptions{DryRun: true})
	if rep.WithText != int64(withText) || rep.Same+rep.Cut+rep.Other+rep.Underivable != rep.WithText {
		t.Fatalf("report %+v, rows with text %d", rep, withText)
	}
	// Two long blocks cut in raw (the claude fixture, seq 4), one text edited by
	// hand, everything else equal.
	if rep.Cut != 1 || rep.Other != 1 || rep.Same != rep.WithText-2 {
		t.Fatalf("classes: same %d, cut %d, other %d of %d", rep.Same, rep.Cut, rep.Other, rep.WithText)
	}
	if rep.Emptied != 0 || rep.Batches != 0 {
		t.Fatalf("a dry run wrote: %+v", rep)
	}
	after := rowsWithText(t, path)
	for id, text := range before {
		if after[id] != text {
			t.Fatalf("chunk %d changed by a dry run", id)
		}
	}
}

// A second run finds nothing, and an interrupted run goes on where it stopped.
func TestSlimTextIsRepeatableAndResumable(t *testing.T) {
	path, sessions := textDB(t)
	before := rowsWithText(t, path)
	withText := int64(0)
	for _, text := range before {
		if text != "" {
			withText++
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	steps := 0
	rep, err := SlimText(ctx, path, SlimTextOptions{Batch: 3, Pause: time.Millisecond, Progress: func(SlimTextReport) {
		if steps++; steps == 2 {
			cancel()
		}
	}})
	if err == nil || ctx.Err() == nil {
		t.Fatalf("the run was not interrupted: %v", err)
	}
	if rep.LastID == 0 || rep.Emptied == 0 || rep.Emptied >= withText {
		t.Fatalf("interrupted report: %+v", rep)
	}
	rest := slimText(t, path, SlimTextOptions{Batch: 3})
	if rep.Emptied+rest.Emptied != withText {
		t.Fatalf("emptied %d + %d, rows with text %d", rep.Emptied, rest.Emptied, withText)
	}
	again := slimText(t, path, SlimTextOptions{Batch: 3})
	if again.Emptied != 0 || again.Chars != 0 {
		t.Fatalf("a second run did work: %+v", again)
	}
	for id, text := range rowsWithText(t, path) {
		if text != "" {
			t.Fatalf("chunk %d still has text %q", id, text)
		}
	}

	// The sum ctx cost reads is the sum before the run.
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var chars int64
	if err := s.db.QueryRow(`SELECT sum(text_chars) FROM session_chunks`).Scan(&chars); err != nil {
		t.Fatal(err)
	}
	want := int64(0)
	for _, text := range before {
		want += int64(len([]rune(text)))
	}
	if chars != want || rest.Chars+rep.Chars != want {
		t.Fatalf("text_chars %d, reports %d, characters before %d", chars, rep.Chars+rest.Chars, want)
	}
	if _, err := s.ReadSession(sessions[0], 0, 100); err != nil {
		t.Fatal(err)
	}
}

// The command refuses where a quote could fail or the old index would be left
// standing on the column.
func TestSlimTextRefusesWhenItIsNotSafe(t *testing.T) {
	path, sessions := textDB(t)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordDistillBatch("batch_1", "m", []DistillBatchItem{{CustomID: "s1", SessionID: sessions[0], Digest: "d1"}}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	_, err = SlimText(context.Background(), path, SlimTextOptions{})
	if err == nil || !strings.Contains(err.Error(), "distillation batch") {
		t.Fatalf("an open batch did not stop the run: %v", err)
	}
	if rep := slimText(t, path, SlimTextOptions{DryRun: true}); len(rep.Blocked) != 1 {
		t.Fatalf("the dry run should name what blocks: %+v", rep.Blocked)
	}
	if !hasAnyText(t, path) {
		t.Fatal("a refused run wrote")
	}
	if rep := slimText(t, path, SlimTextOptions{Force: true}); rep.Emptied == 0 {
		t.Fatalf("--force did not run: %+v", rep)
	}

	// The trigger of the old index.
	path, _ = textDB(t)
	db := openRaw(t, path)
	if _, err := db.Exec(`CREATE TRIGGER chunks_ai AFTER INSERT ON session_chunks BEGIN
		INSERT INTO chunks_fts(rowid, text) VALUES (new.id, new.text); END`); err != nil {
		t.Fatal(err)
	}
	_, err = SlimText(context.Background(), path, SlimTextOptions{})
	if err == nil || !strings.Contains(err.Error(), "slim-search") {
		t.Fatalf("chunks_ai did not stop the run: %v", err)
	}
}

// --restore brings back the text of every row whose derivation is equal, byte
// for byte, and the derived text of the others.
func TestSlimTextRestoreGivesTheTextBack(t *testing.T) {
	path, _ := textDB(t)
	before := rowsWithText(t, path)
	slimText(t, path, SlimTextOptions{})

	if rep := slimText(t, path, SlimTextOptions{Restore: true, DryRun: true}); rep.Restored == 0 {
		t.Fatalf("restore dry run: %+v", rep)
	}
	for _, text := range rowsWithText(t, path) {
		if text != "" {
			t.Fatal("a restore dry run wrote")
		}
	}
	rep := slimText(t, path, SlimTextOptions{Restore: true, Batch: 4})
	if rep.Restored == 0 {
		t.Fatalf("nothing restored: %+v", rep)
	}
	after := rowsWithText(t, path)
	differing := 0
	for id, text := range before {
		if after[id] == text {
			continue
		}
		differing++
		if text == "" {
			t.Errorf("chunk %d had no text and has one now", id)
		}
	}
	// The edited row and the cut one: their derived text comes back.
	if differing != 2 {
		t.Fatalf("%d rows differ from the original text, want the two of the classes cut and other", differing)
	}
	if again := slimText(t, path, SlimTextOptions{Restore: true}); again.Restored != 0 {
		t.Fatalf("a second restore did work: %+v", again)
	}
	// And reading is the same as it was, for the rows that matched.
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for id := range before {
		var stored string
		if err := s.db.QueryRow(`SELECT text FROM session_chunks WHERE id = ?`, id).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored != after[id] {
			t.Fatalf("chunk %d", id)
		}
	}
}

// Chunks that the server writes while the command runs are neither lost nor
// read wrongly.
func TestSlimTextBesideAWritingServer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	cfg := DefaultWriterConfig()
	srv, err := OpenRuntime(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	f := claudeFixture()
	id, err := srv.UpsertSession(Session{Harness: f.harness, ExternalID: "live", Scope: scope.Axes{Project: "p"}, StartedAt: "2026-11-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	const total = 120
	appendSeq := func(seq int) error {
		// Lines that stay whole: every fourth fixture line is the long one, skip it.
		line := f.lines[seq%4]
		return srv.AppendChunks(id, []Chunk{collectorChunk(f.harness, seq, line)})
	}
	for seq := 0; seq < 40; seq++ {
		if err := appendSeq(seq); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	wg.Add(1)
	errs := make(chan error, 1)
	go func() {
		defer wg.Done()
		for seq := 40; seq < total; seq++ {
			if err := appendSeq(seq); err != nil {
				errs <- err
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	rep, err := SlimText(context.Background(), path, SlimTextOptions{Batch: 5, Pause: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
	if rep.Emptied == 0 {
		t.Fatalf("nothing emptied: %+v", rep)
	}
	chunks, err := srv.ReadSession(id, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != total {
		t.Fatalf("%d chunks, want %d", len(chunks), total)
	}
	for _, c := range chunks {
		want := collectorChunk(f.harness, c.Seq, f.lines[c.Seq%4])
		if c.Text != want.Text || c.Raw != want.Raw {
			t.Fatalf("seq %d reads %q, want %q", c.Seq, c.Text, want.Text)
		}
	}
	// What the server wrote after the run stays as it is: a second run finishes it.
	slimText(t, path, SlimTextOptions{})
	for _, text := range rowsWithText(t, path) {
		if text != "" {
			t.Fatal("text left after the second run")
		}
	}
}

// A realistic database: a quarter of the rows are prose, a fifth tool results,
// a tenth tool calls, the rest line types without text. The file shrinks by a
// quarter or more after the run and VACUUM, and each batch holds the write lock
// for a short time.
func TestSlimTextShrinksTheFile(t *testing.T) {
	sessions, perSession := 40, 250
	if testing.Short() {
		sessions = 10
	}
	// GHOSTTREE_SLIMTEXT_SESSIONS sizes the database for a measurement run.
	if v, err := strconv.Atoi(os.Getenv("GHOSTTREE_SLIMTEXT_SESSIONS")); err == nil && v > 0 {
		sessions = v
	}
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	buildTranscripts(t, s, sessions, perSession, 1)
	s.Close()
	before := fileSizeOf(t, path)

	rep := slimText(t, path, SlimTextOptions{})
	if rep.Emptied == 0 {
		t.Fatalf("%+v", rep)
	}
	if rep.LockMax >= 50*time.Millisecond {
		t.Logf("the longest lock was %s (a slow machine?)", rep.LockMax)
	}
	t.Logf("%d rows, %d emptied, %d batches, lock p50 %s p95 %s max %s", rep.Rows, rep.Emptied, rep.Batches, rep.LockP50, rep.LockP95, rep.LockMax)
	if _, err := VacuumDatabase(context.Background(), path, VacuumOptions{FreeBytes: plenty}); err != nil {
		t.Fatal(err)
	}
	after := fileSizeOf(t, path)
	shrunk := 1 - float64(after)/float64(before)
	t.Logf("file %d -> %d bytes, %.1f%% smaller", before, after, 100*shrunk)
	if shrunk < 0.25 {
		t.Fatalf("the file shrank by %.1f%%, want at least 25%%", 100*shrunk)
	}
}

// buildTranscripts fills s with sessions of lines shaped like the ones the
// harness writes, with their metadata and uploaded text.
func buildTranscripts(t testing.TB, s *Store, sessions, perSession int, seed int64) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	vocab := make([]string, 3000)
	for i := range vocab {
		b := make([]byte, 3+rng.Intn(8))
		for j := range b {
			b[j] = byte('a' + rng.Intn(26))
		}
		vocab[i] = string(b)
	}
	prose := func(words int) string {
		var sb strings.Builder
		for i := 0; i < words; i++ {
			// A skewed pick: some words come back, as in prose.
			sb.WriteString(vocab[int(float64(len(vocab))*rng.Float64()*rng.Float64())])
			sb.WriteByte(' ')
		}
		return strings.TrimSpace(sb.String())
	}
	uuid := func() string {
		return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", rng.Uint32(), rng.Intn(1<<16), rng.Intn(1<<16), rng.Intn(1<<16), rng.Int63n(1<<48))
	}
	for n := 0; n < sessions; n++ {
		ext := fmt.Sprintf("bench-%d", n)
		id, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: ext, Scope: scope.Axes{Project: "github.com/bench/p"}, StartedAt: "2026-11-01T00:00:00Z"})
		if err != nil {
			t.Fatal(err)
		}
		var chunks []Chunk
		parent := uuid()
		for seq := 0; seq < perSession; seq++ {
			meta := fmt.Sprintf(`"uuid":"%s","parentUuid":"%s","isSidechain":false,"userType":"external","cwd":"/work/p","sessionId":"%s","version":"2.1.0","gitBranch":"main","timestamp":"2026-11-01T10:%02d:%02dZ"`,
				uuid(), parent, ext, seq/60%60, seq%60)
			var line string
			switch r := rng.Intn(100); {
			case r < 12:
				line = `{"type":"user","message":{"role":"user","content":"` + prose(20+rng.Intn(60)) + `"},` + meta + `}`
			case r < 25:
				line = `{"type":"assistant","message":{"role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"` + prose(40+rng.Intn(120)) + `"}],"usage":{"input_tokens":1200,"output_tokens":340,"cache_read_input_tokens":48211}},` + meta + `}`
			case r < 35:
				line = `{"type":"assistant","message":{"role":"assistant","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"toolu_` + uuid()[:12] + `","name":"Bash","input":{"command":"` + prose(8) + `","description":"` + prose(5) + `"}}],"usage":{"input_tokens":1500,"output_tokens":90}},` + meta + `}`
			case r < 55:
				line = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_` + uuid()[:12] + `","content":"` + prose(150+rng.Intn(500)) + `"}]},` + meta + `}`
			default:
				line = `{"type":"system","subtype":"info","level":"info","content":"` + prose(4) + `",` + meta + `}`
			}
			chunks = append(chunks, collectorChunk("claude-code", seq, line))
		}
		if err := s.AppendChunks(id, chunks); err != nil {
			t.Fatal(err)
		}
	}
}

// BenchmarkReadWindow is what reading a window of the session page costs while
// the text is stored and after it is derived. The difference is the price of
// IDEA-25 per window.
func BenchmarkReadWindow(b *testing.B) {
	for _, mode := range []string{"stored", "derived"} {
		b.Run(mode, func(b *testing.B) {
			s, err := Open(filepath.Join(b.TempDir(), "ghosttree.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			buildTranscripts(b, s, 4, 400, 1)
			if mode == "derived" {
				if _, err := s.db.Exec(`UPDATE session_chunks SET text_chars = length(text), text = '' WHERE text != ''`); err != nil {
					b.Fatal(err)
				}
			}
			id, err := s.SessionByID(1)
			if err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				w, err := s.SessionWindow(id.ID, 0, 0, 200)
				if err != nil || len(w.Chunks) != 200 {
					b.Fatalf("%d chunks, %v", len(w.Chunks), err)
				}
			}
		})
	}
}

// onlyCopy is a line the parsers do not understand, with a text next to it that
// nothing else holds.
const (
	onlyCopyLine = `{"type":"summary","summary":"leaf","leafUuid":"x"}`
	onlyCopyText = "the only copy of this text"
)

// underivableDB is textDB plus one row whose text cannot be derived from its raw.
func underivableDB(t *testing.T) (path string, session int64, id int64) {
	t.Helper()
	path, sessions := textDB(t)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, text := chunktext.Derive("claude", onlyCopyLine); text != "" {
		t.Fatalf("the line is derivable after all: %q", text)
	}
	if err := s.AppendChunks(sessions[0], []Chunk{{Seq: 90, Role: "other", Text: onlyCopyText, Raw: onlyCopyLine}}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT id FROM session_chunks WHERE session_id = ? AND seq = 90`, sessions[0]).Scan(&id); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := s.db.QueryRow(`SELECT text FROM session_chunks WHERE id = ?`, id).Scan(&stored); err != nil || stored != onlyCopyText {
		t.Fatalf("the row was not written with its text: %q, %v", stored, err)
	}
	return path, sessions[0], id
}

// A row whose line yields nothing keeps its text: emptied, it would read as an
// empty chunk and the text would be gone for good. The dry run says so.
func TestSlimTextKeepsTextThatCannotBeDerived(t *testing.T) {
	path, session, id := underivableDB(t)

	dry := slimText(t, path, SlimTextOptions{DryRun: true})
	if dry.Underivable != 1 || dry.Same+dry.Cut+dry.Other+dry.Underivable != dry.WithText {
		t.Fatalf("dry run: %+v", dry)
	}

	rep := slimText(t, path, SlimTextOptions{Batch: 4})
	if rep.Underivable != 1 {
		t.Fatalf("the run did not count the row: %+v", rep)
	}
	if rep.Emptied != dry.WithText-1 {
		t.Fatalf("emptied %d, rows with text %d, one of them underivable", rep.Emptied, dry.WithText)
	}
	if got := rowsWithText(t, path)[id]; got != onlyCopyText {
		t.Fatalf("the text of the underivable row is %q", got)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var read string
	chunks, err := s.ReadSession(session, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range chunks {
		if c.Seq == 90 {
			read = c.Text
		}
	}
	if read != onlyCopyText {
		t.Fatalf("ReadSession gives %q for the row", read)
	}

	// A second run leaves it alone and says so again; restore has nothing to do for it.
	again := slimText(t, path, SlimTextOptions{})
	if again.Emptied != 0 || again.Underivable != 1 {
		t.Fatalf("second run: %+v", again)
	}
	slimText(t, path, SlimTextOptions{Restore: true})
	if got := rowsWithText(t, path)[id]; got != onlyCopyText {
		t.Fatalf("after restore the text is %q", got)
	}
}

// The UPDATE only empties a row that still holds the text that was derived
// against: a change between reading and writing leaves the row alone.
func TestSlimTextEmptiesOnlyTheRowItRead(t *testing.T) {
	path, _ := textDB(t)
	rows := rowsWithText(t, path)
	var id int64
	for i, text := range rows {
		if text != "" {
			id = i
			break
		}
	}
	db := openRaw(t, path)
	if _, err := db.Exec(`UPDATE session_chunks SET text = 'changed meanwhile' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := slimTextEmptyOnce(context.Background(), db, []slimTextRow{{id: id, text: rows[id]}}, id)
	if err != nil {
		t.Fatal(err)
	}
	if got := rowsWithText(t, path)[id]; got != "changed meanwhile" {
		t.Fatalf("a row that was changed since it was read was emptied: %q", got)
	}
}
