package server

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// BenchmarkPromptHookRelevant is what the server spends on one prompt of the
// prompt hook (GET /api/context/relevant) in an archive of sessions. The archive
// is synthetic and deterministic: a Zipf vocabulary filled into chunks of the
// account that asks, so the language gate has a sample to count in and the words
// of the prompt are neither in the knowledge nor in many sessions.
//
//	GHOSTTREE_HOOK_BENCH_CHUNKS=310000 go test ./internal/server -run '^$' \
//	  -bench BenchmarkPromptHookRelevant -benchtime 50x
func BenchmarkPromptHookRelevant(b *testing.B) {
	chunks := 200000
	if v := os.Getenv("GHOSTTREE_HOOK_BENCH_CHUNKS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			b.Fatalf("GHOSTTREE_HOOK_BENCH_CHUNKS=%q", v)
		}
		chunks = n
	}
	path := filepath.Join(b.TempDir(), "bench.db")
	st, err := store.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { st.Close() })
	token, err := st.AddPerson("bench")
	if err != nil {
		b.Fatal(err)
	}
	account := int64(1)
	if p, ok := st.AuthenticatePrincipal(token); ok {
		if id, err := strconv.ParseInt(strings.TrimPrefix(p.ID, "person:"), 10, 64); err == nil {
			account = id
		}
	}

	const vocabulary = 120000
	words := make([]string, vocabulary)
	for i := range words {
		words[i] = fmt.Sprintf("v%06d", i)
	}
	rng := rand.New(rand.NewSource(1))
	zipf := rand.NewZipf(rng, 1.07, 1, vocabulary-1)
	const sessions, batch = 300, 500
	ids := make([]int64, sessions)
	for i := range ids {
		if ids[i], err = st.UpsertSession(store.Session{Harness: "codex", ExternalID: fmt.Sprintf("bench-%d", i), AccountID: account, StartedAt: "2026-08-23T00:00:00Z"}); err != nil {
			b.Fatal(err)
		}
	}
	seq := make([]int, sessions)
	for written := 0; written < chunks; {
		i := rng.Intn(sessions)
		var list []store.Chunk
		for n := 0; n < batch && written < chunks; n++ {
			length := 8 + rng.Intn(40)
			var sb strings.Builder
			for w := 0; w < length; w++ {
				if w > 0 {
					sb.WriteByte(' ')
				}
				sb.WriteString(words[zipf.Uint64()])
			}
			text := sb.String()
			list = append(list, store.Chunk{Seq: seq[i], Role: "user", Text: text, Raw: promptRaw("codex", text)})
			seq[i]++
			written++
		}
		if err := st.AppendChunks(ids[i], list); err != nil {
			b.Fatal(err)
		}
	}
	srv := httptest.NewServer(New(st))
	b.Cleanup(srv.Close)

	// Words from the middle of the ranking: in no entry, in some sessions.
	mid := func(from, n int) string { return strings.Join(words[from:from+n], " ") }
	prompts := []struct{ name, text string }{
		{"plain_prompt", "kannst du das bitte noch einmal prüfen " + mid(2000, 4)},
		{"codename_statement", "bitte prüf hlpvpngw " + mid(9000, 2)},
		{"codename_question", "was ist hlpvpngw " + mid(9000, 2)},
		{"asks_earlier_work", "was hatten wir damals zu " + mid(2000, 4) + " gesagt"},
	}
	client := srv.Client()
	for _, p := range prompts {
		b.Run(p.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				q := url.Values{"q": {p.text}, "session": {"bench-" + strconv.Itoa(i)}}
				req, _ := http.NewRequest("GET", srv.URL+"/api/context/relevant?"+q.Encode(), nil)
				req.Header.Set("Authorization", "Bearer "+token)
				resp, err := client.Do(req)
				if err != nil {
					b.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != 200 {
					b.Fatalf("status %d", resp.StatusCode)
				}
			}
		})
	}
}
