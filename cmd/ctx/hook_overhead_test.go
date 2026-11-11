package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// BenchmarkHookOverhead runs the three hooks that deliver context against a
// local server that answers at once, so that what is measured is the hook and
// not the network. A session id per iteration keeps the session budget from
// running dry. The state directory is on tmpfs when there is one, so that the
// receipt's two synced writes do not drown what the benchmark is for: the
// difference between two builds.
//
//	go test ./cmd/ctx -run '^$' -bench HookOverhead -benchtime 2000x -count 6
func BenchmarkHookOverhead(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/context/bootstrap":
			w.Header().Set("X-Ghosttree-Delivered", "bootstrap:knowledge:1:300,bootstrap:knowledge:2:300,bootstrap:knowledge:3:300,bootstrap:knowledge:4:300,bootstrap:knowledge:5:300,bootstrap:ledger:0:250,bootstrap:ledger:0:400")
			w.Write([]byte("## Known context (ghosttree)\n" + strings.Repeat("x", 2400)))
		case r.URL.Path == "/api/context/relevant":
			w.Header().Set("X-Ghosttree-Delivered", "relevance:knowledge:9:300,relevance:knowledge:10:300")
			w.Write([]byte("## Possibly relevant to what you just said (ghosttree)\n\n" + strings.Repeat("y", 600)))
		case strings.Contains(r.URL.Path, "ghosts"):
			json.NewEncoder(w).Encode([]map[string]string{
				{"path": "internal/store/store.go", "kind": "file", "description": strings.Repeat("what this file does. ", 20)},
				{"path": "internal/store", "kind": "dir", "description": strings.Repeat("what this directory does. ", 10)}})
		default:
			w.Write([]byte("[]"))
		}
	}))
	defer srv.Close()
	b.Setenv("HOME", b.TempDir())
	home := os.Getenv("HOME")
	b.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	state := b.TempDir()
	if info, err := os.Stat("/dev/shm"); err == nil && info.IsDir() {
		if d, err := os.MkdirTemp("/dev/shm", "hookbench"); err == nil {
			state = d
			b.Cleanup(func() { os.RemoveAll(d) })
		}
	}
	b.Setenv("XDG_STATE_HOME", state)
	cfgDir := filepath.Join(home, ".config", "ghosttree")
	os.MkdirAll(cfgDir, 0o755)
	os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{"server_url":"`+srv.URL+`","token":"t","machine":"testbox"}`), 0o600)

	repo := benchRepo(b)
	for _, ev := range []struct {
		event string
		extra map[string]any
	}{
		{"session-start", nil},
		{"user-prompt-submit", map[string]any{"prompt": "what about the store"}},
		{"pre-tool-use", map[string]any{"tool_name": "Read", "tool_input": map[string]string{"file_path": "internal/store/store.go"}}},
	} {
		b.Run(ev.event, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				payload := map[string]any{"session_id": fmt.Sprintf("bench-%s-%d", ev.event, i), "cwd": repo}
				for k, v := range ev.extra {
					payload[k] = v
				}
				raw, _ := json.Marshal(payload)
				var out bytes.Buffer
				cmdHookWith(bytes.NewReader(raw), []string{ev.event, "--harness", "claude"}, &out)
			}
		})
	}
}

func benchRepo(b *testing.B) string {
	dir := b.TempDir()
	run := func(args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			b.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	run("remote", "add", "origin", "https://github.com/test/repo.git")
	os.MkdirAll(filepath.Join(dir, "internal", "store"), 0o755)
	os.WriteFile(filepath.Join(dir, "internal/store/store.go"), []byte("package store\n"), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "x")
	return dir
}
