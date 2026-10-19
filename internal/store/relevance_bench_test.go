package store

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// BenchmarkRelevanceArchive measures what one prompt costs the relevance rule on
// an archive of production size. The rule runs between pressing enter and the
// model reading the prompt, with 900 ms to spare for everything, so the figure
// that matters is the slowest prompt, not the average one.
//
// The language gate counts a word in the reader's own sessions only, so what
// it costs depends on whose prompt it is. The archive therefore has four
// accounts: one with most of the sessions of a long-used instance, one with a few
// sessions, a large rest, and one too new to have a gate at all. Their
// sessions are uploaded in interleaved batches, as the collectors do, so the span
// a session occupies in the full-text index is full of other accounts' chunks.
//
// The corpus is synthetic and deterministic: a vocabulary whose word frequencies
// follow Zipf's law, which is how any natural language behaves, filled into
// chunks of mixed length and into three thousand knowledge entries. The real
// archive holds private transcripts that do not belong in a test, and what the
// benchmark has to show — that the cost of the gate is bounded by the session
// cap however large the archive grows — is a property of the queries, not of
// the words.
//
//	GHOSTTREE_RELEVANCE_BENCH_CHUNKS=1140000 go test ./internal/store -run '^$' \
//	  -bench BenchmarkRelevanceArchive -benchtime 100x
//
// Without the variable the benchmark builds four hundred thousand chunks, which
// takes under a minute and is enough for the account with many sessions to have
// a gate, so a query that stopped using the index shows.
func BenchmarkRelevanceArchive(b *testing.B) {
	chunks := 400000
	if v := os.Getenv("GHOSTTREE_RELEVANCE_BENCH_CHUNKS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			b.Fatalf("GHOSTTREE_RELEVANCE_BENCH_CHUNKS=%q", v)
		}
		chunks = n
	}
	path := filepath.Join(b.TempDir(), "bench.db")
	words := buildBenchArchive(b, path, chunks, 3000)

	s, err := OpenRuntime(path, DefaultWriterConfig())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close() })
	var total, sessions int
	if err := s.db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT session_id) FROM session_chunks`).Scan(&total, &sessions); err != nil {
		b.Fatal(err)
	}
	b.Logf("archive: %d chunks in %d sessions, %d vocabulary words", total, sessions, len(words))

	ordinary := strings.Join(words[0:14], " ")
	prompts := []struct{ name, text string }{
		{"ordinary", ordinary},
		{"codename", "was weißt du über hlpvpngw " + ordinary},
		// Mid-rank words: rare among the entries and rare in the reader's own
		// text, so every one of them has to be counted over the whole sample.
		{"wall_of_rare_words", strings.Join(words[5000:5024], " ")},
		// Words that are everywhere in the reader's own text: ruled out after a
		// few sessions.
		{"own_heavy_x24", strings.Join(benchWords("ownheavy"), " ")},
		// Words that other accounts use all the time and the reader never does:
		// the gate reads past their chunks inside the reader's spans.
		{"foreign_heavy_x24", strings.Join(benchWords("foreignheavy"), " ")},
	}
	for _, acct := range []struct {
		name, principal string
	}{{"many_sessions", "person:1"}, {"few_sessions", "person:2"}, {"too_new", "person:4"}} {
		for _, p := range prompts {
			b.Run(acct.name+"/"+p.name, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := s.RelevantFor(RelevanceQuery{Text: p.text, Limit: 12, Account: acct.principal, Session: "bench"}); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
	// What a hint costs on top: the full-text search of sessions for one rare
	// word, which the server runs for at most three words per prompt.
	b.Run("hint_session_search", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := s.SearchSessionsVisible("hlpvpngw", scope.Axes{}, "bench", 1, nil, SessionPrefilter{Unrestricted: true}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// benchWords are twenty-four words under one prefix: "ownheavy" words fill a
// tenth of the chunks of person:1, "foreignheavy" words a tenth of the chunks
// of person:3. Few entries name each of them, so all pass the first gate and the
// second has to decide.
func benchWords(prefix string) []string {
	out := make([]string, 24)
	for i := range out {
		out[i] = fmt.Sprintf("%sword%02d", prefix, i)
	}
	return out
}

type benchSession struct {
	id      int64
	account int64
	target  int
	written int
}

func buildBenchArchive(b testing.TB, path string, chunks, entries int) []string {
	b.Helper()
	const vocabulary = 120000
	words := make([]string, vocabulary)
	for i := range words {
		words[i] = fmt.Sprintf("v%06d", i)
	}
	rng := rand.New(rand.NewSource(1))
	zipf := rand.NewZipf(rng, 1.07, 1, vocabulary-1)
	text := func(n int) string {
		var sb strings.Builder
		for i := 0; i < n; i++ {
			if i > 0 {
				sb.WriteByte(' ')
			}
			sb.WriteString(words[zipf.Uint64()])
		}
		return sb.String()
	}
	length := func() int {
		switch r := rng.Intn(100); {
		case r < 60:
			return 8 + rng.Intn(22)
		case r < 90:
			return 30 + rng.Intn(90)
		default:
			return 120 + rng.Intn(280)
		}
	}

	s, err := OpenWithOptions(path, OpenOptions{MaxOpenConns: 1})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()

	tx, err := s.db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	own, foreign := benchWords("ownheavy"), benchWords("foreignheavy")
	for i := 0; i < entries; i++ {
		body := text(60 + rng.Intn(140))
		title := text(6 + rng.Intn(4))
		if i == 7 {
			body += " hlpvpngw"
		}
		if i < 20*24 {
			body += " " + own[i%24] + " " + foreign[i%24]
		}
		if _, err := insertKnowledgeTx(tx, withKnowledgeDefaults(Knowledge{Type: "note", Title: title, Body: body})); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}

	// Who owns how much. The account with many sessions holds two fifths of the
	// archive, the rest sits with a large other account, one account has a few
	// sessions and one barely started.
	type share struct {
		account  int64
		sessions int
		chunks   int
	}
	shares := []share{
		{1, 300, chunks * 4 / 10},
		{3, 450, chunks*6/10 - 9000 - 400},
		{2, 6, 9000},
		{4, 1, 400},
	}
	var plan []*benchSession
	for _, sh := range shares {
		for i := 0; i < sh.sessions; i++ {
			id, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: fmt.Sprintf("bench-%d-%d", sh.account, i), AccountID: sh.account})
			if err != nil {
				b.Fatal(err)
			}
			plan = append(plan, &benchSession{id: id, account: sh.account, target: max(1, sh.chunks/sh.sessions)})
		}
	}
	rng.Shuffle(len(plan), func(i, j int) { plan[i], plan[j] = plan[j], plan[i] })

	// Twelve sessions are being collected at any time, each uploading a batch of
	// up to four hundred chunks in turn, as the watchers do.
	const active, batch = 12, 400
	queue, window := plan, []*benchSession{}
	written := 0
	for len(queue) > 0 || len(window) > 0 {
		for len(window) < active && len(queue) > 0 {
			window, queue = append(window, queue[0]), queue[1:]
		}
		i := rng.Intn(len(window))
		sess := window[i]
		tx, err := s.db.Begin()
		if err != nil {
			b.Fatal(err)
		}
		prepared, err := tx.Prepare(`INSERT INTO session_chunks(session_id, seq, role, text, raw) VALUES(?,?,?,?,?)`)
		if err != nil {
			b.Fatal(err)
		}
		for n := 0; n < batch && sess.written < sess.target; n++ {
			t := text(length())
			switch {
			case sess.account == 1 && written%10 == 0:
				t += " " + own[(written/10)%24]
			case sess.account == 3 && written%10 == 0:
				t += " " + foreign[(written/10)%24]
			}
			// A codename of the reader's that came up in two chunks, and once in
			// somebody else's text.
			if (sess.account == 1 && (written == 1000 || written == chunks/3)) || (sess.account == 3 && written == chunks/2) {
				t += " hlpvpngw"
			}
			if _, err := prepared.Exec(sess.id, sess.written, "user", t, "{}"); err != nil {
				b.Fatal(err)
			}
			sess.written++
			written++
		}
		prepared.Close()
		if err := tx.Commit(); err != nil {
			b.Fatal(err)
		}
		if sess.written >= sess.target {
			window = append(window[:i], window[i+1:]...)
		}
	}
	return words
}
