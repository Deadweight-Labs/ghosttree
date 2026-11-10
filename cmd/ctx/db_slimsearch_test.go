package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestDBSlimSearchCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "slim-search"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChunks(id, []store.Chunk{{Seq: 0, Role: "user", Text: "kolibri", Raw: promptRaw("claude-code", "kolibri im garten")}}); err != nil {
		t.Fatal(err)
	}
	keepChunksFTS(t, s.DB())
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := cmdDB([]string{"slim-search", "--db", path, "--dry-run"}, &out); code != 0 {
		t.Fatalf("dry run: code %d\n%s", code, out.String())
	}
	for _, want := range []string{"dry run: nothing is written", "chunk_index: 1 rows, 0 would be moved", "chunks_fts: 1 entries would be emptied"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry run output lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if code := cmdDB([]string{"slim-search", "--db", path, "--pause", "1ms"}, &out); code != 0 {
		t.Fatalf("code %d\n%s", code, out.String())
	}
	for _, want := range []string{"sess_fts: emptied", "chunks_fts: trigger dropped, 1 entries emptied", "ctx db vacuum --db"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if code := cmdDB([]string{"slim-search", "--db", path, "--json"}, &out); code != 0 || !strings.Contains(out.String(), `"moved": 0`) {
		t.Fatalf("second run: code %d\n%s", code, out.String())
	}
	for _, args := range [][]string{{"slim-search"}, {"slim-search", "--db", path, "--batch", "0"}, {"slim-search", "--db", path, "extra"}} {
		out.Reset()
		if code := cmdDB(args, &out); code != 2 {
			t.Errorf("%v: code %d, want 2", args, code)
		}
	}
}
