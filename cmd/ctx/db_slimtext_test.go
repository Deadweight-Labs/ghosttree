package main

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/chunktext"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestDBSlimTextCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "slim-text"})
	if err != nil {
		t.Fatal(err)
	}
	raw := promptRaw("claude-code", "kolibri im garten")
	_, text := chunktext.Derive("claude-code", raw)
	if err := s.AppendChunks(id, []store.Chunk{{Seq: 0, Role: "user", Text: text, Raw: raw}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := cmdDB([]string{"slim-text", "--db", path, "--dry-run"}, &out); code != 0 {
		t.Fatalf("dry run: code %d\n%s", code, out.String())
	}
	for _, want := range []string{"dry run: nothing is written", "rows with text: 1", "derived equal to stored: 1", "truncation mark involved: 0"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry run output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "kolibri") {
		t.Error("the report must carry numbers, no text")
	}
	out.Reset()
	if code := cmdDB([]string{"slim-text", "--db", path, "--pause", "1ms"}, &out); code != 0 {
		t.Fatalf("code %d\n%s", code, out.String())
	}
	for _, want := range []string{"text emptied in 1 rows (17 characters kept in text_chars)", "write lock per batch", "ctx db vacuum --db", "--restore"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if code := cmdDB([]string{"slim-text", "--db", path, "--json"}, &out); code != 0 || !strings.Contains(out.String(), `"emptied": 0`) {
		t.Fatalf("second run: code %d\n%s", code, out.String())
	}
	out.Reset()
	if code := cmdDB([]string{"slim-text", "--db", path, "--restore", "--pause", "1ms"}, &out); code != 0 || !strings.Contains(out.String(), "text written back: 1 rows") {
		t.Fatalf("restore: code %d\n%s", code, out.String())
	}
	for _, args := range [][]string{{"slim-text"}, {"slim-text", "--db", path, "--batch", "0"}, {"slim-text", "--db", path, "extra"}} {
		out.Reset()
		if code := cmdDB(args, &out); code != 2 {
			t.Errorf("%v: code %d, want 2", args, code)
		}
	}
}

func TestServeSessionTextFlag(t *testing.T) {
	t.Setenv(envSessionText, "")
	cfg, err := parseServeConfig([]string{"--db", "x.db"}, io.Discard)
	if err != nil || cfg.SessionText != store.SessionTextStored {
		t.Fatalf("default: %q, %v", cfg.SessionText, err)
	}
	cfg, err = parseServeConfig([]string{"--db", "x.db", "--session-text", "derived"}, io.Discard)
	if err != nil || cfg.SessionText != store.SessionTextDerived {
		t.Fatalf("derived: %q, %v", cfg.SessionText, err)
	}
	t.Setenv(envSessionText, "derived")
	if cfg, err = parseServeConfig([]string{"--db", "x.db"}, io.Discard); err != nil || cfg.SessionText != store.SessionTextDerived {
		t.Fatalf("env: %q, %v", cfg.SessionText, err)
	}
	if _, err := parseServeConfig([]string{"--db", "x.db", "--session-text", "sometimes"}, io.Discard); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
}
