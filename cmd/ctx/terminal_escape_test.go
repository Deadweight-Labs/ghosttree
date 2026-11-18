package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Text a server wrote must not steer the terminal of the person who reads it
// with ctx: no escape sequence reaches the output.
func TestKnowledgeShowStripsTerminalEscapes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(store.Knowledge{ID: 7, Type: "note", Title: "titel\x1b]0;owned\x07", Status: "active",
			Body: "Zeile eins\n\x1b[2J\x1b]52;c;ZXZpbA==\x07Zeile zwei äöü\n"})
	}))
	defer srv.Close()
	withConfig(t, srv.URL)
	var out bytes.Buffer
	if code := run([]string{"knowledge", "show", "7"}, &out); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	got := out.String()
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("a control character reached the terminal: %q", got)
	}
	if !strings.Contains(got, "Zeile eins") || !strings.Contains(got, "Zeile zwei äöü") {
		t.Fatalf("the text is gone: %q", got)
	}
}

func TestRawOutputIsKeptForProtocolsAndPrograms(t *testing.T) {
	for _, cmd := range []string{"hook", "mcp", "channel", "serve", "watch", "claude", "tui", "run"} {
		if !rawOutput(cmd, nil) {
			t.Errorf("%s must keep its output", cmd)
		}
	}
	if !rawOutput("garden", []string{"git-mcp"}) || rawOutput("garden", nil) || rawOutput("knowledge", []string{"show"}) {
		t.Error("garden/knowledge")
	}
}

// ctx idea/pain/request attach sends a file only when it is an image.
func TestReadImageArgRefusesAFileThatIsNoImage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(p, []byte("-----BEGIN OPENSSH " + "PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readImageArg(p); err == nil {
		t.Fatal("a key was read for an attachment")
	}
}
