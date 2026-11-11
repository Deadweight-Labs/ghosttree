package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/chunktext"
	"github.com/Deadweight-Labs/ghosttree/internal/redact"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// textFixture is a session of lines in the shapes the harnesses write. Each
// line comes with the text the collector would have uploaded: parsed from the
// original line and redacted.
type textFixture struct {
	harness string
	lines   []string
	// diverges lists the seqs whose derived text differs from the uploaded one:
	// the slimming cut a long output in raw where the collector's text still
	// had it whole.
	diverges map[int]bool
}

func collectorChunk(harness string, seq int, line string) Chunk {
	p := chunktext.Parse(harness)([]byte(line))
	return Chunk{Seq: seq, Role: p.Role, Text: redact.Redact(p.Text), Raw: line}
}

func claudeFixture() textFixture {
	long := func(c string) string { return strings.Repeat(c+" word ", 1700) } // about 10 KB
	return textFixture{
		harness: "claude-code",
		lines: []string{
			`{"type":"user","message":{"role":"user","content":"fix the auth bug in the oauth flow"}}`,
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Looking at the oauth flow now."},{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`,
			`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"PASS"},{"type":"text","text":"ok  \tinternal/auth"}]}]}}`,
			`{"type":"queue-operation","operation":"enqueue"}`,
			// Two long blocks: raw cuts each one, the collector cut the whole.
			`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":[{"type":"text","text":"` + long("a") + `"},{"type":"text","text":"` + long("b") + `"}]}]}}`,
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Ünïcode and a key sk-ant-` + strings.Repeat("A", 40) + ` in prose"}]}}`,
		},
		diverges: map[int]bool{4: true},
	}
}

func codexFixture() textFixture {
	return textFixture{
		harness: "codex",
		lines: []string{
			`{"type":"session_meta","payload":{"session_id":"s","cwd":"/work"}}`,
			`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"why does the build fail"}]}}`,
			`{"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"go build ./...\"}"}}`,
			`{"type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":{"stdout":"build failed: missing module","result":{"path":"go.mod"}}}}`,
			`{"type":"event_msg","payload":{"type":"token_count"}}`,
			`{"type":"response_item","payload":{"type":"function_call_output","call_id":"c2","output":"` + strings.Repeat("x", 20000) + `"}}`,
		},
		diverges: map[int]bool{},
	}
}

