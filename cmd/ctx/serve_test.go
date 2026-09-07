package main

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	_ "modernc.org/sqlite"
)

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
