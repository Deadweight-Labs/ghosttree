package collector

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// AC-4 von REQ-348 an einem ECHTEN Worktree-Paar: derselbe Pfad in zwei
// Worktrees desselben Repos ist ein anderes Risiko als derselbe Pfad im
// selben Checkout.
//
// Mit echtem git gebaut und nicht mit erfundenen Pfaden, weil die Frage genau
// daran hängt, was git als Arbeitsverzeichnis meldet — eine Annahme darüber
// wäre keine Prüfung.
func TestTwoRealWorktreesAreDifferentCheckoutsOfOneProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	main := filepath.Join(root, "repo")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	run(main, "init", "-q", "-b", "main")
	run(main, "remote", "add", "origin", "https://github.com/example/repo.git")
	if err := os.WriteFile(filepath.Join(main, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(main, "add", "a.go")
	run(main, "commit", "-qm", "erste")

	second := filepath.Join(root, "feat")
	run(main, "worktree", "add", "-q", "-b", "feat", second)

	mainProject, mainBranch := GitInfo(main)
	featProject, featBranch := GitInfo(second)

	// Dasselbe Projekt — beide gehören in denselben Raum.
	if mainProject == "" || mainProject != featProject {
		t.Fatalf("both worktrees belong to one project: %q vs %q", mainProject, featProject)
	}
	if mainBranch == featBranch {
		t.Fatalf("the worktrees should be on different branches, both on %q", mainBranch)
	}

	// Aber verschiedene Checkouts — und damit eine andere Konfliktlage.
	if got := store.ClassifyConflict(main, second); got != store.ConflictOtherWorktree {
		t.Fatalf("two worktrees must classify as %q, got %q", store.ConflictOtherWorktree, got)
	}
	if got := store.ClassifyConflict(main, main); got != store.ConflictSameCheckout {
		t.Fatalf("one checkout must classify as %q, got %q", store.ConflictSameCheckout, got)
	}

	// Und derselbe relative Pfad in beiden ist nicht dieselbe Datei auf Platte.
	if filepath.Join(main, "a.go") == filepath.Join(second, "a.go") {
		t.Fatal("the same relative path in two worktrees must be two files")
	}
}
