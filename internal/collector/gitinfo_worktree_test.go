package collector

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func initRepo(t *testing.T, dir string) {
	t.Helper()
	// A commit is needed for rev-parse to name a branch: an unborn HEAD has none.
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"remote", "add", "origin", "https://github.com/example/example-project.git"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "root"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

// Worktrees under .claude/worktrees are removed when the work is done, and the
// collector resolves a session's project by asking git about its working
// directory. By then the directory is gone, so both axes come back empty and
// the session produces no knowledge at all: 165 of 432 projectless sessions in
// the production archive are exactly this.
func TestGitContextWalksUpFromARemovedWorktree(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	gone := filepath.Join(repo, ".claude", "worktrees", "example-project-durchstich")

	got := ResolveGitContext(gone)
	if got.Project != "github.com/example/example-project" {
		t.Errorf("project = %q, want the repository above the removed worktree", got.Project)
	}
	// The worktree's branch died with the directory. The parent is on some
	// branch of its own, and reporting that one would be an invention.
	if got.Branch != "" {
		t.Errorf("branch = %q, want empty: it cannot be known", got.Branch)
	}
}

// A directory that is simply outside any repository must stay projectless.
func TestGitContextDoesNotInventAProjectOutsideARepo(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "not", "a", "repo")
	if got := ResolveGitContext(outside); got.Project != "" {
		t.Errorf("project = %q, want empty", got.Project)
	}
}

// An existing directory keeps the exact behaviour it had, branch included.
func TestGitContextUnchangedForALiveCheckout(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	sub := filepath.Join(repo, "internal", "store")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got := ResolveGitContext(sub)
	if got.Project != "github.com/example/example-project" || got.Branch != "main" {
		t.Errorf("live checkout = %+v, want project and branch", got)
	}
	if got.RepoPath != "internal/store" {
		t.Errorf("repo path = %q", got.RepoPath)
	}
}
