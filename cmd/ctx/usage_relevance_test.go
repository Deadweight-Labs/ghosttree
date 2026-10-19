package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The replay says which word fired each entry, so a threshold can be judged by
// looking at what it let through and why.
func TestUsageRelevanceReplayNamesTheWordBehindEachHit(t *testing.T) {
	db := filepath.Join(t.TempDir(), "ghosttree.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: "Ollama-Inventar", Body: "qwen3.8 und gemma4"}); err != nil {
		t.Fatal(err)
	}
	id, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "s1", Scope: scope.Axes{Project: "github.com/x/y"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendChunks(id, []store.Chunk{{Seq: 0, Role: "user", Text: "wie ist der stand von ollama", Raw: "{}"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := cmdUsage([]string{"--db", db, "--relevance", "--sample", "10"}, &out); code != 0 {
		t.Fatalf("exit = %d\n%s", code, out.String())
	}
	for _, want := range []string{"1 archived prompts replayed, 1 delivered something", "Ollama-Inventar", `(triggered by "ollama")`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("replay output lacks %q:\n%s", want, out.String())
		}
	}
}
