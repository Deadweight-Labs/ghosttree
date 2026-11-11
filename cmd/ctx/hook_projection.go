package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// The files under .ghosttree/ (the tree of descriptions and the mirror of
// knowledge, documents and the ledger) are rewritten at every session start,
// so that an agent finds them current. That takes one request per document and
// several per discussion, and the session waited for all of them before the
// model saw anything.
//
// Now the session start waits only where there is nothing to read yet: in a
// checkout without .ghosttree/INDEX.md. Everywhere else a process of its own
// rewrites the files while the session begins; for those seconds the files
// show the state of the previous session start, which is what INDEX.md says
// of itself.

// errProjectionBusy: another process is rewriting the files of this checkout.
var errProjectionBusy = errors.New("the files are being rewritten")

// startProjectionRefresh starts `ctx hook refresh-files <root>` detached from
// the hook: a session of its own and no inherited output, so the harness does
// not wait for it and does not end it with the hook. Tests replace it; the test
// binary is not ctx.
var startProjectionRefresh = func(root string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "hook", "refresh-files", root)
	cmd.Dir = root
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func projectionExists(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".ghosttree", "INDEX.md"))
	return err == nil
}

// refreshProjection brings the files up to date at session start, without
// making the session wait when they already exist.
func refreshProjection(cfg config.Config, gitCtx collector.GitContext) {
	if gitCtx.Project == "" || gitCtx.Root == "" {
		return
	}
	if projectionExists(gitCtx.Root) && startProjectionRefresh(gitCtx.Root) == nil {
		return
	}
	writeProjection(cfg, gitCtx, true)
}

// writeProjection rewrites the tree and the mirror of one checkout. One writer
// at a time: both are built in a temporary directory beside the target, and two
// writers would remove each other's work. wait says whether to wait for a
// running writer (the session start that needs the files) or to leave it to
// that writer (the refresh in the background). Errors are swallowed: the files
// are a projection and must never fail a session.
func writeProjection(cfg config.Config, gitCtx collector.GitContext, wait bool) {
	release, err := lockProjection(gitCtx.Root, wait)
	if errors.Is(err, errProjectionBusy) {
		return
	}
	if err == nil {
		defer release()
	}
	if home, err := os.UserHomeDir(); err == nil {
		_ = WriteTree(client.New(cfg), gitCtx.Project, gitCtx.Root, home)
	}
	_ = WriteMirror(client.New(cfg),
		scope.Axes{Project: gitCtx.Project, Branch: gitCtx.Branch, Lineage: gitCtx.Lineage, Machine: cfg.Machine},
		gitCtx.Root)
}

// lockProjection takes the lock of one checkout. It belongs to the open file,
// so a writer that died leaves none behind.
func lockProjection(root string, wait bool) (release func(), err error) {
	dir, err := hookStateDir()
	if err != nil {
		return nil, err
	}
	dir = filepath.Join(dir, "projection")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(root))
	f, err := os.OpenFile(filepath.Join(dir, hex.EncodeToString(sum[:12])+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errProjectionBusy
		}
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

// refreshFiles is `ctx hook refresh-files <repo>`, the process the session
// start leaves behind. It prints nothing and always ends with 0.
func refreshFiles(args []string) int {
	if len(args) != 1 {
		return 0
	}
	cfg, err := config.Load()
	if err != nil {
		return 0
	}
	gitCtx := collector.ResolveGitContext(args[0])
	if gitCtx.Project == "" || gitCtx.Root == "" {
		return 0
	}
	writeProjection(cfg, gitCtx, false)
	return 0
}

// refreshMirrorAfterWrite rewrites the mirror of the checkout the session works
// in after the session stored, replaced or retired a knowledge entry
// (REQ-436 AC-2). It runs in the background of ctx mcp, never makes a tool
// call wait, and does nothing in a checkout that has no mirror yet: the session
// start is what creates it. The ghost tree is left alone, describe_file has its
// own refresh. Errors are swallowed: the files are a projection.
func refreshMirrorAfterWrite(c *client.Client, machine string) {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	g := collector.ResolveGitContext(cwd)
	if g.Project == "" || g.Root == "" || !projectionExists(g.Root) {
		return
	}
	// Wait for a writer that is running: it may have read the entries before
	// this one was stored.
	release, err := lockProjection(g.Root, true)
	if err == nil {
		defer release()
	}
	_ = WriteMirror(c, scope.Axes{Project: g.Project, Branch: g.Branch, Lineage: g.Lineage, Machine: machine}, g.Root)
}
