package main

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A database that a server of the release before has migrated has the tables of
// knowledge and none of the other kinds. The status says so for those kinds,
// keeps what it knows about knowledge, and does not fail as a whole.
func TestEmbedStatusOnADatabaseWithoutTheTablesOfTheOtherKinds(t *testing.T) {
	k := newKindsDB(t, true)
	st, err := store.Open(k.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"embeddings", "embedding_failures"} {
		if _, err := st.DB().Exec(`DROP TABLE ` + table); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	code, out := runCtx("embed", "status", "--db", k.path)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"active entries   1", "embedded         1", "due              0", "background job"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, kind := range []string{store.KindRequest, store.KindPain, store.KindDocument, store.KindGhost} {
		if want := fmt.Sprintf("%-16s tables not created yet; start the server once with this version", kind); !strings.Contains(out, want) {
			t.Errorf("output lacks the line of %s:\n%s", kind, out)
		}
	}
	if strings.Contains(out, "no such table") {
		t.Fatalf("a raw database error reached the output:\n%s", out)
	}

	// With the kinds named, the line still says what that configuration would do.
	code, out = runCtx("embed", "status", "--db", k.path, "--embed-kinds", "knowledge,request", "--embed-search-kinds", "request")
	if code != 0 || !strings.Contains(out, "tables not created yet; start the server once with this version  [embedding on, search on]") {
		t.Fatalf("exit %d:\n%s", code, out)
	}

	// The measurement of such a kind needs the vectors' table to count them: it
	// says what to do, as it does for knowledge.
	evalProgress = io.Discard
	set := writeSet(t, fmt.Sprintf(`{"query": "x", "kind": "request", "target": "%d"}`, k.request))
	code, out = runCtx("eval", "search", "--db", k.path, "--set", set, "--embed-url", k.srv.URL, "--pause", "0")
	if code != 1 || !strings.Contains(out, "no embedding tables yet") || strings.Contains(out, "no such table") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}
