package main

import (
	"context"
	"database/sql"
	"net/http"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	_ "modernc.org/sqlite"
)

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
