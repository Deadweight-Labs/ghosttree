package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestMetricsExposeBoundedHTTPAndStoreValues(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	m := newMetricsRegistry(st, "test-version")
	m.begin()
	m.observe(requestObservation{
		Method:        http.MethodGet,
		Route:         "/api/knowledge/{id}",
		Status:        http.StatusOK,
		Duration:      12 * time.Millisecond,
		RequestBytes:  7,
		ResponseBytes: 11,
	})
	m.begin()
	m.observe(requestObservation{
		Method:     http.MethodPost,
		Route:      "",
		Status:     http.StatusUnauthorized,
		Duration:   2 * time.Millisecond,
		ErrorClass: "auth",
	})

	rr := httptest.NewRecorder()
	m.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if got := rr.Code; got != http.StatusOK {
		t.Fatalf("status = %d, want %d", got, http.StatusOK)
	}
	if got := rr.Header().Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	body := rr.Body.String()
	for _, want := range []string{
		`ghosttree_http_requests_total{method="GET",route="/api/knowledge/{id}",status="200"} 1`,
		`ghosttree_http_requests_total{method="POST",route="unmatched",status="401"} 1`,
		`ghosttree_http_requests_in_flight 0`,
		`ghosttree_http_request_bytes_total{method="GET",route="/api/knowledge/{id}"} 7`,
		`ghosttree_http_response_bytes_total{method="GET",route="/api/knowledge/{id}",status="200"} 11`,
		`ghosttree_http_request_duration_seconds_bucket{method="GET",route="/api/knowledge/{id}",le="0.025"} 1`,
		`ghosttree_http_request_duration_seconds_bucket{method="GET",route="/api/knowledge/{id}",le="+Inf"} 1`,
		`ghosttree_http_request_duration_seconds_count{method="GET",route="/api/knowledge/{id}"} 1`,
		`ghosttree_http_request_duration_seconds_sum{method="GET",route="/api/knowledge/{id}"} 0.012`,
		`ghosttree_http_errors_total{class="auth"} 1`,
		`ghosttree_db_max_open_connections 1`,
		`ghosttree_db_open_connections 1`,
		`ghosttree_db_in_use_connections 0`,
		`ghosttree_db_idle_connections 1`,
		`ghosttree_db_wait_count_total 0`,
		`ghosttree_db_wait_duration_seconds_total 0`,
		`ghosttree_sqlite_file_bytes{kind="database"} 0`,
		`ghosttree_sqlite_file_bytes{kind="wal"} 0`,
		`ghosttree_sqlite_file_bytes{kind="shm"} 0`,
		`ghosttree_build_info{version="test-version"} 1`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %q:\n%s", want, body)
		}
	}
}

func TestMetricsExposeDeterministicSortedOutput(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m := newMetricsRegistry(st, "v\"1\\next\nline")

	for _, observation := range []requestObservation{
		{Method: http.MethodPost, Route: "/z", Status: http.StatusCreated},
		{Method: http.MethodGet, Route: "/a", Status: http.StatusOK},
	} {
		m.begin()
		m.observe(observation)
	}

	first := httptest.NewRecorder()
	m.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	second := httptest.NewRecorder()
	m.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if first.Body.String() != second.Body.String() {
		t.Fatalf("successive scrapes differ:\nfirst:\n%s\nsecond:\n%s", first.Body.String(), second.Body.String())
	}
	body := first.Body.String()
	get := strings.Index(body, `ghosttree_http_requests_total{method="GET",route="/a",status="200"}`)
	post := strings.Index(body, `ghosttree_http_requests_total{method="POST",route="/z",status="201"}`)
	if get < 0 || post < 0 || get >= post {
		t.Fatalf("request series are not sorted:\n%s", body)
	}
	if !strings.Contains(body, `ghosttree_build_info{version="v\"1\\next\nline"} 1`) {
		t.Fatalf("build version is not Prometheus-escaped:\n%s", body)
	}
}

func TestMetricsBoundErrorClasses(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m := newMetricsRegistry(st, "test")

	for _, class := range []string{"auth", "not_found", "client", "server", "sqlite_busy", "attacker-controlled"} {
		m.begin()
		m.observe(requestObservation{Method: http.MethodGet, Route: "/api/test", Status: 500, ErrorClass: class})
	}
	m.begin()
	m.observe(requestObservation{Method: "ATTACKER-METHOD", Route: "/api/test", Status: 200})

	rr := httptest.NewRecorder()
	m.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rr.Body.String()
	if strings.Contains(body, "attacker-controlled") {
		t.Fatalf("unbounded error class leaked into labels:\n%s", body)
	}
	if strings.Contains(body, "ATTACKER-METHOD") || !strings.Contains(body, `ghosttree_http_requests_total{method="OTHER",route="/api/test",status="200"} 1`) {
		t.Fatalf("HTTP method was not bounded:\n%s", body)
	}
	for _, class := range []string{"auth", "not_found", "client", "server", "sqlite_busy"} {
		if !strings.Contains(body, `ghosttree_http_errors_total{class="`+class+`"} 1`) {
			t.Errorf("missing bounded class %q:\n%s", class, body)
		}
	}
}

func TestMetricsConcurrentObserve(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m := newMetricsRegistry(st, "test")

	const observations = 100
	var wg sync.WaitGroup
	wg.Add(observations)
	for i := range observations {
		go func() {
			defer wg.Done()
			m.begin()
			m.observe(requestObservation{
				Method:        http.MethodGet,
				Route:         "/api/knowledge/{id}",
				Status:        http.StatusOK,
				Duration:      time.Duration(i+1) * time.Millisecond,
				RequestBytes:  1,
				ResponseBytes: 2,
			})
		}()
	}
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rr := httptest.NewRecorder()
			m.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			if rr.Code != http.StatusOK {
				t.Errorf("concurrent scrape status = %d", rr.Code)
			}
		}()
	}
	wg.Wait()

	rr := httptest.NewRecorder()
	m.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rr.Body.String()
	for _, want := range []string{
		`ghosttree_http_requests_total{method="GET",route="/api/knowledge/{id}",status="200"} ` + strconv.Itoa(observations),
		`ghosttree_http_requests_in_flight 0`,
		`ghosttree_http_request_duration_seconds_count{method="GET",route="/api/knowledge/{id}"} ` + strconv.Itoa(observations),
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %q:\n%s", want, body)
		}
	}
}
