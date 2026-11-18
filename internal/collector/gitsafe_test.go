package collector

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A checkout that brought its own core.fsmonitor must not get that program
// started by the status the anchor and the snapshot take.
func TestStatusDoesNotRunTheFsmonitorOfTheCheckout(t *testing.T) {
	repo := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "fsm.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"},
		{"add", "f"}, {"commit", "-qm", "i"}, {"config", "core.fsmonitor", script},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if _, err := anchorGit(context.Background(), repo, "status", "--porcelain", "--untracked-files=no"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitBytes(repo, "status", "--porcelain=v1", "-z", "--untracked-files=all"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the program from .git/config ran: %v", err)
	}
}
