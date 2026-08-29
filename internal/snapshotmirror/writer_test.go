//go:build !windows

package snapshotmirror

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

type staticLister struct{ heads []snapshot.Head }

func (l staticLister) ListContextSnapshots(_ context.Context, f snapshot.ListFilter) (snapshot.SnapshotPage, error) {
	if f.Project == "" || f.Limit <= 0 {
		return snapshot.SnapshotPage{}, fmt.Errorf("missing project or page limit")
	}
	return snapshot.SnapshotPage{Snapshots: l.heads}, nil
}

type pagedLister struct {
	calls []snapshot.ListFilter
}

type cyclingLister struct{}

func (cyclingLister) ListContextSnapshots(_ context.Context, filter snapshot.ListFilter) (snapshot.SnapshotPage, error) {
	switch filter.Cursor {
	case "":
		return snapshot.SnapshotPage{NextCursor: "a"}, nil
	case "a":
		return snapshot.SnapshotPage{NextCursor: "b"}, nil
	default:
		return snapshot.SnapshotPage{NextCursor: "a"}, nil
	}
}

func TestListAllRejectsCursorCycles(t *testing.T) {
	if _, err := listAll(context.Background(), cyclingLister{}, "project"); err == nil {
		t.Fatal("cursor cycle accepted")
	}
}

func (l *pagedLister) ListContextSnapshots(_ context.Context, filter snapshot.ListFilter) (snapshot.SnapshotPage, error) {
	l.calls = append(l.calls, filter)
	if filter.Cursor == "" {
		return snapshot.SnapshotPage{Snapshots: []snapshot.Head{{Name: "new", CreatedAt: "2026-08-30T00:00:00Z"}}, NextCursor: "next"}, nil
	}
	return snapshot.SnapshotPage{Snapshots: []snapshot.Head{{Name: "old", CreatedAt: "2026-08-29T00:00:00Z"}}}, nil
}

func TestRebuildFetchesEveryPageAfterTakingExternalLock(t *testing.T) {
	repo := t.TempDir()
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	lister := new(pagedLister)
	if err := Rebuild(context.Background(), lister, repo, "project"); err != nil {
		t.Fatal(err)
	}
	if len(lister.calls) != 2 || lister.calls[0].Cursor != "" || lister.calls[1].Cursor != "next" {
		t.Fatalf("pagination calls = %#v", lister.calls)
	}
	lockPath, err := projectLockPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(lockPath, repo+string(filepath.Separator)) {
		t.Fatalf("lock is inside repository: %s", lockPath)
	}
	b, err := os.ReadFile(filepath.Join(repo, ".ghosttree", "snapshots", "INDEX.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "## new") || !strings.Contains(string(b), "## old") {
		t.Fatalf("not all pages rendered:\n%s", b)
	}
}

func TestRebuildRejectsSymlinkedSnapshotDirectoryWithoutChangingTarget(t *testing.T) {
	repo := t.TempDir()
	external := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".ghosttree"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(repo, ".ghosttree", "snapshots")); err != nil {
		t.Fatal(err)
	}
	if err := Rebuild(context.Background(), staticLister{}, repo, "project"); err == nil {
		t.Fatal("symlinked snapshots directory accepted")
	}
	if _, err := os.Stat(filepath.Join(external, "INDEX.md")); !os.IsNotExist(err) {
		t.Fatalf("external target changed: %v", err)
	}
}

func TestProjectLockPathResolvesRepositoryAliases(t *testing.T) {
	realRepo := t.TempDir()
	alias := filepath.Join(t.TempDir(), "repo-alias")
	if err := os.Symlink(realRepo, alias); err != nil {
		t.Fatal(err)
	}
	realLock, err := projectLockPath(realRepo)
	if err != nil {
		t.Fatal(err)
	}
	aliasLock, err := projectLockPath(alias)
	if err != nil {
		t.Fatal(err)
	}
	if realLock != aliasLock {
		t.Fatalf("repository aliases use different locks: %q != %q", realLock, aliasLock)
	}
}

