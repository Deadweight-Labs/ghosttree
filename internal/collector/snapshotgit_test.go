package collector

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/ghost"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

func TestSnapshotGitReleaseTagsAndDeterministicRefs(t *testing.T) {
	repo := newSnapshotGitRepo(t, "sha1")
	gitSnapshot(t, repo, "tag", "v1.2.3")

	got, err := ResolveSnapshotGit(repo, "v1.2.3", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.ObjectFormat != "sha1" || got.Ref == nil || *got.Ref != "refs/tags/v1.2.3" {
		t.Fatalf("release provenance = %+v", got)
	}
	if got.Branch == nil || *got.Branch != "main" || got.MetadataSource != "server-verified" {
		t.Fatalf("release provenance = %+v", got)
	}
	if got.AllowDirtyUsed || got.WorktreeFingerprintVersion != nil || got.WorktreeFingerprint != nil {
		t.Fatalf("clean release recorded dirty-only fields: %+v", got)
	}
	checkpoint, err := ResolveSnapshotGit(repo, "checkpoint", false)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Ref == nil || *checkpoint.Ref != "refs/heads/main" {
		t.Fatalf("non-release ref = %+v", checkpoint)
	}

	gitSnapshot(t, repo, "tag", "-a", "v1.2.4", "-m", "annotated")
	annotated, err := ResolveSnapshotGit(repo, "v1.2.4", false)
	if err != nil || annotated.Commit != got.Commit {
		t.Fatalf("annotated tag was not peeled: %+v, %v", annotated, err)
	}

	gitSnapshot(t, repo, "checkout", "--detach", "HEAD")
	detached, err := ResolveSnapshotGit(repo, "checkpoint", false)
	if err != nil {
		t.Fatal(err)
	}
	if detached.Ref != nil || detached.Branch != nil {
		t.Fatalf("detached provenance chose a ref: %+v", detached)
	}
}

func TestSnapshotGitRejectsReleaseMismatchAndDirtyTree(t *testing.T) {
	repo := newSnapshotGitRepo(t, "sha1")
	_, err := ResolveSnapshotGit(repo, "v1.0.0", false)
	assertSnapshotRule(t, err, "snapshot_tag_mismatch")

	gitSnapshot(t, repo, "tag", "v1.0.0")
	writeSnapshotFile(t, filepath.Join(repo, "tracked.txt"), "second\n")
	gitSnapshot(t, repo, "add", "tracked.txt")
	gitSnapshot(t, repo, "commit", "-m", "second")
	_, err = ResolveSnapshotGit(repo, "v1.0.0", true)
	assertSnapshotRule(t, err, "snapshot_tag_mismatch")

	gitSnapshot(t, repo, "tag", "v1.0.1")
	writeSnapshotFile(t, filepath.Join(repo, "tracked.txt"), "dirty\n")
	_, err = ResolveSnapshotGit(repo, "v1.0.1", false)
	assertSnapshotRule(t, err, "snapshot_dirty_worktree")
	got, err := ResolveSnapshotGit(repo, "v1.0.1", true)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Dirty || !got.AllowDirtyUsed || got.WorktreeFingerprintVersion == nil || got.WorktreeFingerprint == nil {
		t.Fatalf("dirty override provenance = %+v", got)
	}
}

func TestWorktreeFingerprintChangesForRelevantStates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and executable-mode semantics differ on Windows")
	}
	repo := newSnapshotGitRepo(t, "sha1")
	base := dirtyFingerprint(t, repo, func() { writeSnapshotFile(t, filepath.Join(repo, "untracked"), "one") })
	changed := dirtyFingerprint(t, repo, func() { writeSnapshotFile(t, filepath.Join(repo, "untracked"), "two") })
	if base == changed {
		t.Fatal("untracked content change did not change fingerprint")
	}
	os.Remove(filepath.Join(repo, "untracked"))

	cases := []struct {
		name   string
		mutate func()
	}{
		{"unstaged", func() { writeSnapshotFile(t, filepath.Join(repo, "tracked.txt"), "unstaged\n") }},
		{"staged", func() {
			writeSnapshotFile(t, filepath.Join(repo, "tracked.txt"), "staged\n")
			gitSnapshot(t, repo, "add", "tracked.txt")
		}},
		{"deleted", func() { os.Remove(filepath.Join(repo, "tracked.txt")) }},
		{"symlink-target", func() { os.Symlink("tracked.txt", filepath.Join(repo, "link")) }},
		{"mode", func() { os.Chmod(filepath.Join(repo, "tracked.txt"), 0o755) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gitSnapshot(t, repo, "reset", "--hard", "HEAD")
			os.Remove(filepath.Join(repo, "link"))
			tc.mutate()
			one := snapshotFingerprint(t, repo)
			if one == (snapshot.Digest{}) {
				t.Fatal("dirty tree has zero fingerprint")
			}
		})
	}
}

