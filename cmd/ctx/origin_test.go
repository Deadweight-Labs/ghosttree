package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestOriginBackfillCommandIsIdempotentAndHasADryRun(t *testing.T) {
	db := filepath.Join(t.TempDir(), "g.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertSession(store.Session{Harness: "claude", ExternalID: "sess-cmd", Scope: scope.Axes{Project: "github.com/x/y"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordPathActivity([]store.PathActivity{{SessionExternalID: "sess-cmd", Checkout: "/w", Tool: "Edit",
		Path: "/w/internal/store/a.go", Writes: true, Quality: store.ActivityIntent, At: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: "alt", Body: "b", Person: "robin", SessionRef: "sess-cmd"}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if code := cmdOrigin(append([]string{"backfill", "--db", db}, args...), &out); code != 0 {
			t.Fatalf("exit %d: %s", code, out.String())
		}
		return out.String()
	}
	if out := run("--dry-run"); !strings.Contains(out, "dry run") || !strings.Contains(out, "would fill=1") {
		t.Fatalf("dry run: %s", out)
	}
	if out := run(); !strings.Contains(out, "filled=1") {
		t.Fatalf("first run: %s", out)
	}
	if out := run(); !strings.Contains(out, "filled=0") || strings.Contains(out, "filled=1") {
		t.Fatalf("second run must change nothing: %s", out)
	}
	var out bytes.Buffer
	if code := cmdOrigin([]string{"backfill"}, &out); code != 2 {
		t.Fatalf("without --db: exit %d", code)
	}
	if code := cmdOrigin([]string{"nope"}, &out); code != 2 {
		t.Fatalf("unknown subcommand: exit %d", code)
	}
}