func TestRebuildWritesOnlySnapshotIndex(t *testing.T) {
	repo := t.TempDir()
	other := filepath.Join(repo, ".ghosttree", "knowledge", "keep.md")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Rebuild(context.Background(), staticLister{[]snapshot.Head{{Name: "v1", CreatedAt: "2026-08-29T00:00:00Z"}}}, repo, "project"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(repo, ".ghosttree", "snapshots", "INDEX.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "## v1") {
		t.Fatalf("snapshot missing:\n%s", b)
	}
	if b, err := os.ReadFile(other); err != nil || string(b) != "keep" {
		t.Fatalf("other mirror subtree changed: %q, %v", b, err)
	}
}

// This helper-process test makes the stale writer enter Rebuild first and hold
// the project lock while listing. The fresh writer starts while that lock is
// held. The final index must contain the fresh view: listing before locking
// would let the stale writer overwrite it last.
func TestConcurrentRebuildRereadsAfterCrossProcessLock(t *testing.T) {
	if os.Getenv("GHOSTTREE_MIRROR_HELPER") != "" {
		runRebuildHelper(t)
		return
	}
	repo := t.TempDir()
	cache := t.TempDir()
	ready := filepath.Join(t.TempDir(), "ready")
	release := filepath.Join(t.TempDir(), "release")

	stale := helperCommand(t, repo, cache, ready, release, "stale")
	var staleOut bytes.Buffer
	stale.Stdout, stale.Stderr = &staleOut, &staleOut
	if err := stale.Start(); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, ready)
	fresh := helperCommand(t, repo, cache, "", "", "fresh")
	var freshOut bytes.Buffer
	fresh.Stdout, fresh.Stderr = &freshOut, &freshOut
	if err := fresh.Start(); err != nil {
		t.Fatal(err)
	}
	// Let the fresh process reach the lock before the stale process writes.
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(release, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := stale.Wait(); err != nil {
		t.Fatalf("stale: %v\n%s", err, staleOut.Bytes())
	}
	if err := fresh.Wait(); err != nil {
		t.Fatalf("fresh: %v\n%s", err, freshOut.Bytes())
	}

	b, err := os.ReadFile(filepath.Join(repo, ".ghosttree", "snapshots", "INDEX.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "## stale") || !strings.Contains(string(b), "## fresh") {
		t.Fatalf("stale final index:\n%s", b)
	}
}

func helperCommand(t *testing.T, repo, cache, ready, release, name string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestConcurrentRebuildRereadsAfterCrossProcessLock$")
	cmd.Env = append(os.Environ(),
		"GHOSTTREE_MIRROR_HELPER=1", "GHOSTTREE_MIRROR_REPO="+repo,
		"GHOSTTREE_MIRROR_CACHE="+cache, "GHOSTTREE_MIRROR_READY="+ready,
		"GHOSTTREE_MIRROR_RELEASE="+release, "GHOSTTREE_MIRROR_NAME="+name)
	return cmd
}

func runRebuildHelper(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", os.Getenv("GHOSTTREE_MIRROR_CACHE"))
	lister := helperLister{name: os.Getenv("GHOSTTREE_MIRROR_NAME"), ready: os.Getenv("GHOSTTREE_MIRROR_READY"), release: os.Getenv("GHOSTTREE_MIRROR_RELEASE")}
	if err := Rebuild(context.Background(), lister, os.Getenv("GHOSTTREE_MIRROR_REPO"), "project"); err != nil {
		t.Fatal(err)
	}
}

type helperLister struct{ name, ready, release string }

func (l helperLister) ListContextSnapshots(_ context.Context, _ snapshot.ListFilter) (snapshot.SnapshotPage, error) {
	if l.ready != "" {
		if err := os.WriteFile(l.ready, []byte("ready"), 0o600); err != nil {
			return snapshot.SnapshotPage{}, err
		}
		for {
			if _, err := os.Stat(l.release); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	return snapshot.SnapshotPage{Snapshots: []snapshot.Head{{Name: l.name, CreatedAt: "2026-08-29T00:00:00Z"}}}, nil
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("helper never reached the locked listing phase")
}
