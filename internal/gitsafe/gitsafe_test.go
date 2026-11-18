package gitsafe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// repoWithFsmonitor makes a checkout whose own config names a program for
// core.fsmonitor, the way a copied working directory would.
func repoWithFsmonitor(t *testing.T) (repo, marker string) {
	t.Helper()
	repo = t.TempDir()
	marker = filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "fsm.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "f"}, {"commit", "-qm", "i"}, {"config", "core.fsmonitor", script}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return repo, marker
}

func TestRepoConfigProgramsDoNotRun(t *testing.T) {
	repo, marker := repoWithFsmonitor(t)

	// Without the options the checkout's own program runs: this is the hole.
	if err := exec.Command("git", "-C", repo, "status", "--porcelain").Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Skipf("this git does not start core.fsmonitor here: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	if out, err := Command("-C", repo, "status", "--porcelain").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if out, err := CommandContext(context.Background(), "-C", repo, "status", "--porcelain=v1", "-z").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the program from .git/config ran: %v", err)
	}
}
