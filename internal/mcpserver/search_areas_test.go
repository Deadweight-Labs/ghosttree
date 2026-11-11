package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A search over every area answers with what it found when one area fails: the
// requests area here hangs past its budget or gets a proxy's error page, and the
// knowledge hits still come back, with one short line that names the missing area
// and nothing of the error page. The default search does not ask the sessions
// (decision #4447); asked for alone, a failing sessions area is the answer.
func TestContextSearchKeepsHitsWhenOneAreaFails(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("alice")
	real := httptest.NewServer(server.New(st))
	t.Cleanup(real.Close)
	target, _ := url.Parse(real.URL)
	var mode atomic.Value
	var sessionCalls atomic.Int32
	mode.Store("hang")
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind := r.URL.Query().Get("kind")
		if r.URL.Path == "/api/search" && kind == "sessions" {
			sessionCalls.Add(1)
		}
		if r.URL.Path == "/api/search" && (kind == "requests" || kind == "sessions") {
			switch mode.Load().(string) {
			case "hang":
				<-r.Context().Done()
				return
			case "502":
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte("<html><head><title>502 Bad Gateway</title></head><body>nginx</body></html>"))
				return
			}
		}
		httputil.NewSingleHostReverseProxy(target).ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)

	project := "github.com/x/y"
	c := client.New(config.Config{ServerURL: proxy.URL, Token: token, Machine: "workstation-a"})
	s := &Server{client: c, ctxAxes: scope.Axes{Project: project}, searchBudget: 300 * time.Millisecond}
	if _, _, err := s.handleRemember(context.Background(), nil, RememberInput{Type: "pitfall", Title: "quorvex braucht Neustart", Body: "nach dem Update", ScopeHint: "project"}); err != nil {
		t.Fatal(err)
	}

	for _, m := range []string{"hang", "502"} {
		mode.Store(m)
		start := time.Now()
		res, _, err := s.handleSearch(context.Background(), nil, SearchInput{Query: "quorvex"})
		if err != nil {
			t.Fatalf("%s: the whole search failed: %v", m, err)
		}
		got := text(t, res)
		if !strings.Contains(got, "## knowledge") || !strings.Contains(got, "quorvex braucht Neustart") {
			t.Errorf("%s: knowledge hits lost:\n%s", m, got)
		}
		if !strings.Contains(got, "(incomplete — requests: ") {
			t.Errorf("%s: no note about the missing area:\n%s", m, got)
		}
		if strings.Contains(got, "nginx") || strings.Contains(got, "http://") && strings.Contains(got, "/api/search") {
			t.Errorf("%s: the note carries the error page or the request URL:\n%s", m, got)
		}
		if m == "hang" {
			if !strings.Contains(got, "requests: timed out") {
				t.Errorf("hang: want timed out:\n%s", got)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("hang: the search waited %v, not the budget", d)
			}
		}
		if m == "502" && !strings.Contains(got, "requests: server answered 502") {
			t.Errorf("502: want the status:\n%s", got)
		}
	}

	if n := sessionCalls.Load(); n != 0 {
		t.Errorf("the default search asked the sessions %d times", n)
	}
	// Asked for sessions alone, the failure is the answer.
	mode.Store("502")
	if _, _, err := s.handleSearch(context.Background(), nil, SearchInput{Query: "quorvex", Kind: "sessions"}); err == nil {
		t.Error("kind=sessions: a failed area alone must be an error")
	}
}

func TestRenderGapsIsOneShortLine(t *testing.T) {
	got := renderGaps([]store.SearchGap{{Area: "sessions", Reason: store.GapTimeout}, {Area: "files", Reason: "server answered 502"}})
	if strings.Count(got, "\n") != 1 || !strings.Contains(got, "sessions: timed out; files: server answered 502") {
		t.Errorf("%q", got)
	}
	if renderGaps(nil) != "" {
		t.Error("no gaps, no line")
	}
}