func storeFixture(t *testing.T, s *Store, ext string, f textFixture) int64 {
	t.Helper()
	id, err := s.UpsertSession(Session{Harness: f.harness, ExternalID: ext, Scope: scope.Axes{Project: "p"}, StartedAt: "2026-11-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	var chunks []Chunk
	for i, line := range f.lines {
		chunks = append(chunks, collectorChunk(f.harness, i, line))
	}
	if err := s.AppendChunks(id, chunks); err != nil {
		t.Fatal(err)
	}
	return id
}

// emptyText is what ctx db slim-text does to the rows, by hand.
func emptyText(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE session_chunks SET text_chars = length(text), text = '' WHERE text != ''`); err != nil {
		t.Fatal(err)
	}
}

func readTexts(t *testing.T, s *Store, id int64) map[int]string {
	t.Helper()
	chunks, err := s.ReadSession(id, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]string{}
	for _, c := range chunks {
		out[c.Seq] = c.Text
	}
	w, err := s.SessionWindow(id, 0, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Chunks) != len(chunks) {
		t.Fatalf("window has %d chunks, ReadSession %d", len(w.Chunks), len(chunks))
	}
	for _, c := range w.Chunks {
		if out[c.Seq] != c.Text {
			t.Fatalf("seq %d: window text %q differs from ReadSession %q", c.Seq, c.Text, out[c.Seq])
		}
	}
	return out
}

// After text is emptied, ReadSession and the session window give the texts they
// gave before, except where the derivation is the known shorter view.
func TestDerivedTextEqualsStoredText(t *testing.T) {
	for _, f := range []textFixture{claudeFixture(), codexFixture()} {
		t.Run(f.harness, func(t *testing.T) {
			s := openTest(t)
			id := storeFixture(t, s, "x", f)
			before := readTexts(t, s, id)
			emptyText(t, s)
			after := readTexts(t, s, id)
			if len(before) != len(f.lines) || len(after) != len(f.lines) {
				t.Fatalf("chunks before %d, after %d, want %d", len(before), len(after), len(f.lines))
			}
			for seq := range f.lines {
				if f.diverges[seq] {
					if after[seq] == before[seq] || !strings.Contains(after[seq], "gekürzt") || after[seq] == "" {
						t.Errorf("seq %d should differ by the slimming cut: before %d bytes, after %d bytes", seq, len(before[seq]), len(after[seq]))
					}
					continue
				}
				if after[seq] != before[seq] {
					t.Errorf("seq %d: derived %q, stored %q", seq, after[seq], before[seq])
				}
			}
		})
	}
}

// The derived text is redacted like the stored one.
func TestDerivedTextIsRedacted(t *testing.T) {
	s := openTest(t)
	id := storeFixture(t, s, "x", claudeFixture())
	emptyText(t, s)
	got := readTexts(t, s, id)[5]
	if strings.Contains(got, "sk-ant-") || !strings.Contains(got, "Ünïcode") {
		t.Fatalf("derived text = %q", got)
	}
}

// A server that does not store the text writes the same reading view, and it
// counts the characters it left out.
func TestSessionTextDerivedWritesNoTextAndKeepsTheCount(t *testing.T) {
	stored, derived := openTest(t), openTest(t)
	if err := derived.setSessionText(SessionTextDerived); err != nil {
		t.Fatal(err)
	}
	f := claudeFixture()
	a := storeFixture(t, stored, "x", f)
	b := storeFixture(t, derived, "x", f)

	var left int
	if err := derived.db.QueryRow(`SELECT count(*) FROM session_chunks WHERE text != ''`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("derived store keeps text in %d rows, err %v", left, err)
	}
	var sumStored, sumDerived int
	if err := stored.db.QueryRow(`SELECT sum(text_chars) FROM session_chunks`).Scan(&sumStored); err != nil {
		t.Fatal(err)
	}
	if err := derived.db.QueryRow(`SELECT sum(text_chars) FROM session_chunks`).Scan(&sumDerived); err != nil {
		t.Fatal(err)
	}
	if sumStored == 0 || sumStored != sumDerived {
		t.Fatalf("text_chars stored %d, derived %d", sumStored, sumDerived)
	}
	emptyText(t, stored)
	ta, tb := readTexts(t, stored, a), readTexts(t, derived, b)
	for seq := range f.lines {
		if ta[seq] != tb[seq] {
			t.Errorf("seq %d reads differently: %q vs %q", seq, ta[seq], tb[seq])
		}
	}
	if err := derived.setSessionText("sometimes"); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
}

// A quote taken from the derived text is accepted, a foreign one is refused,
// by both checks.
func TestQuotesAreCheckedAgainstTheDerivedText(t *testing.T) {
	for _, name := range []string{"knowledge", "request"} {
		t.Run(name, func(t *testing.T) {
			s := openTest(t)
			id := storeFixture(t, s, "x", claudeFixture())
			emptyText(t, s)
			apply := func(digest, quote string) error {
				if name == "knowledge" {
					_, err := s.ApplySessionDistillation(id, digest, "v1", scope.Axes{Project: "p"},
						[]SessionDistilledItem{{Type: "note", Title: "t " + digest, Body: "b", Quote: quote, ChunkSeq: 1}})
					return err
				}
				_, err := s.ApplyRequestDistillation(id, digest, "req-v1", scope.Axes{Project: "p"},
					[]DistilledRequest{{Type: "feature", Title: "t " + digest, Body: "b", Quote: quote, ChunkSeq: 1}})
				return err
			}
			if err := apply("d1", "Looking at the oauth flow"); err != nil {
				t.Fatalf("a quote from the derived text was refused: %v", err)
			}
			if err := apply("d2", "never said in this transcript"); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("a foreign quote: err = %v, want a refusal", err)
			}
			// What the model saw is what is checked: the text of seq 1 is the
			// prose, then the call.
			if got := readTexts(t, s, id)[1]; !strings.Contains(got, "[tool call: Bash]") {
				t.Fatalf("derived text of seq 1 = %q", got)
			}
		})
	}
}

// ctx cost gives the same number before and after the text is emptied.
func TestCostCountsTheSameCharactersBeforeAndAfterEmptying(t *testing.T) {
	s := openTest(t)
	billedSession(t, s, "a", "github.com/x/one", "2026-08-01T00:00:00Z")
	storeFixture(t, s, "b", claudeFixture())
	storeFixture(t, s, "c", codexFixture())
	sessions1, chars1, err := s.PendingDistillationSize(scope.Axes{}, "2026-12-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	emptyText(t, s)
	sessions2, chars2, err := s.PendingDistillationSize(scope.Axes{}, "2026-12-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if chars1 == 0 || sessions1 != sessions2 || chars1 != chars2 {
		t.Fatalf("before: %d sessions, %d chars; after: %d sessions, %d chars", sessions1, chars1, sessions2, chars2)
	}
}

// The tool-call count does not depend on whether the text is stored.
func TestToolCallsAreCountedFromTheLineWhenTheTextIsEmpty(t *testing.T) {
	s := openTest(t)
	id, _ := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "a",
		Scope: scope.Axes{Project: "github.com/x/user"}, StartedAt: "2026-08-23T00:00:00Z"})
	if err := s.AppendChunks(id, []Chunk{
		toolCallChunk(1, "mcp__ghosttree__context_search", `{"query":"ufw"}`),
		toolCallChunk(2, "Bash", `{"command":"ls"}`),
	}); err != nil {
		t.Fatal(err)
	}
	emptyText(t, s)
	got, err := s.ToolCallsPerProject("mcp__ghosttree__")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Calls != 1 || got[0].Sessions != 1 {
		t.Fatalf("rows = %+v", got)
	}
}

// Derive is what the collector computes, for every line the fixtures hold, so
// moving the parsers changed nothing.
func TestDeriveMatchesTheCollectorForLinesThatStayWhole(t *testing.T) {
	for _, f := range []textFixture{claudeFixture(), codexFixture()} {
		for seq, line := range f.lines {
			if f.diverges[seq] || len(line) > 10000 {
				continue
			}
			c := collectorChunk(f.harness, seq, line)
			role, text := chunktext.Derive(f.harness, line)
			if role != c.Role || text != c.Text {
				t.Errorf("%s seq %d: derive (%q, %q), collector (%q, %q)", f.harness, seq, role, text, c.Role, c.Text)
			}
		}
	}
}

// Through the runtime writer, by both entry points, the text stays out too.
func TestRuntimeStoreWritesNoTextWhenDerived(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := OpenRuntimeWithText(path, DefaultWriterConfig(), SessionTextDerived)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	f := claudeFixture()
	a, err := s.UpsertSession(Session{Harness: f.harness, ExternalID: "a", Scope: scope.Axes{Project: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.UpsertSession(Session{Harness: f.harness, ExternalID: "b", Scope: scope.Axes{Project: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChunks(a, []Chunk{collectorChunk(f.harness, 0, f.lines[0])}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChunkBatches([]ChunkBatch{{SessionID: b, Chunks: []Chunk{collectorChunk(f.harness, 0, f.lines[1])}}}); err != nil {
		t.Fatal(err)
	}
	var withText, chars int
	if err := s.reader.db.QueryRow(`SELECT count(*) FILTER (WHERE text != ''), sum(text_chars) FROM session_chunks`).Scan(&withText, &chars); err != nil {
		t.Fatal(err)
	}
	if withText != 0 || chars == 0 {
		t.Fatalf("rows with text %d, text_chars %d", withText, chars)
	}
	got, err := s.ReadSession(b, 0, 10)
	if err != nil || len(got) != 1 || !strings.Contains(got[0].Text, "Looking at the oauth flow") {
		t.Fatalf("read %+v, %v", got, err)
	}
}

// In derived mode text_chars is the length of the text the readers see, not of
// the one the client sent, and a line that yields nothing keeps the client's
// text, since that text is the only copy.
func TestSessionTextDerivedCountsTheDerivedText(t *testing.T) {
	s := openTest(t)
	if err := s.setSessionText(SessionTextDerived); err != nil {
		t.Fatal(err)
	}
	id, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "d", Scope: scope.Axes{Project: "p"}, StartedAt: "2026-11-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	line := redactUserLine("kurz")
	if err := s.AppendChunks(id, []Chunk{
		{Seq: 0, Role: "user", Text: "viel länger als das, was sich ableiten lässt", Raw: line},
		{Seq: 1, Role: "other", Text: "only copy", Raw: `{"type":"summary","summary":"x","leafUuid":"y"}`},
	}); err != nil {
		t.Fatal(err)
	}
	type row struct {
		text  string
		chars int
	}
	got := map[int]row{}
	rows, err := s.db.Query(`SELECT seq, text, text_chars FROM session_chunks WHERE session_id = ?`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var r row
		if err := rows.Scan(&seq, &r.text, &r.chars); err != nil {
			t.Fatal(err)
		}
		got[seq] = r
	}
	if got[0].text != "" || got[0].chars != len([]rune("kurz")) {
		t.Errorf("derivable row: %+v, want no text and 4 characters", got[0])
	}
	if got[1].text != "only copy" || got[1].chars != len("only copy") {
		t.Errorf("row with nothing to derive: %+v, want its text kept", got[1])
	}
	read := readTexts(t, s, id)
	if read[0] != "kurz" || read[1] != "only copy" {
		t.Errorf("reads %v", read)
	}
}
