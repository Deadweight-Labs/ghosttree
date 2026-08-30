package server

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var durationBuckets = [...]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

type requestObservation struct {
	Method        string
	Route         string
	ErrorClass    string
	Status        int
	Duration      time.Duration
	RequestBytes  int64
	ResponseBytes int64
}

type requestMetricKey struct {
	Method string
	Route  string
	Status int
}

type methodRouteKey struct {
	Method string
	Route  string
}

type histogramValue struct {
	Buckets [len(durationBuckets)]uint64
	Count   uint64
	Sum     float64
}

type metricsRegistry struct {
	mu            sync.Mutex
	store         *store.Store
	version       string
	inFlight      int64
	requests      map[requestMetricKey]uint64
	requestBytes  map[methodRouteKey]uint64
	responseBytes map[requestMetricKey]uint64
	errors        map[string]uint64
	durations     map[methodRouteKey]histogramValue
}

type metricsSnapshot struct {
	inFlight      int64
	requests      map[requestMetricKey]uint64
	requestBytes  map[methodRouteKey]uint64
	responseBytes map[requestMetricKey]uint64
	errors        map[string]uint64
	durations     map[methodRouteKey]histogramValue
}

func newMetricsRegistry(st *store.Store, version string) *metricsRegistry {
	return &metricsRegistry{
		store:         st,
		version:       version,
		requests:      make(map[requestMetricKey]uint64),
		requestBytes:  make(map[methodRouteKey]uint64),
		responseBytes: make(map[requestMetricKey]uint64),
		errors:        make(map[string]uint64),
		durations:     make(map[methodRouteKey]histogramValue),
	}
}

func (m *metricsRegistry) begin() {
	m.mu.Lock()
	m.inFlight++
	m.mu.Unlock()
}

func (m *metricsRegistry) observe(observation requestObservation) {
	observation.Method = boundedMethod(observation.Method)
	if observation.Route == "" {
		observation.Route = "unmatched"
	}
	requestKey := requestMetricKey{
		Method: observation.Method,
		Route:  observation.Route,
		Status: observation.Status,
	}
	methodRoute := methodRouteKey{Method: observation.Method, Route: observation.Route}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inFlight > 0 {
		m.inFlight--
	}
	m.requests[requestKey]++
	m.requestBytes[methodRoute] += nonnegativeUint64(observation.RequestBytes)
	m.responseBytes[requestKey] += nonnegativeUint64(observation.ResponseBytes)
	if boundedErrorClass(observation.ErrorClass) {
		m.errors[observation.ErrorClass]++
	}
	histogram := m.durations[methodRoute]
	durationSeconds := max(0, observation.Duration.Seconds())
	for i, upperBound := range durationBuckets {
		if durationSeconds <= upperBound {
			histogram.Buckets[i]++
		}
	}
	histogram.Count++
	histogram.Sum += durationSeconds
	m.durations[methodRoute] = histogram
}

func (m *metricsRegistry) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	snapshot := m.snapshot()
	runtimeStats := m.store.RuntimeStats()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	writeMetricHeader(w, "ghosttree_http_requests_total", "Completed HTTP requests.", "counter")
	for _, key := range sortedRequestKeys(snapshot.requests) {
		fmt.Fprintf(w, "ghosttree_http_requests_total{method=\"%s\",route=\"%s\",status=\"%d\"} %d\n", escapeLabel(key.Method), escapeLabel(key.Route), key.Status, snapshot.requests[key])
	}

	writeMetricHeader(w, "ghosttree_http_requests_in_flight", "HTTP requests currently in flight.", "gauge")
	fmt.Fprintf(w, "ghosttree_http_requests_in_flight %d\n", snapshot.inFlight)

	writeMetricHeader(w, "ghosttree_http_request_bytes_total", "HTTP request body bytes read.", "counter")
	for _, key := range sortedMethodRouteKeys(snapshot.requestBytes) {
		fmt.Fprintf(w, "ghosttree_http_request_bytes_total{method=\"%s\",route=\"%s\"} %d\n", escapeLabel(key.Method), escapeLabel(key.Route), snapshot.requestBytes[key])
	}

	writeMetricHeader(w, "ghosttree_http_response_bytes_total", "HTTP response body bytes written.", "counter")
	for _, key := range sortedRequestKeys(snapshot.responseBytes) {
		fmt.Fprintf(w, "ghosttree_http_response_bytes_total{method=\"%s\",route=\"%s\",status=\"%d\"} %d\n", escapeLabel(key.Method), escapeLabel(key.Route), key.Status, snapshot.responseBytes[key])
	}

	writeMetricHeader(w, "ghosttree_http_request_duration_seconds", "HTTP request duration in seconds.", "histogram")
	for _, key := range sortedMethodRouteKeys(snapshot.durations) {
		histogram := snapshot.durations[key]
		for i, upperBound := range durationBuckets {
			fmt.Fprintf(w, "ghosttree_http_request_duration_seconds_bucket{method=\"%s\",route=\"%s\",le=\"%s\"} %d\n", escapeLabel(key.Method), escapeLabel(key.Route), formatFloat(upperBound), histogram.Buckets[i])
		}
		fmt.Fprintf(w, "ghosttree_http_request_duration_seconds_bucket{method=\"%s\",route=\"%s\",le=\"+Inf\"} %d\n", escapeLabel(key.Method), escapeLabel(key.Route), histogram.Count)
		fmt.Fprintf(w, "ghosttree_http_request_duration_seconds_sum{method=\"%s\",route=\"%s\"} %s\n", escapeLabel(key.Method), escapeLabel(key.Route), formatFloat(histogram.Sum))
		fmt.Fprintf(w, "ghosttree_http_request_duration_seconds_count{method=\"%s\",route=\"%s\"} %d\n", escapeLabel(key.Method), escapeLabel(key.Route), histogram.Count)
	}

	writeMetricHeader(w, "ghosttree_http_errors_total", "Completed HTTP request errors by bounded class.", "counter")
	for _, class := range sortedStringKeys(snapshot.errors) {
		fmt.Fprintf(w, "ghosttree_http_errors_total{class=\"%s\"} %d\n", escapeLabel(class), snapshot.errors[class])
	}

	writeDBMetrics(w, runtimeStats)
	writeMetricHeader(w, "ghosttree_build_info", "Ghosttree build information.", "gauge")
	fmt.Fprintf(w, "ghosttree_build_info{version=\"%s\"} 1\n", escapeLabel(m.version))
}

