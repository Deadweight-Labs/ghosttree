package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/rawcodec"
	"github.com/Deadweight-Labs/ghosttree/internal/transcript"
)

// realLines sind Zeilen in den Formen, die Claude Code und Codex schreiben,
// lang genug, um gepackt zu werden.
func realLines(i int) []string {
	long := strings.Repeat(fmt.Sprintf("line %d of the build output: ok ", i), 40)
	return []string{
		fmt.Sprintf(`{"parentUuid":"p%[1]d","isSidechain":false,"userType":"external","cwd":"/home/u/Projects/x","sessionId":"s1","version":"2.1.301","gitBranch":"main","type":"user","message":{"role":"user","content":"please rename the flag number %[1]d and run the tests"},"uuid":"u%[1]d","timestamp":"2026-11-01T10:%02[2]d:00.000Z"}`, i, i%60),
		fmt.Sprintf(`{"parentUuid":"u%[1]d","isSidechain":false,"userType":"external","cwd":"/home/u/Projects/x","sessionId":"s1","version":"2.1.301","gitBranch":"main","type":"assistant","message":{"role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"Renamed the flag %[1]d; the tests pass."},{"type":"tool_use","id":"toolu_%[1]d","name":"Bash","input":{"command":"go test ./..."}}],"usage":{"input_tokens":3,"output_tokens":40}},"uuid":"a%[1]d","timestamp":"2026-11-01T10:%02[2]d:01.000Z"}`, i, i%60),
		fmt.Sprintf(`{"parentUuid":"a%[1]d","type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_%[1]d","type":"tool_result","content":%[3]q}]},"uuid":"r%[1]d","timestamp":"2026-11-01T10:%02[2]d:02.000Z"}`, i, i%60, long),
	}
}

func codexLines(i int) []string {
	return []string{
		fmt.Sprintf(`{"timestamp":"2026-11-01T11:%02d:00.000Z","type":"turn_context","payload":{"cwd":"/home/u/Projects/x","approval_policy":"never","model":"gpt-5.5-codex","effort":"high","summary":"auto"}}`, i%60),
		fmt.Sprintf(`{"timestamp":"2026-11-01T11:%02d:01.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_%d","output":%q}}`, i%60, i, strings.Repeat("compiled package ok\n", 30)),
	}
}

