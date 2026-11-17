package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The local worktree of workflows (REQ-699, DEC-44): .ghosttree/workflows/
// holds one <name>.md per workflow, beside a .state.json that remembers, for
// each file, the version and hash it was pulled at. Like the document
// worktree (.ghosttree/edit/) it is written by people and agents and only
// ever by `ctx workflow`: the mirror never writes there, git ignores it, and
// nothing of it is committed. The truth stays on the server; a push is a save
// through SaveDef, the one function web, MCP and API use.
//
// The directory comes with a checkout, so a repository can bring something
// there: a symbolic link (.ghosttree/workflows/deploy.md -> ~/.bash_aliases)
// would make a pull write the server's text outside the worktree, a tracked
// .state.json would forge the bases. So .ghosttree and .ghosttree/workflows
// have to be plain directories, every file a regular one, nothing in it
// tracked by git (CheckWorktree), files are opened without following a link
// and written through a new temporary file and a rename.

// WorktreeEntry is what is known of one file: the version of the server it
// was pulled or pushed at and the hash of the text it had then. Version 0 is a
// draft that was never pushed.
type WorktreeEntry struct {
	Version int    `json:"version"`
	Hash    string `json:"hash"`
}

// WorktreeState maps a workflow name to its entry.
type WorktreeState map[string]WorktreeEntry

// WorktreeDir is the directory of the files.
func WorktreeDir(repoRoot string) string {
	return filepath.Join(repoRoot, ".ghosttree", "workflows")
}

// WorktreeFile is the file of a workflow.
func WorktreeFile(repoRoot, name string) string {
	return filepath.Join(WorktreeDir(repoRoot), name+".md")
}

// CheckWorktreeName refuses a name the server would refuse, before a file
// is made for it. That keeps a name from reaching the file system as a path.
func CheckWorktreeName(name string) error {
	if _, ok := store.CleanWorkflowDefName(name); !ok || name != strings.TrimSpace(name) {
		return fmt.Errorf("invalid workflow name %q: 2 to 64 lower-case letters, digits, - and _ (not new or runs)", name)
	}
	return nil
}

// errNotPlain refuses what is not a plain directory or a regular file.
func errNotPlain(path string) error {
	return fmt.Errorf("%s is not a plain directory or regular file (a symbolic link?); ctx workflow does not follow it", path)
}

// checkWorktreeDirs refuses a .ghosttree or .ghosttree/workflows that exists
// and is not a plain directory.
func checkWorktreeDirs(repoRoot string) error {
	for _, dir := range []string{filepath.Join(repoRoot, ".ghosttree"), WorktreeDir(repoRoot)} {
		fi, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !fi.Mode().IsDir() {
			return errNotPlain(dir)
		}
	}
	return nil
}

// ensureWorktreeDir makes the directory where it is missing, after the check.
func ensureWorktreeDir(repoRoot string) error {
	if err := checkWorktreeDirs(repoRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(WorktreeDir(repoRoot), 0o755); err != nil {
		return err
	}
	return checkWorktreeDirs(repoRoot)
}

// CheckWorktree refuses a worktree a checkout brought: a directory that is a
// symbolic link, or anything under .ghosttree/workflows that git tracks. A
// directory that is not in a git repository has nothing tracked.
func CheckWorktree(repoRoot string) error {
	if err := checkWorktreeDirs(repoRoot); err != nil {
		return err
	}
	out, err := exec.Command("git", "-C", repoRoot, "ls-files", "-z", "--", ".ghosttree/workflows").Output()
	if err != nil {
		return nil
	}
	if tracked := strings.Split(strings.Trim(string(out), "\x00"), "\x00"); tracked[0] != "" {
		return fmt.Errorf("git tracks %s; the workflow worktree is never committed, and ctx workflow refuses to work in one a checkout brought (git rm --cached it, or remove it)", strings.Join(tracked, ", "))
	}
	return nil
}

// readPlainFile reads a regular file without following a symbolic link.
func readPlainFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, errNotPlain(path)
		}
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errNotPlain(path)
	}
	return io.ReadAll(f)
}

// writePlainFile writes a file through a new temporary file beside it and a
// rename, which replaces a directory entry and never writes through a link.
// An existing entry that is not a regular file is refused.
func writePlainFile(repoRoot, path string, data []byte) error {
	if err := ensureWorktreeDir(repoRoot); err != nil {
		return err
	}
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return errNotPlain(path)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, bytes.NewReader(data)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// LoadWorktreeState reads .state.json; a missing file is an empty state.
func LoadWorktreeState(repoRoot string) (WorktreeState, error) {
	if err := checkWorktreeDirs(repoRoot); err != nil {
		return nil, err
	}
	path := filepath.Join(WorktreeDir(repoRoot), ".state.json")
	raw, err := readPlainFile(path)
	if os.IsNotExist(err) {
		return WorktreeState{}, nil
	}
	if err != nil {
		return nil, err
	}
	state := WorktreeState{}
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return state, nil
}

// SaveWorktreeState writes .state.json through a temporary file, so that a
// crash leaves the old state and not half of a new one.
func SaveWorktreeState(repoRoot string, state WorktreeState) error {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writePlainFile(repoRoot, filepath.Join(WorktreeDir(repoRoot), ".state.json"), append(raw, '\n'))
}

// ReadWorktreeFile reads the file of a workflow byte for byte. A text that is
// not UTF-8 is refused rather than rewritten.
func ReadWorktreeFile(repoRoot, name string) (string, error) {
	if err := CheckWorktreeName(name); err != nil {
		return "", err
	}
	if err := checkWorktreeDirs(repoRoot); err != nil {
		return "", err
	}
	raw, err := readPlainFile(WorktreeFile(repoRoot, name))
	if err != nil {
		return "", err
	}
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("%s.md is not valid UTF-8; ghosttree stores text byte-for-byte and will not rewrite it", name)
	}
	return string(raw), nil
}

// WriteWorktreeFile writes the file of a workflow.
func WriteWorktreeFile(repoRoot, name, text string) error {
	if err := CheckWorktreeName(name); err != nil {
		return err
	}
	return writePlainFile(repoRoot, WorktreeFile(repoRoot, name), []byte(text))
}

// WorktreeNames lists the workflows that have a file, sorted.
func WorktreeNames(repoRoot string) ([]string, error) {
	if err := checkWorktreeDirs(repoRoot); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(WorktreeDir(repoRoot))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		n, ok := strings.CutSuffix(e.Name(), ".md")
		if ok && !e.IsDir() && CheckWorktreeName(n) == nil {
			names = append(names, n)
		}
	}
	return names, nil
}

// SkeletonText is the file `ctx workflow new` writes without a template: the
// smallest file that passes the check, with the keys a person usually
// changes next.
const SkeletonText = `---
# One workflow per file. The text below the second --- line is the extra
# instruction for the model (stage 2); leave it empty for a block alone.
description: What this workflow does, in a few words
on: pr.merged
uses: ghosttree/echo
---
`
