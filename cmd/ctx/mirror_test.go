package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// mirrorServer antwortet auf genau die drei Abfragen, aus denen der Spiegel
// besteht — und merkt sich, ob jemand nach Sitzungsprotokollen gefragt hat.
func mirrorServer(t *testing.T, askedSessions *bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/sessions"):
			*askedSessions = true
			w.Write([]byte(`[]`))
		case r.URL.Path == "/api/knowledge" && r.URL.Query().Get("include_archived") == "1":
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 99, "type": "plan", "title": "docs/specs/thing.md", "body": "# Spec", "status": "archived", "observed_at": "2026-08-22T09:00:00Z"},
				{"id": 42, "type": "pitfall", "title": "Fallstrick", "body": "Text", "status": "active", "confidence": "trusted"},
			})
		case r.URL.Path == "/api/knowledge":
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 42, "type": "pitfall", "title": "Fallstrick", "body": "Text", "status": "active", "confidence": "trusted"},
			})
		case strings.HasPrefix(r.URL.Path, "/api/requests"):
			json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
				{"request": map[string]any{"id": 177, "type": "feature", "title": "Offener Faden", "description": "Text", "state": "open"}},
			}})
		default:
			w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func gitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return repo
}

func TestWriteMirrorPutsKnowledgeDocsAndLedgerOnDisk(t *testing.T) {
	asked := false
	srv := mirrorServer(t, &asked)
	withConfig(t, srv.URL)
	repo := gitRepo(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	if err := WriteMirror(client.New(cfg), scope.Axes{Project: "github.com/x/y"}, repo); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"INDEX.md",
		"knowledge/pitfall/42-fallstrick.md",
		"docs/2026-08-22-99-thing.md",
		"requests/open/REQ-177-offener-faden.md",
	} {
		if _, err := os.Stat(filepath.Join(repo, ".ghosttree", rel)); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}
	if asked {
		t.Error("the mirror must not even ask for session transcripts")
	}
}

// "Genau die Vereinigung, die eine Sitzung hier liest" heisst: mit Zweig und
// Abstammung. Ohne sie fehlt branch-gebundenes Wissen, und der Spiegel liest
// sich wie das Ganze, obwohl er ein Ausschnitt ist.
func TestMirrorAsksForTheSameScopeUnionASessionReads(t *testing.T) {
	var asked url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/knowledge" && r.URL.Query().Get("include_archived") != "1" {
			asked = r.URL.Query()
		}
		if strings.HasPrefix(r.URL.Path, "/api/requests") {
			w.Write([]byte(`{"results":[]}`))
			return
		}
		w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)
	withConfig(t, srv.URL)
	repo := gitRepo(t)
	cfg, _ := config.Load()

	ax := scope.Axes{Project: "github.com/x/y", Branch: "feature/mirror",
		Lineage: []string{"main", "feature/mirror"}, Machine: "testbox"}
	if err := WriteMirror(client.New(cfg), ax, repo); err != nil {
		t.Fatal(err)
	}
	if asked.Get("project") != "github.com/x/y" || asked.Get("branch") != "feature/mirror" {
		t.Fatalf("scope query = %v, want project and branch of the session", asked)
	}
	if asked.Get("machine") != "testbox" {
		t.Fatalf("machine = %q, want the machine whose knowledge the session reads", asked.Get("machine"))
	}
	if len(asked["lineage"]) == 0 {
		t.Fatalf("lineage was not passed on: %v", asked)
	}
}

// Der Ghost-Baum liegt im selben Verzeichnis und darf beim Schreiben des
// Spiegels nicht verschwinden — beide werden vollständig neu geschrieben, jeder
// für sich.
func TestWriteMirrorLeavesTheGhostTreeAlone(t *testing.T) {
	asked := false
	srv := mirrorServer(t, &asked)
	withConfig(t, srv.URL)
	repo := gitRepo(t)
	tree := filepath.Join(repo, ".ghosttree", "tree")
	os.MkdirAll(tree, 0o755)
	os.WriteFile(filepath.Join(tree, "__dir.md"), []byte("# Wurzel"), 0o644)

	cfg, _ := config.Load()
	if err := WriteMirror(client.New(cfg), scope.Axes{Project: "github.com/x/y"}, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tree, "__dir.md")); err != nil {
		t.Fatalf("the mirror wiped the ghost tree: %v", err)
	}
}

// Der Spiegel ist eine Projektion und gehört nicht in die Versionsverwaltung —
// und ebensowenig in den git status von jemandem, der ihn nicht erwartet.
func TestTheMirrorStaysOutOfGitStatus(t *testing.T) {
	asked := false
	srv := mirrorServer(t, &asked)
	withConfig(t, srv.URL)
	repo := gitRepo(t)
	cfg, _ := config.Load()
	if err := WriteMirror(client.New(cfg), scope.Axes{Project: "github.com/x/y"}, repo); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", repo, "status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), ".ghosttree") {
		t.Fatalf("the mirror shows up in git status:\n%s", out)
	}
}
