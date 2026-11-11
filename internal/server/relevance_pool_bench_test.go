package server

import (
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"
)

// BenchmarkHookReadUnderSearchLoad is what a prompt of the prompt hook waits for
// while other callers search (IDEA-22). The server reads through a pool of three
// connections that hooks, searches and the web interface share; a search that
// holds a connection for a long time makes the hook wait for one. The searchers
// run until the measurement is over; the figures are those of the hook's calls:
// p50, p95 and the slowest in microseconds.
//
//	GHOSTTREE_HOOK_BENCH_CHUNKS=310000 go test ./internal/server -run '^$' \
//	  -bench BenchmarkHookReadUnderSearchLoad -benchtime 200x
func BenchmarkHookReadUnderSearchLoad(b *testing.B) {
	srv, token, words := newHookArchive(b)
	client := &http.Client{Timeout: time.Minute, Transport: &http.Transport{MaxIdleConnsPerHost: 64}}
	get := func(path string, q url.Values) time.Duration {
		req, _ := http.NewRequest("GET", srv.URL+path+"?"+q.Encode(), nil)
		req.Header.Set("Authorization", "Bearer "+token)
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			b.Error(err)
			return 0
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return time.Since(start)
	}
	common := words[2] + " " + words[5]
	scenarios := []struct {
		name     string
		searches int
		kind     string
	}{
		{"idle", 0, ""},
		{"default_search_x12", 12, "all"},
		{"sessions_search_x3", 3, "sessions"},
		{"sessions_search_x12", 12, "sessions"},
	}
	for _, sc := range scenarios {
		b.Run(sc.name, func(b *testing.B) {
			stop := make(chan struct{})
			var wg sync.WaitGroup
			for range sc.searches {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						select {
						case <-stop:
							return
						default:
						}
						get("/api/search", url.Values{"q": {common}, "kind": {sc.kind}, "limit": {"20"}})
					}
				}()
			}
			time.Sleep(300 * time.Millisecond)
			lat := make([]time.Duration, 0, b.N)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				lat = append(lat, get("/api/context/relevant", url.Values{"q": {"kannst du das bitte prüfen"}, "session": {"pool-" + strconv.Itoa(i)}}))
			}
			b.StopTimer()
			close(stop)
			wg.Wait()
			slices.Sort(lat)
			us := func(d time.Duration) float64 { return float64(d.Microseconds()) }
			b.ReportMetric(us(lat[len(lat)/2]), "hook_p50_µs")
			b.ReportMetric(us(lat[len(lat)*95/100]), "hook_p95_µs")
			b.ReportMetric(us(lat[len(lat)-1]), "hook_max_µs")
		})
	}
}
