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

// buildIndexedArchive fills a store with chunks that reach the full-text index:
// AppendChunks derives the indexed words from the raw line, which the archive of
// BenchmarkRelevanceArchive (a raw line of "{}") does not give. The words follow
// Zipf's law as there, v000000 the commonest.
func buildIndexedArchive(b testing.TB, path string, chunks int) []string {
	b.Helper()
	const vocabulary = 120000
	words := make([]string, vocabulary)
	for i := range words {
		words[i] = fmt.Sprintf("v%06d", i)
	}
	rng := rand.New(rand.NewSource(1))
	zipf := rand.NewZipf(rng, 1.07, 1, vocabulary-1)
	s, err := OpenWithOptions(path, OpenOptions{MaxOpenConns: 1})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	const sessions, batch = 300, 500
	ids := make([]int64, sessions)
	for i := range ids {
		if ids[i], err = s.UpsertSession(Session{Harness: "codex", ExternalID: fmt.Sprintf("bench-%d", i), StartedAt: "2026-08-23T00:00:00Z"}); err != nil {
			b.Fatal(err)
		}
	}
	seq := make([]int, sessions)
	for written := 0; written < chunks; {
		i := rng.Intn(sessions)
		var batchChunks []Chunk
		for n := 0; n < batch && written < chunks; n++ {
			length := 8 + rng.Intn(40)
			if rng.Intn(10) == 0 {
				length += 100 + rng.Intn(200)
			}
			var sb strings.Builder
			for w := 0; w < length; w++ {
				if w > 0 {
					sb.WriteByte(' ')
				}
				sb.WriteString(words[zipf.Uint64()])
			}
			text := sb.String()
			batchChunks = append(batchChunks, Chunk{Seq: seq[i], Role: "user", Text: text, Raw: codexTextLine("user", text)})
			seq[i]++
			written++
		}
		if err := s.AppendChunks(ids[i], batchChunks); err != nil {
			b.Fatal(err)
		}
	}
	return words
}

// BenchmarkSessionWordSearch compares the word search over sessions before and
// after "AND first, OR as the fallback": "or_only" asks the OR expression the way
// ftsSessionHits did, "and_first" asks as it does now. The archive is built by
// buildIndexedArchive (Zipf vocabulary), so the first words
// are the commonest, as "go" and "test" are in real transcripts.
//
//	GHOSTTREE_RELEVANCE_BENCH_CHUNKS=1140000 go test ./internal/store -run '^$' \
//	  -bench BenchmarkSessionWordSearch -benchtime 20x
func BenchmarkSessionWordSearch(b *testing.B) {
	chunks := 400000
	if v := os.Getenv("GHOSTTREE_RELEVANCE_BENCH_CHUNKS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			b.Fatalf("GHOSTTREE_RELEVANCE_BENCH_CHUNKS=%q", v)
		}
		chunks = n
	}
	path := filepath.Join(b.TempDir(), "bench.db")
	words := buildIndexedArchive(b, path, chunks)
	s, err := OpenRuntime(path, DefaultWriterConfig())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close() })
	var indexed int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM chunk_index`).Scan(&indexed); err != nil {
		b.Fatal(err)
	}
	b.Logf("archive: %d indexed chunks", indexed)
	r, err := s.readableSessions(scope.Axes{}, nil, SessionPrefilter{Unrestricted: true})
	if err != nil {
		b.Fatal(err)
	}
	pre := SessionPrefilter{Unrestricted: true}
	const limit = 20

	queries := []struct{ name, text string }{
		{"two_common", words[2] + " " + words[5]},
		{"three_common", words[1] + " " + words[3] + " " + words[6]},
		{"common_and_mid", words[4] + " " + words[400]},
		{"two_rare", words[3000] + " " + words[4000]},
	}
	for _, q := range queries {
		var orN, andN int
		b.Run(q.name+"/or_only", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				hits, _, err := s.ftsSessionHitsFor(ftsQuery(q.text), "", r, pre, limit)
				if err != nil {
					b.Fatal(err)
				}
				orN = len(hits)
			}
		})
		b.Run(q.name+"/and_first", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				hits, _, err := s.ftsSessionHits(q.text, "", r, pre, limit)
				if err != nil {
					b.Fatal(err)
				}
				andN = len(hits)
			}
		})
		b.Logf("%s: or_only %d rows, and_first %d rows", q.name, orN, andN)
	}
}
