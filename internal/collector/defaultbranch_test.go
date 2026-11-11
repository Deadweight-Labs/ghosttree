package collector

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIsDefaultBranchFollowsOriginHeadAndFallsBackToDefault(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "trunk")
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "a")
	run("commit", "-q", "-m", "a")
	ctx := context.Background()
	// Ohne bekannten Default (kein origin, keine der üblichen Namen) gilt jeder Branch als Default.
	if !IsDefaultBranch(ctx, dir, "feat/x") {
		t.Fatal("unknown default: the writer must stay on the project level")
	}
	run("update-ref", "refs/remotes/origin/trunk", "HEAD")
	run("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
	if !IsDefaultBranch(ctx, dir, "trunk") || IsDefaultBranch(ctx, dir, "feat/x") {
		t.Fatal("origin/HEAD names trunk as the default")
	}
	if !IsDefaultBranch(ctx, dir, "") {
		t.Fatal("a detached head has no branch layer")
	}
}
