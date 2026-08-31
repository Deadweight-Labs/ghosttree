package snapshotmirror

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/ghost"
	"github.com/Deadweight-Labs/ghosttree/internal/privatefile"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

const listPageSize = 100

type SnapshotLister interface {
	ListContextSnapshots(context.Context, snapshot.ListFilter) (snapshot.SnapshotPage, error)
}

func Rebuild(ctx context.Context, lister SnapshotLister, repoRoot, project string) error {
	if lister == nil {
		return fmt.Errorf("snapshot mirror: nil lister")
	}
	if repoRoot == "" || project == "" {
		return fmt.Errorf("snapshot mirror: repository root and project are required")
	}
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return fmt.Errorf("snapshot mirror: resolve repository root: %w", err)
	}
	unlock, err := acquireProjectLock(ctx, absRoot)
	if err != nil {
		return fmt.Errorf("snapshot mirror: lock: %w", err)
	}
	defer unlock()

	// The authoritative list is deliberately fetched only after the
	// cross-process lock is held, preventing a stale waiter from writing last.
	heads, err := listAll(ctx, lister, project)
	if err != nil {
		return fmt.Errorf("snapshot mirror: list: %w", err)
	}
	if err := ghost.EnsureExcluded(absRoot); err != nil {
		return fmt.Errorf("snapshot mirror: exclusions: %w", err)
	}
	dir, err := ensureSnapshotDirectory(absRoot)
	if err != nil {
		return fmt.Errorf("snapshot mirror: create directory: %w", err)
	}
	path := filepath.Join(dir, "INDEX.md")
	if err := privatefile.WriteSyncedNoFollow(path, RenderIndex(heads), 0o600); err != nil {
		return fmt.Errorf("snapshot mirror: write index: %w", err)
	}
	return nil
}

func ensureSnapshotDirectory(repoRoot string) (string, error) {
	if err := requireRealDirectory(repoRoot, false); err != nil {
		return "", err
	}
	ghostRoot := filepath.Join(repoRoot, ".ghosttree")
	if err := requireRealDirectory(ghostRoot, true); err != nil {
		return "", err
	}
	dir := filepath.Join(ghostRoot, "snapshots")
	if err := requireRealDirectory(dir, true); err != nil {
		return "", err
	}
	return dir, nil
}

func requireRealDirectory(path string, create bool) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && create {
		if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("path is not a real directory: %s", path)
	}
	return nil
}

func listAll(ctx context.Context, lister SnapshotLister, project string) ([]snapshot.Head, error) {
	var heads []snapshot.Head
	cursor := ""
	seen := map[string]struct{}{cursor: {}}
	for {
		page, err := lister.ListContextSnapshots(ctx, snapshot.ListFilter{Project: project, Cursor: cursor, Limit: listPageSize})
		if err != nil {
			return nil, err
		}
		heads = append(heads, page.Snapshots...)
		if page.NextCursor == "" {
			return heads, nil
		}
		if _, duplicate := seen[page.NextCursor]; duplicate {
			return nil, fmt.Errorf("snapshot list returned a repeated cursor")
		}
		cursor = page.NextCursor
		seen[cursor] = struct{}{}
	}
}

func projectLockPath(repoRoot string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	identity, err := canonicalLockIdentity(repoRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "ghosttree", "snapshot-mirror-locks", lockIDForIdentity(identity, runtime.GOOS == "windows")+".lock"), nil
}

func lockIDForIdentity(identity string, caseInsensitive bool) string {
	if caseInsensitive {
		identity = strings.ToLower(path.Clean(strings.ReplaceAll(identity, `\`, "/")))
	} else {
		identity = filepath.Clean(identity)
	}
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}
