package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// legacySearchLayout brings the search index of a database written by this
// version into the form the versions before REQ-622 criterion 4 left: the
// trigger chunks_ai keeps chunks_fts over session_chunks.text, every row of
// chunk_index holds its four fields and their words are in sess_fts.
func legacySearchLayout(t *testing.T, s *Store) {
	t.Helper()
	for _, q := range []string{
		`CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON session_chunks BEGIN
  INSERT INTO chunks_fts(rowid, text) VALUES (new.id, new.text);
END`,
		`INSERT INTO chunks_fts(chunks_fts) VALUES('rebuild')`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	var ids []int64
	rows, err := s.db.Query(`SELECT chunk_id FROM chunk_index WHERE fts = 1 ORDER BY chunk_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	words, err := s.direct().chunkWords(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		f := words[id]
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM chunk_fts WHERE rowid=?`, []any{id}},
			{`UPDATE chunk_index SET text=?, thinking=?, tool_input=?, tool_output=?, prompt='', fts=0 WHERE chunk_id=?`,
				[]any{f.Text, f.Thinking, f.ToolInput, f.ToolOutput, id}},
		} {
			if _, err := s.db.Exec(q.sql, q.args...); err != nil {
				t.Fatal(err)
			}
		}
		if f.Text != "" || f.Thinking != "" || f.ToolInput != "" || f.ToolOutput != "" {
			if _, err := s.db.Exec(`INSERT INTO sess_fts(rowid, text, thinking, tool_input, tool_output) VALUES(?,?,?,?,?)`,
				id, f.Text, f.Thinking, f.ToolInput, f.ToolOutput); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func queryCount(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// wordCount counts the chunks either index finds for a word.
func wordCount(t *testing.T, db *sql.DB, word string) int {
	t.Helper()
	return queryCount(t, db, `SELECT count(*) FROM (`+wordHits("")+`)`, word, word)
}

// searchCorpus seeds Claude Code and Codex sessions whose words sit in prompts,
// answers, thinking, commands and tool output, some of them shared.
func searchCorpus(t *testing.T, s *Store) (request int64) {
	t.Helper()
	claude := func(word string, extra ...string) []Chunk {
		lines := []string{
			userLine("2026-10-01T10:00:00Z", "please look into "+word+" for me"),
			assistantLine("2026-10-01T10:00:01Z", thinkBlock("reasoning about "+word+" and the rollout privately")),
			assistantLine("2026-10-01T10:00:02Z", textBlock("looking at the rollout"), bashBlock("t1", "grep -rn "+word+" .")),
			resultLine("2026-10-01T10:00:03Z", "t1", "./file.go:3: "+word+" appears here next to sqlite"),
			assistantLine("2026-10-01T10:00:04Z", textBlock("done with "+word+", the checksum matches")),
		}
		lines = append(lines, extra...)
		return chunksOf(lines...)
	}
	addSession(t, s, "c-alpha", 1, "github.com/x/y", "boxa", VisProject, claude("alpha"))
	addSession(t, s, "c-beta", 1, "github.com/x/y", "boxb", VisProject, claude("beta", userLine("2026-10-02T10:00:00Z", "and now alpha again with the rollout")))
	addSession(t, s, "c-gamma", 1, "github.com/x/z", "boxa", "", claude("gamma"))
	id, err := s.UpsertSession(Session{Harness: "codex", ExternalID: "x-delta", AccountID: 1, Scope: scope.Axes{Project: "github.com/x/y"}})
	if err != nil {
		t.Fatal(err)
	}
	codex := []string{
		codexTextLine("user", "why does the rollout of delta fail"),
		codexTextLine("assistant", "the sqlite migration checksum differs for delta"),
	}
	var chunks []Chunk
	for i, l := range codex {
		chunks = append(chunks, Chunk{Seq: i, Role: "user", Text: "codex " + l, Raw: l})
	}
	if err := s.AppendChunks(id, chunks); err != nil {
		t.Fatal(err)
	}
	detail, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "change", Title: "rollout"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartRequestWork(detail.Request.ID, id, "primary", "robin"); err != nil {
		t.Fatal(err)
	}
	return detail.Request.ID
}

var searchProbes = []string{"alpha", "beta", "gamma", "delta", "rollout", "sqlite", "checksum", "reasoning", "grep", "appears here", "alpha rollout", "missing"}

// searchSnapshot is every answer the session search gives for the probes, as
// text: the web search (all kinds, both orders, hits and snippets), the hits
// inside a session, the agents' search, a request's sessions, the outline and
// the texts picked for vectors.
func searchSnapshot(t *testing.T, s *Store, request int64) string {
	t.Helper()
	var sb strings.Builder
	pa := viewer(s, "person:1", "robin")
	for _, q := range searchProbes {
		for _, kind := range []string{"", "messages", "output", "commands", "thinking", "all"} {
			for _, sort := range []string{"", "newest"} {
				page, err := s.SearchTranscripts(pa, SearchQuery{Q: q, Kind: kind, Sort: sort, Limit: 50})
				if err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(&sb, "T %q %s %s: %d sessions %d matches\n", q, kind, sort, page.Sessions, page.Matches)
				for _, g := range page.Groups {
					fmt.Fprintf(&sb, "  %s total %d\n", g.Session.ExternalID, g.Total)
					for _, h := range g.Hits {
						fmt.Fprintf(&sb, "    %d %s %v\n", h.Seq, h.Kind, h.Parts)
					}
					if kind == "" && sort == "" {
						seqs, err := s.SessionHits(pa, g.Session, q, kind)
						if err != nil {
							t.Fatal(err)
						}
						fmt.Fprintf(&sb, "    hits %v\n", seqs)
					}
				}
			}
		}
		hits, err := s.SearchSessions(q, scope.Axes{}, "", 20)
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range hits {
			fmt.Fprintf(&sb, "S %q %s %d %q\n", q, h.Session.ExternalID, h.Seq, h.Snippet)
		}
		page, err := s.SearchRequestSessions(request, q, 10, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range page.Results {
			fmt.Fprintf(&sb, "R %q %d %d %q\n", q, h.SessionID, h.ChunkSeq, h.Snippet)
		}
	}
	all, err := s.allSessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, sess := range all {
		outline, err := s.SessionOutline(sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		picked, err := s.sessionPicked(sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sb, "O %s %+v\nP %s %+v\n", sess.ExternalID, outline, sess.ExternalID, picked)
	}
	return sb.String()
}

// The answers of the session search stay the same through the move: on the
// layout older versions left (words in sess_fts) and after ctx db slim-search;
// half way only the order by rank may differ. The snippets made from raw are the ones snippet() of
// sess_fts made while it held the text.
func TestSlimSearchKeepsEverySearchAnswer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	request := searchCorpus(t, s)
	legacySearchLayout(t, s)
	if queryCount(t, s.db, `SELECT count(*) FROM chunk_index WHERE fts = 1`) != 0 || queryCount(t, s.db, `SELECT count(*) FROM sess_fts WHERE sess_fts MATCH 'rollout'`) == 0 {
		t.Fatal("the seed is not in the old layout")
	}
	before := searchSnapshot(t, s, request)
	if !strings.Contains(before, "c-alpha") || !strings.Contains(before, "x-delta") || !strings.Contains(before, "\nR ") {
		t.Fatalf("the probes find too little to compare:\n%s", before)
	}

	// The snippets of the old layout, by the query the versions before made.
	pa := viewer(s, "person:1", "robin")
	page, err := s.SearchTranscripts(pa, SearchQuery{Q: "rollout", Kind: "all", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, g := range page.Groups {
		for _, h := range g.Hits {
			var cid int64
			if err := s.db.QueryRow(`SELECT chunk_id FROM chunk_index WHERE session_id=(SELECT id FROM sessions WHERE public_id=?) AND seq=?`, g.Session.PublicID, h.Seq).Scan(&cid); err != nil {
				t.Fatal(err)
			}
			var old [4]string
			if err := s.db.QueryRow(`SELECT snippet(sess_fts, 0, char(2), char(3), '…', 18), snippet(sess_fts, 1, char(2), char(3), '…', 18),
				snippet(sess_fts, 2, char(2), char(3), '…', 18), snippet(sess_fts, 3, char(2), char(3), '…', 18)
				FROM sess_fts WHERE sess_fts MATCH ? AND rowid=?`, matchExpr("rollout", "all"), cid).Scan(&old[0], &old[1], &old[2], &old[3]); err != nil {
				t.Fatal(err)
			}
			now, err := s.chunkSnippets(context.Background(), matchExpr("rollout", "all"), []int64{cid}, "\x02", "\x03", 18)
			if err != nil {
				t.Fatal(err)
			}
			if now[cid] != old {
				t.Fatalf("chunk %d: snippet from raw %q, from the stored text %q", cid, now[cid], old)
			}
			checked++
		}
	}
	if checked < 3 {
		t.Fatalf("only %d snippets compared", checked)
	}
	s.Close()

	// Half way: a run that is stopped after a few rows.
	ctx, stop := context.WithCancel(context.Background())
	if _, err := SlimSearch(ctx, path, SlimSearchOptions{Batch: 3, Pause: 1, Progress: func(r SlimSearchReport) {
		if r.Moved+r.MovedEmpty >= 6 {
			stop()
		}
	}}); err != context.Canceled {
		t.Fatalf("the run was not stopped: %v", err)
	}
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	moved := queryCount(t, s.db, `SELECT count(*) FROM chunk_index WHERE fts = 1`)
	if moved == 0 || moved == queryCount(t, s.db, `SELECT count(*) FROM chunk_index`) {
		t.Fatalf("%d rows moved: not half way", moved)
	}
	// Half way each index ranks by its own statistics, so the order by rank may
	// differ for the minutes the run takes; what is found and shown may not.
	if half := searchSnapshot(t, s, request); sortedLines(half) != sortedLines(before) {
		t.Fatalf("half way the answers differ:\n%s", firstDiff(sortedLines(before), sortedLines(half)))
	}
	s.Close()

	rep, err := SlimSearch(context.Background(), path, SlimSearchOptions{Batch: 4, Pause: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.SessFTSEmptied || !rep.ChunksFTSEmptied || rep.Remaining != 0 || rep.FieldBytes == 0 || rep.KeptPromptBytes == 0 {
		t.Fatalf("report %+v", rep)
	}
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if after := searchSnapshot(t, s, request); after != before {
		t.Fatalf("after the move the answers differ:\n%s", firstDiff(before, after))
	}
	// chunk_index keeps no copy of the text, only the prompts; the old indexes are empty.
	if n := queryCount(t, s.db, `SELECT count(*) FROM chunk_index WHERE fts != 1 OR text != '' OR thinking != '' OR tool_input != '' OR tool_output != ''`); n != 0 {
		t.Fatalf("%d rows still hold text fields", n)
	}
	if n := queryCount(t, s.db, `SELECT count(*) FROM chunk_index WHERE prompt != ''`); n != 5 {
		t.Fatalf("%d rows keep a prompt, want 5", n)
	}
	for _, table := range []string{"sess_fts", "chunks_fts"} {
		if n := queryCount(t, s.db, `SELECT count(*) FROM `+table+`_docsize`); n != 0 {
			t.Errorf("%s still has %d entries", table, n)
		}
	}
	if live, err := chunksFTSLive(context.Background(), s.db); err != nil || live {
		t.Fatalf("trigger chunks_ai still there: %v %v", live, err)
	}
	checkSearchIntegrity(t, s.db)

	// A line written now goes to chunk_fts and nowhere else.
	id, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "late"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChunks(id, chunksOf(userLine("2026-10-03T10:00:00Z", "zeppelin after the move"))); err != nil {
		t.Fatal(err)
	}
	if wordCount(t, s.db, "zeppelin") != 1 || queryCount(t, s.db, `SELECT count(*) FROM chunks_fts_docsize`) != 0 {
		t.Fatal("a new line is not only in chunk_fts")
	}
}

// The dry run counts what the run does and writes nothing; a second run finds
// nothing; rows an older binary writes after a run are moved by the next one.
func TestSlimSearchDryRunAndRepeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	searchCorpus(t, s)
	legacySearchLayout(t, s)
	s.Close()
	dump := func() string {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		return dumpTable(t, db, `SELECT * FROM chunk_index ORDER BY chunk_id`) +
			dumpTable(t, db, `SELECT * FROM sess_fts_docsize ORDER BY id`) +
			dumpTable(t, db, `SELECT * FROM chunks_fts_docsize ORDER BY id`) +
			dumpTable(t, db, `SELECT name FROM sqlite_master ORDER BY name`)
	}
	start := dump()
	dry, err := SlimSearch(context.Background(), path, SlimSearchOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if dump() != start {
		t.Fatal("the dry run wrote")
	}
	real, err := SlimSearch(context.Background(), path, SlimSearchOptions{Batch: 2, Pause: 1})
	if err != nil {
		t.Fatal(err)
	}
	if dry.Moved != real.Moved || dry.MovedEmpty != real.MovedEmpty || dry.FieldBytes != real.FieldBytes || dry.KeptPromptBytes != real.KeptPromptBytes ||
		dry.ChunksFTSRows != real.ChunksFTSRows || real.Moved == 0 {
		t.Fatalf("dry run %+v, real run %+v", dry, real)
	}
	again, err := SlimSearch(context.Background(), path, SlimSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if again.Moved+again.MovedEmpty != 0 || again.ChunksFTSEmptied || len(again.Optimized) != 0 {
		t.Fatalf("second run %+v", again)
	}

	// An older binary writes the old way again.
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	id, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "rolled-back"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChunks(id, chunksOf(userLine("2026-10-03T10:00:00Z", "quokka written by an older binary"))); err != nil {
		t.Fatal(err)
	}
	legacySearchLayout(t, s)
	if wordCount(t, s.db, "quokka") != 1 {
		t.Fatal("a row of the old layout is not found")
	}
	s.Close()
	last, err := SlimSearch(context.Background(), path, SlimSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if last.Moved == 0 || !last.SessFTSEmptied || !last.ChunksFTSEmptied {
		t.Fatalf("run after a rollback %+v", last)
	}
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if wordCount(t, s.db, "quokka") != 1 || queryCount(t, s.db, `SELECT count(*) FROM chunk_index WHERE fts = 0`) != 0 {
		t.Fatal("the rolled back row was not moved")
	}
	checkSearchIntegrity(t, s.db)
}

// A server that holds the write lock longer than busy_timeout does not end the
// run, as for slim-sessions.
func TestSlimSearchWaitsOutALongWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	searchCorpus(t, s)
	legacySearchLayout(t, s)
	s.Close()

	oldDSN, oldBackoff := slimSearchDSN, slimBusyBackoff
	slimSearchDSN = func(p string) string { return p + "?_pragma=busy_timeout(50)" }
	slimBusyBackoff = []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 2 * time.Second}
	defer func() { slimSearchDSN, slimBusyBackoff = oldDSN, oldBackoff }()

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
	rep, err := SlimSearch(context.Background(), path, SlimSearchOptions{Batch: 1000, Pause: 1})
	<-released
	if err != nil {
		t.Fatalf("run ended at the lock: %v", err)
	}
	if time.Since(started) < 2*time.Second {
		t.Fatalf("run finished in %v while the lock was held for 2s: it never met the lock", time.Since(started))
	}
	var rows int64
	if err := holder.QueryRow(`SELECT count(*) FROM chunk_index`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rep.Moved+rep.MovedEmpty != rows || !rep.SessFTSEmptied {
		t.Fatalf("counts after a retried step: %+v of %d rows", rep, rows)
	}
}

// A database the server of this version has not migrated yet is left alone.
func TestSlimSearchNeedsTheNewSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE chunk_index(chunk_id INTEGER PRIMARY KEY, session_id INTEGER, seq INTEGER, role TEXT, ts TEXT, is_prompt INTEGER, tool_calls INTEGER, text TEXT, thinking TEXT, tool_input TEXT, tool_output TEXT)`,
		`CREATE VIRTUAL TABLE sess_fts USING fts5(text, thinking, tool_input, tool_output, content='chunk_index', content_rowid='chunk_id')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	if _, err := SlimSearch(context.Background(), path, SlimSearchOptions{}); err == nil || !strings.Contains(err.Error(), "start the new ghosttree server") {
		t.Fatalf("err = %v", err)
	}
}

// checkSearchIntegrity checks the structure of the indexes, and chunks_fts
// against its content while the trigger keeps it. sess_fts is not compared with
// chunk_index (rank=1): rows without words were never entered, on every layout.
func checkSearchIntegrity(t *testing.T, db *sql.DB) {
	t.Helper()
	var ok string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
		t.Fatalf("integrity_check: %q, %v", ok, err)
	}
	for _, q := range []string{
		`INSERT INTO sess_fts(sess_fts) VALUES('integrity-check')`,
		`INSERT INTO chunk_fts(chunk_fts) VALUES('integrity-check')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if live, err := chunksFTSLive(context.Background(), db); err != nil {
		t.Fatal(err)
	} else if live {
		if _, err := db.Exec(`INSERT INTO chunks_fts(chunks_fts, rank) VALUES('integrity-check', 1)`); err != nil {
			t.Fatalf("chunks_fts integrity-check: %v", err)
		}
	}
}

// firstDiff shows the first lines in which two snapshots differ.
func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) || i < len(bl); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			from := max(i-3, 0)
			return fmt.Sprintf("line %d\nbefore: %q\nnow:    %q\ncontext before:\n%s", i, x, y, strings.Join(al[from:min(i+3, len(al))], "\n"))
		}
	}
	return ""
}

func sortedLines(s string) string {
	lines := strings.Split(s, "\n")
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}