func appendLines(t *testing.T, s *Store, harness, ext string, lines []string) int64 {
	t.Helper()
	id, err := s.UpsertSession(Session{Harness: harness, ExternalID: ext, StartedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	var chunks []Chunk
	for i, l := range lines {
		chunks = append(chunks, Chunk{Seq: i, Role: "", Text: fmt.Sprintf("text %d", i), Raw: l})
	}
	if err := s.AppendChunks(id, chunks); err != nil {
		t.Fatal(err)
	}
	return id
}

func trainTestDict(t *testing.T, s *Store) RawDictInfo {
	t.Helper()
	var samples [][]byte
	for i := 0; i < 200; i++ {
		for _, l := range append(realLines(i), codexLines(i)...) {
			samples = append(samples, []byte(l))
		}
	}
	info, err := s.AddRawDict(context.Background(), rawcodec.Train(samples, rawcodec.DictSize), len(samples), 0)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func openRuntimeTest(t *testing.T) *Store {
	t.Helper()
	s, err := OpenRuntime(filepath.Join(t.TempDir(), "ghosttree.db"), DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// storageOf sagt je Zeile einer Session, wie raw gespeichert ist.
func storageOf(t *testing.T, db *sql.DB, session int64) (packed, text int, versions map[int64]int) {
	t.Helper()
	rows, err := db.Query(`SELECT typeof(raw), raw, slim_version FROM session_chunks WHERE session_id=?`, session)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	versions = map[int64]int{}
	for rows.Next() {
		var typ string
		var v rawcodec.Value
		var sv int
		if err := rows.Scan(&typ, &v, &sv); err != nil {
			t.Fatal(err)
		}
		if sv != 1 {
			t.Errorf("slim_version %d", sv)
		}
		switch typ {
		case "blob":
			packed++
			d, _ := rawcodec.FrameDict(v.Frame)
			versions[d]++
		case "text":
			text++
		default:
			t.Errorf("raw stored as %s", typ)
		}
	}
	return packed, text, versions
}

func TestNewLinesArePackedAndEveryReaderUnpacks(t *testing.T) {
	for name, open := range map[string]func(t *testing.T) *Store{"direct": openTest, "runtime": openRuntimeTest} {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			dict := trainTestDict(t, s)
			var lines []string
			for i := 0; i < 5; i++ {
				lines = append(lines, realLines(i)...)
			}
			id := appendLines(t, s, "claude-code", "packed-"+name, lines)
			db := s.db
			packed, text, versions := storageOf(t, db, id)
			if packed == 0 || versions[dict.Version] != packed {
				t.Fatalf("packed %d (dictionaries %v), text %d", packed, versions, text)
			}
			want := make([]string, len(lines))
			for i, l := range lines {
				var keep bool
				if want[i], keep = sanitizeRaw(l); !keep {
					t.Fatalf("line %d dropped", i)
				}
			}
			got, err := s.ReadSession(id, 0, 100)
			if err != nil || len(got) != len(lines) {
				t.Fatalf("ReadSession: %d, %v", len(got), err)
			}
			for i, c := range got {
				if c.Raw != want[i] {
					t.Fatalf("ReadSession line %d:\n%s\n%s", i, c.Raw, want[i])
				}
			}
			all, err := s.SessionRaw(id)
			if err != nil || strings.Join(all, "\n") != strings.Join(want, "\n") {
				t.Fatalf("SessionRaw differs: %v", err)
			}
			w, err := s.SessionWindow(id, 0, -1, 100)
			if err != nil || len(w.Chunks) != len(lines) || w.Chunks[2].Raw != want[2] {
				t.Fatalf("SessionWindow: %+v, %v", len(w.Chunks), err)
			}
			// Der Index wird beim Schreiben aus der ungepackten Zeile gebildet.
			tool := transcript.Fields(transcript.Parse("claude-code", 2, want[2])).ToolOutput
			word := strings.Fields(tool)[0]
			var found int
			if err := db.QueryRow(`SELECT count(*) FROM chunk_fts JOIN chunk_index ci ON ci.chunk_id = chunk_fts.rowid
				WHERE chunk_fts MATCH ? AND ci.session_id=? AND ci.seq=2`, `tool_output: "`+word+`"`, id).Scan(&found); err != nil || found != 1 {
				t.Fatalf("index tool_output %q: found %d, %v", word, found, err)
			}
			ids := []int64{}
			rows, _ := db.Query(`SELECT id FROM session_chunks WHERE session_id=? ORDER BY seq`, id)
			for rows.Next() {
				var cid int64
				_ = rows.Scan(&cid)
				ids = append(ids, cid)
			}
			rows.Close()
			raws, err := s.direct().chunkRaws(ids)
			if err != nil || raws[ids[2]] != want[2] {
				t.Fatalf("chunkRaws: %v", err)
			}
		})
	}
}

// Die Vektoren hängen am Hash der entpackten Zeile: wird eine Zeile mit einem
// anderen Wörterbuch neu gepackt, gilt ihr Vektor weiter (#3404).
func TestSessionSourceIsTheUnpackedLine(t *testing.T) {
	s := openTest(t)
	trainTestDict(t, s)
	lines := realLines(1)
	id := appendLines(t, s, "claude-code", "source", lines)
	var chunk int64
	var v rawcodec.Value
	if err := s.db.QueryRow(`SELECT id, raw FROM session_chunks WHERE session_id=? AND seq=2`, id).Scan(&chunk, &v); err != nil || !v.Packed {
		t.Fatalf("not packed: %v", err)
	}
	line, _ := sanitizeRaw(lines[2])
	source := sessionSource(line)
	verify := func() bool {
		tx, err := s.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		ok, err := verifySessionSource(tx, s.rawState(), chunk, source)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !verify() {
		t.Fatal("source of the packed line does not match")
	}
	// Neu gepackt, ohne Wörterbuch und als Klartext: dieselbe Zeile, derselbe Hash.
	plain, _ := rawcodec.NewEncoder(rawcodec.Dict{})
	for _, stored := range []any{plain.Encode(line), line} {
		if _, err := s.db.Exec(`UPDATE session_chunks SET raw=? WHERE id=?`, stored, chunk); err != nil {
			t.Fatal(err)
		}
		if !verify() {
			t.Fatalf("source changed after repacking as %T", stored)
		}
	}
}

func TestRawDictionariesAreNeverDeletedOrChanged(t *testing.T) {
	s := openTest(t)
	info := trainTestDict(t, s)
	if info.Version != 1 || info.Bytes == 0 {
		t.Fatalf("dictionary %+v", info)
	}
	if _, err := s.db.Exec(`DELETE FROM session_raw_dicts`); err == nil || !strings.Contains(err.Error(), "never deleted") {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE session_raw_dicts SET dict = x'00'`); err == nil || !strings.Contains(err.Error(), "never changes") {
		t.Fatalf("update: %v", err)
	}
	second := trainTestDict(t, s)
	if second.Version != 2 {
		t.Fatalf("second dictionary %+v", second)
	}
	dicts, err := listRawDicts(context.Background(), s.db)
	if err != nil || len(dicts) != 2 || dicts[0].Version != 2 {
		t.Fatalf("dictionaries %+v, %v", dicts, err)
	}
}

// Ein neues Wörterbuch gilt für neue Zeilen, ältere Zeilen bleiben lesbar.
func TestNewDictionaryForNewLinesOldOnesStayReadable(t *testing.T) {
	s := openRuntimeTest(t)
	first := trainTestDict(t, s)
	a := appendLines(t, s, "claude-code", "first", realLines(1))
	second := trainTestDict(t, s)
	b := appendLines(t, s, "claude-code", "second", realLines(2))
	if _, _, v := storageOf(t, s.db, a); v[first.Version] == 0 {
		t.Fatalf("first session packed with %v", v)
	}
	if _, _, v := storageOf(t, s.db, b); v[second.Version] == 0 || v[first.Version] != 0 {
		t.Fatalf("second session packed with %v", v)
	}
	for _, id := range []int64{a, b} {
		if got, err := s.SessionRaw(id); err != nil || len(got) != 3 {
			t.Fatalf("session %d: %d lines, %v", id, len(got), err)
		}
	}
}

// Ohne Wörterbuch wird ohne Vorgabe gepackt; ab genug Zeilen lernt der Server
// eins, einmal.
func TestEnsureRawDictLearnsOnceFromEnoughLines(t *testing.T) {
	s := openRuntimeTest(t)
	id := appendLines(t, s, "claude-code", "few", realLines(0))
	if _, _, v := storageOf(t, s.db, id); v[0] == 0 {
		t.Fatalf("without a dictionary: %v", v)
	}
	if _, trained, err := EnsureRawDict(context.Background(), s); err != nil || trained {
		t.Fatalf("learned from too few lines: %v %v", trained, err)
	}
	var lines []string
	for i := 0; len(lines) < rawDictMinChunks; i++ {
		lines = append(lines, append(realLines(i), codexLines(i)...)...)
	}
	appendLines(t, s, "claude-code", "many", lines)
	info, trained, err := EnsureRawDict(context.Background(), s)
	if err != nil || !trained || info.Version != 1 || info.SampleLines < 100 {
		t.Fatalf("dictionary %+v, %v %v", info, trained, err)
	}
	if _, trained, err := EnsureRawDict(context.Background(), s); err != nil || trained {
		t.Fatalf("learned twice: %v %v", trained, err)
	}
	if got, err := s.SessionRaw(id); err != nil || len(got) != 3 {
		t.Fatalf("old lines: %d, %v", len(got), err)
	}
}

// Codex nennt das Modell in turn_context ohne Rolle; der Vorfilter dafür lief in
// SQL über raw und läuft jetzt in Go über die entpackte Zeile.
func TestModelObservationsReadPackedTurnContext(t *testing.T) {
	s := openTest(t)
	trainTestDict(t, s)
	var lines []string
	for i := 0; i < 3; i++ {
		lines = append(lines, codexLines(i)...)
	}
	id := appendLines(t, s, "codex", "models", lines)
	if packed, _, _ := storageOf(t, s.db, id); packed == 0 {
		t.Fatal("nothing packed")
	}
	obs, err := sessionObservations(s, id, 100)
	if err != nil || len(obs) != 3 {
		t.Fatalf("observations %+v, %v", obs, err)
	}
	if at, ok := timeFromQ(s, id, 0); !ok || !strings.HasPrefix(at, "2026-11-01T11:") {
		t.Fatalf("time %q %v", at, ok)
	}
}

// Ein Rahmen, der nicht zu seinem Wörterbuch passt, ist ein Fehler beim Lesen,
// kein anderer Text.
func TestReadersFailLoudlyOnACorruptFrame(t *testing.T) {
	s := openTest(t)
	trainTestDict(t, s)
	id := appendLines(t, s, "claude-code", "corrupt", realLines(3))
	var chunk int64
	var v rawcodec.Value
	if err := s.db.QueryRow(`SELECT id, raw FROM session_chunks WHERE session_id=? AND seq=2`, id).Scan(&chunk, &v); err != nil || !v.Packed {
		t.Fatal("not packed")
	}
	bad := append([]byte{}, v.Frame...)
	bad[len(bad)-3] ^= 0x55
	if _, err := s.db.Exec(`UPDATE session_chunks SET raw=? WHERE id=?`, bad, chunk); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionRaw(id); err == nil {
		t.Fatal("SessionRaw returned a corrupt line without error")
	}
	if _, err := s.ReadSession(id, 0, 10); err == nil {
		t.Fatal("ReadSession returned a corrupt line without error")
	}
}
