package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestHookBudgetWith308DistinctStoredDescriptions(t *testing.T) {
	repo := newRepo(t)
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	token, err := st.AddPerson("test")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.New(st))
	defer srv.Close()
	withConfig(t, srv.URL)
	cfg := config.Config{ServerURL: srv.URL, Token: token, Machine: "testbox"}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	description := strings.Repeat("a", 390)
	total, notices := 0, 0
	for i := range 308 {
		path := fmt.Sprintf("path%d.go", i)
		if err := os.WriteFile(filepath.Join(repo, path), []byte("package test\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := st.PutGhostFile(store.GhostFile{Project: "github.com/test/repo", Path: path, Description: description}); err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(map[string]any{"session_id": "full-tree-session", "cwd": repo,
			"tool_name": "Read", "tool_input": map[string]string{"file_path": path}})
		var out bytes.Buffer
		if code := cmdHookWith(bytes.NewReader(payload), []string{"pre-tool-use"}, &out); code != 0 {
			t.Fatal(code)
		}
		_, text := decodeHook(t, &out)
		total += utf8.RuneCountInString(text)
		notices += strings.Count(text, "Session hook context budget reached")
	}
	if total != 24000 || notices != 1 {
		t.Fatalf("308 actual descriptions: chars=%d notices=%d", total, notices)
	}
	full, err := client.New(cfg).GhostTree("github.com/test/repo", "path307.go")
	if err != nil || len(full) != 1 || full[0].Description != description {
		t.Fatalf("explicit full retrieval after cutoff: %v %v", full, err)
	}
}

func TestHookBudgetSpansEventsRepositoriesAndManyPaths(t *testing.T) {
	repo := newRepo(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "ghost") {
			json.NewEncoder(w).Encode([]map[string]string{{"path": "internal/store/store.go", "kind": "file", "description": strings.Repeat("界", 390)}})
			return
		}
		w.Write([]byte(strings.Repeat("ä", 390)))
	}))
	defer srv.Close()
	withConfig(t, srv.URL)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	total, notices := 0, 0
	for i := range 308 {
		event := []string{"session-start", "user-prompt-submit", "pre-tool-use"}[i%3]
		cwd := "/tmp"
		if event == "pre-tool-use" {
			cwd = repo
		}
		payload, _ := json.Marshal(map[string]any{"session_id": "budget-session", "cwd": cwd,
			"prompt": "knowledge", "tool_name": "Read", "tool_input": map[string]string{"file_path": "internal/store/store.go"}})
		var out bytes.Buffer
		if code := cmdHookWith(bytes.NewReader(payload), []string{event}, &out); code != 0 {
			t.Fatalf("hook %d exited %d", i, code)
		}
		_, delivered := decodeHook(t, &out)
		if !utf8.ValidString(delivered) {
			t.Fatal("invalid Unicode output")
		}
		total += utf8.RuneCountInString(delivered)
		notices += strings.Count(delivered, "Session hook context budget reached")
	}
	if total > 24000 || total == 0 || notices != 1 {
		t.Fatalf("308 hook outputs: %d characters, %d notices; want 1..24000 and one notice", total, notices)
	}
}

func TestHookWithoutSessionCannotEmitUnbudgetedContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("unbudgeted knowledge"))
	}))
	defer srv.Close()
	withConfig(t, srv.URL)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var out bytes.Buffer
	if code := cmdHookWith(strings.NewReader(`{"cwd":"/tmp","prompt":"anything"}`), []string{"user-prompt-submit"}, &out); code != 0 {
		t.Fatal(code)
	}
	if _, text := decodeHook(t, &out); text != "" {
		t.Fatalf("missing session emitted %q", text)
	}
}
