package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// fakeGhostServer beantwortet genau die zwei Anfragen, die WriteTree stellt.
// Ein echter Store dahinter würde nichts zusätzlich prüfen: was hier zählt, ist
// was auf Platte landet, nicht wie die Antwort zustande kam.
func fakeGhostServer(t *testing.T, described map[string]string) (*client.Client, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/health":
			w.Write([]byte(`{"ok":true}`))
		case "/api/ghosts/tree":
			out := []store.GhostFile{}
			for path, desc := range described {
				out = append(out, store.GhostFile{Project: r.URL.Query().Get("project"), Path: path,
					Kind: "file", Description: desc, DescribedAt: "2026-08-24T10:00:00Z", Person: "robin"})
			}
			json.NewEncoder(w).Encode(out)
		default:
			http.NotFound(w, r)
		}
	}))
	return client.New(config.Config{ServerURL: srv.URL, Token: "t"}), srv.Close
}

func TestWriteTreeMirrorsTheRepoAndExcludesItself(t *testing.T) {
	repo := newRepo(t)
	c, stop := fakeGhostServer(t, map[string]string{
		"internal/store/knowledge.go": "Lese- und Schreibpfade für Wissenseinträge",
	})
	defer stop()

	if err := WriteTree(c, "p", repo, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	described, err := os.ReadFile(filepath.Join(repo, ".ghosttree", "tree", "internal", "store", "knowledge.go.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(described), "Lese- und Schreibpfade") {
		t.Fatalf("described file lost its text: %q", described)
	}
	undescribed, err := os.ReadFile(filepath.Join(repo, ".ghosttree", "tree", "internal", "store", "store.go.md"))
	if err != nil {
		t.Fatalf("an undescribed file must still appear in the tree: %v", err)
	}
	if !strings.Contains(string(undescribed), "(keine Beschreibung)") {
		t.Fatalf("undescribed file must say so: %q", undescribed)
	}
	exclude, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(exclude), ".ghosttree/tree/") {
		t.Fatal("the tree must be excluded, or it shows up in git status")
	}

	// Und der Ausschluss wirkt wirklich.
	out, err := exec.Command("git", "-C", repo, "status", "--short", "--untracked-files=all").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), ".ghosttree/tree") {
		t.Fatalf("the tree still shows in git status:\n%s", out)
	}
}

// Ohne Repo gibt es keinen Baum und keine Frage danach. Ein Aufruf mit leerem
// Projekt würde den Ast des Projekts "" holen und `git ls-files` im
// Arbeitsverzeichnis des Prozesses laufen lassen — beides falsch, und beim
// Session-Start ausserhalb eines Repos ist genau das der Normalfall.
func TestWriteTreeStaysSilentWithoutARepository(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := client.New(config.Config{ServerURL: srv.URL, Token: "t"})

	if err := WriteTree(c, "", "", t.TempDir()); err != nil {
		t.Fatalf("no repository is not an error: %v", err)
	}
	if called {
		t.Fatal("a session outside a repository must not ask the server for a tree")
	}
}
