package collector

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

func TestTrackedGeneratedPathsAffectReleaseAndFingerprint(t *testing.T) {
	for _, dir := range []string{"tree", "knowledge", "docs", "requests"} {
		for _, change := range []string{"modified", "staged", "deleted", "staged-delete", "renamed"} {
			t.Run(dir+"/"+change, func(t *testing.T) {
				repo := newSnapshotGitRepo(t, "sha1")
				path := filepath.Join(".ghosttree", dir, "owned.md")
				writeSnapshotFile(t, filepath.Join(repo, path), "original")
				gitSnapshot(t, repo, "add", "-f", path)
				gitSnapshot(t, repo, "commit", "-m", "tracked generated path")
				gitSnapshot(t, repo, "tag", "v1.0.0")
				writeSnapshotFile(t, filepath.Join(repo, "tracked.txt"), "other dirty file")
				before := snapshotFingerprint(t, repo)
				switch change {
				case "modified", "staged":
					writeSnapshotFile(t, filepath.Join(repo, path), "changed")
					if change == "staged" {
						gitSnapshot(t, repo, "add", "-f", path)
					}
				case "deleted", "staged-delete":
					if err := os.Remove(filepath.Join(repo, path)); err != nil {
						t.Fatal(err)
					}
					if change == "staged-delete" {
						gitSnapshot(t, repo, "add", "-u")
					}
				case "renamed":
					gitSnapshot(t, repo, "mv", path, path+".renamed")
				}
				if after := snapshotFingerprint(t, repo); after == before {
					t.Fatal("tracked generated path did not affect fingerprint")
				}
				gitSnapshot(t, repo, "restore", "--staged", "--worktree", "tracked.txt")
				_, err := ResolveSnapshotGit(repo, "v1.0.0", false)
				assertSnapshotRule(t, err, "snapshot_dirty_worktree")
			})
		}
	}
}

func TestTrackedGeneratedStagedDeletionRetainsRecreatedWorktree(t *testing.T) {
	repo := newSnapshotGitRepo(t, "sha1")
	path := ".ghosttree/docs/owned.md"
	writeSnapshotFile(t, filepath.Join(repo, path), "original")
	gitSnapshot(t, repo, "add", "-f", path)
	gitSnapshot(t, repo, "commit", "-m", "tracked generated path")
	writeSnapshotFile(t, filepath.Join(repo, ".git/info/exclude"), ".ghosttree/docs/\n")
	gitSnapshot(t, repo, "rm", path)
	before := snapshotFingerprint(t, repo)
	writeSnapshotFile(t, filepath.Join(repo, path), "recreated")
	after := snapshotFingerprint(t, repo)
	if before == after {
		t.Fatal("staged deletion hid recreated ignored worktree content")
	}
}

func TestRecheckSnapshotGitPreservesResolverErrors(t *testing.T) {
	repo := newSnapshotGitRepo(t, "sha1")
	want, err := ResolveSnapshotGit(repo, "checkpoint", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, repo, code string }{
		{"V1.2.3", repo, "snapshot_invalid_input"},
		{"checkpoint", filepath.Join(repo, "missing"), ""},
	} {
		t.Run(tc.name+tc.code, func(t *testing.T) {
			err := RecheckSnapshotGit(tc.repo, tc.name, want)
			if err == nil {
				t.Fatal("resolver error lost")
			}
			var rule *snapshot.RuleError
			if errors.As(err, &rule) && (rule.Code != tc.code || rule.Retryable) {
				t.Fatalf("resolver error became %+v", rule)
			}
			if tc.code != "" && !errors.As(err, &rule) {
				t.Fatalf("missing rule error: %v", err)
			}
		})
	}
}

func TestTrackedGeneratedDeletedSubmoduleRetainsWorktreeChanges(t *testing.T) {
	repo := newSnapshotGitRepo(t, "sha1")
	child := newSnapshotGitRepo(t, "sha1")
	path := ".ghosttree/tree/sub"
	gitSnapshot(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", child, path)
	gitSnapshot(t, repo, "commit", "-am", "submodule")
	gitSnapshot(t, repo, "rm", "--cached", path)
	before := snapshotFingerprint(t, repo)
	writeSnapshotFile(t, filepath.Join(repo, path, "tracked.txt"), "changed child")
	if after := snapshotFingerprint(t, repo); after == before {
		t.Fatal("staged submodule deletion hid remaining worktree changes")
	}
}
