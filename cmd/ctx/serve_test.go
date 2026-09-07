package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	_ "modernc.org/sqlite"
)

func TestServerRootExposesMetricsOutsideAPIAndIncludesVersion(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := buildServerHandler(st, serveConfig{SnapshotLimits: snapshot.DefaultLimits()}, io.Discard)
	rr := httptest.NewRecorder()
	root.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `ghosttree_build_info{version="`+version+`"} 1`) {
		t.Fatalf("body = %s", rr.Body.String())
	}
}

func TestServerConcurrentTelemetrySmoke(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ghosttree.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	token, err := st.AddPerson("smoke-actor")
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	srv := httptest.NewServer(buildServerHandler(st, serveConfig{SnapshotLimits: snapshot.DefaultLimits()}, &logs))
	t.Cleanup(srv.Close)

	const pairs = 8
	const secret = "SMOKE_BODY_SECRET"
	var wg sync.WaitGroup
	errs := make(chan error, pairs*2)
	for i := range pairs {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			body, err := json.Marshal(map[string]any{
				"project": "smoke", "path": "file-" + strconv.Itoa(i), "kind": "file",
				"description": secret,
			})
			if err != nil {
				errs <- err
				return
			}
			req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/ghosts", bytes.NewReader(body))
			if err != nil {
				errs <- err
				return
			}
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := srv.Client().Do(req)
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errs <- fmt.Errorf("write %d status = %d", i, resp.StatusCode)
			}
		}(i)
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/whoami", nil)
			if err != nil {
				errs <- err
				return
			}
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := srv.Client().Do(req)
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errs <- fmt.Errorf("read status = %d", resp.StatusCode)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if t.Failed() {
		return
	}

	metricsResponse, err := srv.Client().Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer metricsResponse.Body.Close()
	metricsBody, err := io.ReadAll(metricsResponse.Body)
	if err != nil {
		t.Fatal(err)
	}
	if metricsResponse.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d", metricsResponse.StatusCode)
	}
	if got := sumPrometheusMetric(t, string(metricsBody), "ghosttree_http_requests_total"); got != pairs*2 {
		t.Fatalf("request total = %d, want %d\n%s", got, pairs*2, metricsBody)
	}
	for _, name := range []string{"ghosttree_db_wait_count_total", "ghosttree_db_wait_duration_seconds_total"} {
		if _, ok := prometheusScalar(t, string(metricsBody), name); !ok {
			t.Fatalf("missing numeric metric %s\n%s", name, metricsBody)
		}
	}

	lines := bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n"))
	if len(lines) != pairs*2 {
		t.Fatalf("audit lines = %d, want %d\n%s", len(lines), pairs*2, logs.String())
	}
	if bytes.Contains(logs.Bytes(), []byte(secret)) {
		t.Fatalf("audit log leaked request body marker: %s", logs.String())
	}
	for _, line := range lines {
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("invalid JSON audit line %q: %v", line, err)
		}
		if event["event"] != "http_request" {
			t.Fatalf("unexpected audit event: %#v", event)
		}
	}
}

func sumPrometheusMetric(t *testing.T, body, name string) uint64 {
	t.Helper()
	var total uint64
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name+"{") && !strings.HasPrefix(line, name+" ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("invalid metric line %q", line)
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			t.Fatalf("invalid counter %q: %v", line, err)
		}
		total += value
	}
	return total
}

func prometheusScalar(t *testing.T, body, name string) (float64, bool) {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name+" ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("invalid metric line %q", line)
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			t.Fatalf("invalid scalar %q: %v", line, err)
		}
		return value, true
	}
	return 0, false
}

