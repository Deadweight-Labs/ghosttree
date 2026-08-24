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

// The migration digest is built from the project name GitInfo returns, so if
// that name still varied with the spelling of the remote, the idempotency guard
// would treat a rerun as new work. That is not hypothetical: it is how one
// repository ended up in the ledger three times under three spellings.
func TestGitInfoReturnsTheSameProjectForEverySpellingOfTheRemote(t *testing.T) {
	spellings := []string{
		"https://github.com/Example/SampleProject.git",
		"https://github.com/example/sampleproject",
		"git@github.com:Example/SampleProject.git",
		"ssh://github.com/example/SampleProject/",
	}
	var seen string
	for _, remote := range spellings {
		repo := filepath.Join(t.TempDir(), "repo")
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"init", repo}, {"-C", repo, "remote", "add", "origin", remote}} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v: %s", args, err, out)
			}
		}
		project, _ := GitInfo(repo)
		if project != "github.com/example/sampleproject" {
			t.Fatalf("remote %q gave project %q, want the canonical name", remote, project)
		}
		if seen != "" && project != seen {
			t.Fatalf("remote %q gave %q, previous spelling gave %q", remote, project, seen)
		}
		seen = project
	}
}