func TestWorktreeFingerprintDistinguishesIndexModeSymlinkAndDeletion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and executable-mode semantics differ on Windows")
	}
	repo := newSnapshotGitRepo(t, "sha1")
	fingerprintAfterReset := func(mutate func()) snapshot.Digest {
		t.Helper()
		gitSnapshot(t, repo, "reset", "--hard", "HEAD")
		_ = os.Remove(filepath.Join(repo, "link"))
		mutate()
		return snapshotFingerprint(t, repo)
	}

	unstaged := fingerprintAfterReset(func() {
		writeSnapshotFile(t, filepath.Join(repo, "tracked.txt"), "same bytes\n")
	})
	staged := fingerprintAfterReset(func() {
		writeSnapshotFile(t, filepath.Join(repo, "tracked.txt"), "same bytes\n")
		gitSnapshot(t, repo, "add", "tracked.txt")
	})
	if unstaged == staged {
		t.Fatal("index state did not change fingerprint")
	}

	mode644 := fingerprintAfterReset(func() {
		writeSnapshotFile(t, filepath.Join(repo, "untracked"), "same\n")
	})
	mode755 := fingerprintAfterReset(func() {
		writeSnapshotFile(t, filepath.Join(repo, "untracked"), "same\n")
		if err := os.Chmod(filepath.Join(repo, "untracked"), 0o755); err != nil {
			t.Fatal(err)
		}
	})
	if mode644 == mode755 {
		t.Fatal("lstat mode did not change fingerprint")
	}
	_ = os.Remove(filepath.Join(repo, "untracked"))

	linkOne := fingerprintAfterReset(func() {
		if err := os.Symlink("tracked.txt", filepath.Join(repo, "link")); err != nil {
			t.Fatal(err)
		}
	})
	linkTwo := fingerprintAfterReset(func() {
		if err := os.Symlink("missing.txt", filepath.Join(repo, "link")); err != nil {
			t.Fatal(err)
		}
	})
	if linkOne == linkTwo {
		t.Fatal("symlink target did not change fingerprint")
	}

	deleted := fingerprintAfterReset(func() {
		if err := os.Remove(filepath.Join(repo, "tracked.txt")); err != nil {
			t.Fatal(err)
		}
	})
	if deleted == linkTwo {
		t.Fatal("deletion collided with symlink state")
	}
}