func (m *metricsRegistry) snapshot() metricsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return metricsSnapshot{
		inFlight:      m.inFlight,
		requests:      cloneMap(m.requests),
		requestBytes:  cloneMap(m.requestBytes),
		responseBytes: cloneMap(m.responseBytes),
		errors:        cloneMap(m.errors),
		durations:     cloneMap(m.durations),
	}
}

func writeDBMetrics(w http.ResponseWriter, stats store.RuntimeStats) {
	writeMetricHeader(w, "ghosttree_db_max_open_connections", "Maximum number of open database connections.", "gauge")
	fmt.Fprintf(w, "ghosttree_db_max_open_connections %d\n", stats.DB.MaxOpenConnections)
	writeMetricHeader(w, "ghosttree_db_open_connections", "Number of established database connections.", "gauge")
	fmt.Fprintf(w, "ghosttree_db_open_connections %d\n", stats.DB.OpenConnections)
	writeMetricHeader(w, "ghosttree_db_in_use_connections", "Number of database connections currently in use.", "gauge")
	fmt.Fprintf(w, "ghosttree_db_in_use_connections %d\n", stats.DB.InUse)
	writeMetricHeader(w, "ghosttree_db_idle_connections", "Number of idle database connections.", "gauge")
	fmt.Fprintf(w, "ghosttree_db_idle_connections %d\n", stats.DB.Idle)
	writeMetricHeader(w, "ghosttree_db_wait_count_total", "Number of waits for a database connection.", "counter")
	fmt.Fprintf(w, "ghosttree_db_wait_count_total %d\n", stats.DB.WaitCount)
	writeMetricHeader(w, "ghosttree_db_wait_duration_seconds_total", "Time spent waiting for database connections in seconds.", "counter")
	fmt.Fprintf(w, "ghosttree_db_wait_duration_seconds_total %s\n", formatFloat(stats.DB.WaitDuration.Seconds()))
	writeMetricHeader(w, "ghosttree_sqlite_file_bytes", "SQLite file size in bytes.", "gauge")
	fmt.Fprintf(w, "ghosttree_sqlite_file_bytes{kind=\"database\"} %d\n", stats.DatabaseBytes)
	fmt.Fprintf(w, "ghosttree_sqlite_file_bytes{kind=\"shm\"} %d\n", stats.SHMBytes)
	fmt.Fprintf(w, "ghosttree_sqlite_file_bytes{kind=\"wal\"} %d\n", stats.WALBytes)
}

func writeMetricHeader(w http.ResponseWriter, name, help, metricType string) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, metricType)
}

func sortedRequestKeys[V any](values map[requestMetricKey]V) []requestMetricKey {
	keys := make([]requestMetricKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Method != keys[j].Method {
			return keys[i].Method < keys[j].Method
		}
		if keys[i].Route != keys[j].Route {
			return keys[i].Route < keys[j].Route
		}
		return keys[i].Status < keys[j].Status
	})
	return keys
}

func sortedMethodRouteKeys[V any](values map[methodRouteKey]V) []methodRouteKey {
	keys := make([]methodRouteKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Method != keys[j].Method {
			return keys[i].Method < keys[j].Method
		}
		return keys[i].Route < keys[j].Route
	})
	return keys
}

func sortedStringKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneMap[K comparable, V any](source map[K]V) map[K]V {
	clone := make(map[K]V, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func boundedMethod(method string) string {
	switch method {
	case http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodOptions,
		http.MethodPatch, http.MethodPost, http.MethodPut, http.MethodTrace:
		return method
	default:
		return "OTHER"
	}
}

func boundedErrorClass(class string) bool {
	switch class {
	case "auth", "not_found", "client", "server", "sqlite_busy":
		return true
	default:
		return false
	}
}

func nonnegativeUint64(value int64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}

func escapeLabel(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"")
	return replacer.Replace(value)
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}
