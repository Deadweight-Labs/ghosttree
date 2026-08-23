package collector

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestResolveGitContext(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(repo, "core", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", repo}, {"-C", repo, "remote", "add", "origin", "https://github.com/x/y.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	got := ResolveGitContext(filepath.Join(repo, "core", "lib"))
	if got.Project != "github.com/x/y" || got.RepoPath != "core/lib" || got.Root != repo {
		t.Fatalf("GitContext = %+v", got)
	}
	outside := ResolveGitContext(t.TempDir())
	if outside.Root != "" || outside.RepoPath != "" {
		t.Fatalf("outside Git = %+v", outside)
	}
}