func TestServeSnapshotLimitsUseExactFiniteDefaults(t *testing.T) {
	cfg, err := parseServeConfig(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.SnapshotLimits, snapshot.DefaultLimits(); got != want {
		t.Fatalf("snapshot limits = %#v, want %#v", got, want)
	}
}

func TestServeSnapshotLimitsAcceptExplicitFiniteValues(t *testing.T) {
	args := []string{
		"--snapshot-max-entry-bytes", "1",
		"--snapshot-max-entries", "2",
		"--snapshot-max-payload-bytes", "3",
		"--snapshot-max-head-bytes", "4",
		"--snapshot-max-logical-bytes", "5",
		"--snapshot-max-project-count", "6",
		"--snapshot-max-project-bytes", "7",
		"--snapshot-max-store-count", "8",
		"--snapshot-max-store-bytes", "9",
	}
	cfg, err := parseServeConfig(args, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.SnapshotLimits
	want := snapshot.DefaultLimits()
	want.MaxEntryPayloadBytes = 1
	want.MaxEntriesPerSnapshot = 2
	want.MaxSnapshotPayloadBytes = 3
	want.MaxCanonicalHeadBytes = 4
	want.MaxSnapshotLogicalBytes = 5
	want.MaxSnapshotsPerProject = 6
	want.MaxProjectLogicalBytes = 7
	want.MaxSnapshotsPerStore = 8
	want.MaxStoreLogicalBytes = 9
	if got != want {
		t.Fatalf("snapshot limits = %#v, want %#v", got, want)
	}
}

func TestServeSnapshotLimitsRejectZeroNegativeAndUnboundedValues(t *testing.T) {
	flags := []string{
		"--snapshot-max-entry-bytes",
		"--snapshot-max-entries",
		"--snapshot-max-payload-bytes",
		"--snapshot-max-head-bytes",
		"--snapshot-max-logical-bytes",
		"--snapshot-max-project-count",
		"--snapshot-max-project-bytes",
		"--snapshot-max-store-count",
		"--snapshot-max-store-bytes",
	}
	for _, flagName := range flags {
		for _, value := range []string{"0", "-1"} {
			t.Run(strings.TrimPrefix(flagName, "--")+"="+value, func(t *testing.T) {
				if _, err := parseServeConfig([]string{flagName, value}, &bytes.Buffer{}); err == nil {
					t.Fatalf("accepted %s %s", flagName, value)
				}
			})
		}
	}
}

func TestServeSnapshotRootsCanonicalizeProjects(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	cfg, err := parseServeConfig([]string{
		"--snapshot-root", "https://GitHub.com/Owner/One.git=" + first,
		"--snapshot-root", "github.com/owner/two=" + second,
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"github.com/owner/one": first,
		"github.com/owner/two": second,
	}
	if !reflect.DeepEqual(cfg.SnapshotRoots, want) {
		t.Fatalf("snapshot roots = %#v, want %#v", cfg.SnapshotRoots, want)
	}
}

func TestServeSnapshotRootsRejectDuplicateRelativeAndSymlinkRoots(t *testing.T) {
	realRoot := t.TempDir()
	symlinkRoot := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(realRoot, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"duplicate canonical project": {
			"--snapshot-root", "https://github.com/Owner/Repo.git=" + realRoot,
			"--snapshot-root", "github.com/owner/repo=" + t.TempDir(),
		},
		"relative root": {"--snapshot-root", "github.com/owner/repo=relative/repo"},
		"symlink root":  {"--snapshot-root", "github.com/owner/repo=" + symlinkRoot},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseServeConfig(args, &bytes.Buffer{}); err == nil {
				t.Fatalf("accepted args %v", args)
			}
		})
	}
}

func TestHTTPServerHasTimeouts(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", http.NewServeMux())
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Fatalf("server timeouts are incomplete: %+v", srv)
	}
}

func TestServeRejectsStaleSnapshotSchemaWithoutChangingCounts(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys=ON; PRAGMA recursive_triggers=ON`); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureContextSnapshotSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := serveSnapshotSchemaReady(context.Background(), db); err != nil {
		t.Fatalf("current schema rejected: %v", err)
	}
	if _, err := db.Exec(`DROP TRIGGER context_snapshot_entry_update`); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := db.QueryRow(`SELECT count(*) FROM context_snapshots`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := serveSnapshotSchemaReady(context.Background(), db); err == nil {
		t.Fatal("stale schema accepted")
	}
	var after int
	if err := db.QueryRow(`SELECT count(*) FROM context_snapshots`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("startup probe changed snapshot count: %d -> %d", before, after)
	}
}