func TestWorktreeFingerprintIncludesSubmoduleHeadAndDirtyState(t *testing.T) {
	sub := newSnapshotGitRepo(t, "sha1")
	repo := newSnapshotGitRepo(t, "sha1")
	gitSnapshot(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", sub, "dependency")
	gitSnapshot(t, repo, "commit", "-m", "add submodule")

	clean, err := ResolveSnapshotGit(repo, "checkpoint", false)
	if err != nil {
		t.Fatal(err)
	}
	if clean.Dirty || clean.WorktreeFingerprint != nil {
		t.Fatalf("clean submodule provenance = %+v", clean)
	}

	writeSnapshotFile(t, filepath.Join(repo, "dependency", "tracked.txt"), "dirty one\n")
	one := snapshotFingerprint(t, repo)
	writeSnapshotFile(t, filepath.Join(repo, "dependency", "tracked.txt"), "dirty two\n")
	two := snapshotFingerprint(t, repo)
	if one == two {
		t.Fatal("dirty submodule content did not change fingerprint")
	}

	gitSnapshot(t, filepath.Join(repo, "dependency"), "config", "user.name", "Snapshot Test")
	gitSnapshot(t, filepath.Join(repo, "dependency"), "config", "user.email", "snapshot@example.test")
	gitSnapshot(t, filepath.Join(repo, "dependency"), "add", "tracked.txt")
	gitSnapshot(t, filepath.Join(repo, "dependency"), "commit", "-m", "advance")
	advanced := snapshotFingerprint(t, repo)
	if two == advanced {
		t.Fatal("submodule HEAD did not change fingerprint")
	}
}

func TestSnapshotIndexExcludedFromDirtyStateAndFingerprint(t *testing.T) {
	repo := newSnapshotGitRepo(t, "sha1")
	index := filepath.Join(repo, ".ghosttree", "snapshots", "INDEX.md")
	writeSnapshotFile(t, index, "first\n")
	clean, err := ResolveSnapshotGit(repo, "checkpoint", false)
	if err != nil {
		t.Fatal(err)
	}
	if clean.Dirty || clean.WorktreeFingerprint != nil {
		t.Fatalf("generated snapshot index made tree dirty: %+v", clean)
	}
	writeSnapshotFile(t, filepath.Join(repo, ".ghosttree", "operator-note"), "one\n")
	one := snapshotFingerprint(t, repo)
	writeSnapshotFile(t, index, "second\n")
	two := snapshotFingerprint(t, repo)
	if one != two {
		t.Fatal("generated snapshot index changed fingerprint")
	}
	writeSnapshotFile(t, filepath.Join(repo, ".ghosttree", "operator-note"), "two\n")
	three := snapshotFingerprint(t, repo)
	if two == three {
		t.Fatal("operator-owned .ghosttree file did not change fingerprint")
	}
}

func TestWorktreeFingerprintIncludesIgnoredDocumentDraftLifecycle(t *testing.T) {
	repo := newSnapshotGitRepo(t, "sha1")
	if err := ghost.EnsureExcluded(repo); err != nil {
		t.Fatal(err)
	}
	draft := filepath.Join(repo, ".ghosttree", "edit", "snapshot-spec.md")
	writeSnapshotFile(t, draft, "first\n")
	if out, err := exec.Command("git", "-C", repo, "status", "--porcelain=v1").CombinedOutput(); err != nil {
		t.Fatal(err)
	} else if len(out) != 0 {
		t.Fatalf("test precondition: draft should be hidden by info/exclude, status=%q", out)
	}
	created := snapshotFingerprint(t, repo)
	writeSnapshotFile(t, draft, "second\n")
	modified := snapshotFingerprint(t, repo)
	if created == modified {
		t.Fatal("modified ignored draft did not change fingerprint")
	}
	if err := os.Remove(draft); err != nil {
		t.Fatal(err)
	}
	clean, err := ResolveSnapshotGit(repo, "checkpoint", false)
	if err != nil {
		t.Fatal(err)
	}
	if clean.Dirty || clean.WorktreeFingerprint != nil {
		t.Fatalf("removing an untracked draft did not return to clean provenance: %+v", clean)
	}
	expected := clean
	expected.Dirty = true
	expected.WorktreeFingerprintVersion = uint32Pointer(snapshot.WorktreeFingerprintVersion)
	expected.WorktreeFingerprint = &modified
	if err := RecheckSnapshotGit(repo, "checkpoint", expected); err == nil {
		t.Fatal("deleted ignored draft was not detected as provenance change")
	}

	writeSnapshotFile(t, draft, "tracked baseline\n")
	gitSnapshot(t, repo, "add", "-f", ".ghosttree/edit/snapshot-spec.md")
	gitSnapshot(t, repo, "commit", "-m", "track draft fixture")
	if err := os.Remove(draft); err != nil {
		t.Fatal(err)
	}
	deleted := snapshotFingerprint(t, repo)
	if deleted == created || deleted == modified {
		t.Fatal("deleted tracked draft collided with another draft state")
	}
}

func TestWorktreeFingerprintKeepsOtherGhosttreeOperatorPathsRelevant(t *testing.T) {
	repo := newSnapshotGitRepo(t, "sha1")
	paths := []string{
		".ghosttree/operator-note",
		".ghosttree/snapshots/operator-note",
		".ghosttree/edit-local-note",
	}
	var previous snapshot.Digest
	for _, path := range paths {
		writeSnapshotFile(t, filepath.Join(repo, filepath.FromSlash(path)), path)
		current := snapshotFingerprint(t, repo)
		if previous != (snapshot.Digest{}) && current == previous {
			t.Fatalf("operator path %q did not change fingerprint", path)
		}
		previous = current
	}
}

func TestRecheckSnapshotGitDetectsChanges(t *testing.T) {
	repo := newSnapshotGitRepo(t, "sha1")
	want, err := ResolveSnapshotGit(repo, "checkpoint", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecheckSnapshotGit(repo, "checkpoint", want); err != nil {
		t.Fatalf("unchanged recheck: %v", err)
	}
	writeSnapshotFile(t, filepath.Join(repo, "new"), "changed")
	err = RecheckSnapshotGit(repo, "checkpoint", want)
	var rule *snapshot.RuleError
	if !errors.As(err, &rule) || rule.Code != "snapshot_git_changed" || !rule.Retryable {
		t.Fatalf("changed recheck error = %#v", err)
	}
}

func TestSnapshotGitSHA256Repository(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	cmd := exec.Command("git", "init", "--object-format=sha256", "-b", "main", repo)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git lacks SHA-256 repository support: %v: %s", err, out)
	}
	finishSnapshotGitRepo(t, repo)
	got, err := ResolveSnapshotGit(repo, "checkpoint", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.ObjectFormat != "sha256" || len(got.Commit) != 64 {
		t.Fatalf("SHA-256 provenance = %+v", got)
	}
}

func newSnapshotGitRepo(t *testing.T, format string) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	gitSnapshot(t, "", "init", "--object-format="+format, "-b", "main", repo)
	finishSnapshotGitRepo(t, repo)
	return repo
}

func finishSnapshotGitRepo(t *testing.T, repo string) {
	t.Helper()
	gitSnapshot(t, repo, "config", "user.name", "Snapshot Test")
	gitSnapshot(t, repo, "config", "user.email", "snapshot@example.test")
	writeSnapshotFile(t, filepath.Join(repo, "tracked.txt"), "first\n")
	gitSnapshot(t, repo, "add", "tracked.txt")
	gitSnapshot(t, repo, "commit", "-m", "initial")
}

func dirtyFingerprint(t *testing.T, repo string, mutate func()) snapshot.Digest {
	t.Helper()
	mutate()
	return snapshotFingerprint(t, repo)
}

func snapshotFingerprint(t *testing.T, repo string) snapshot.Digest {
	t.Helper()
	got, err := ResolveSnapshotGit(repo, "checkpoint", false)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Dirty || got.WorktreeFingerprint == nil {
		t.Fatalf("wanted dirty fingerprint, got %+v", got)
	}
	return *got.WorktreeFingerprint
}

func assertSnapshotRule(t *testing.T, err error, code string) {
	t.Helper()
	var rule *snapshot.RuleError
	if !errors.As(err, &rule) || rule.Code != code {
		t.Fatalf("error = %#v, want %s", err, code)
	}
}

func writeSnapshotFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitSnapshot(t *testing.T, repo string, args ...string) {
	t.Helper()
	if repo != "" {
		args = append([]string{"-C", repo}, args...)
	}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func uint32Pointer(value uint32) *uint32 { return &value }
