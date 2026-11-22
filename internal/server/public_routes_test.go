package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/version"
)

func publicRouteHandler(t *testing.T, opts ...Option) (http.Handler, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tok, _ := st.AddPerson("test")
	return New(st, opts...), tok
}

func fetch(h http.Handler, path, remote, forwarded, bearer string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.RemoteAddr = remote
	if forwarded != "" {
		r.Header.Set("X-Forwarded-For", forwarded)
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// /metrics is for the machine it runs on, or for whoever holds the metrics token;
// everyone else gets what an unknown address gets.
func TestMetricsAreForLoopbackOrTheMetricsToken(t *testing.T) {
	h, _ := publicRouteHandler(t, WithMetricsToken("scrape-secret-0123456789"))
	for name, tc := range map[string]struct {
		remote, forwarded, bearer string
		want                      int
	}{
		"loopback":                  {"127.0.0.1:5000", "", "", 200},
		"loopback, no forward":      {"[::1]:5000", "", "", 200},
		"public address":            {"203.0.113.9:5000", "", "", 404},
		"public client via proxy":   {"127.0.0.1:5000", "203.0.113.9", "", 404},
		"public with token":         {"203.0.113.9:5000", "", "scrape-secret-0123456789", 200},
		"via proxy with token":      {"127.0.0.1:5000", "203.0.113.9", "scrape-secret-0123456789", 200},
		"public with a wrong token": {"203.0.113.9:5000", "", "scrape-secret-wrong", 404},
		"a person's token is none":  {"203.0.113.9:5000", "", "person-token", 404},
	} {
		if got := fetch(h, "/metrics", tc.remote, tc.forwarded, tc.bearer).Code; got != tc.want {
			t.Errorf("%s: status %d, want %d", name, got, tc.want)
		}
	}
	// Without a token configured only loopback reads them, and a bearer means nothing.
	h2, tok := publicRouteHandler(t)
	if got := fetch(h2, "/metrics", "203.0.113.9:5000", "", tok).Code; got != 404 {
		t.Errorf("a person's token reads the metrics: %d", got)
	}
}

// /api/version stays open: a client reads it before it has a working token, the
// installer waits for the new version after an update (from the host of a
// container too), and /dist/ names the version in its file names anyway.
func TestVersionAnswersWithoutAToken(t *testing.T) {
	h, _ := publicRouteHandler(t, WithBuildVersion("0.1.0-dev+b0cd1b9"))
	var info version.Info
	w := fetch(h, "/api/version", "203.0.113.9:5000", "", "")
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Version != "0.1.0-dev+b0cd1b9" || info.Protocol != version.Protocol {
		t.Fatalf("info = %+v", info)
	}
}
