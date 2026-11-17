package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// LoadWorktreeState reads .state.json; a missing file is an empty state.
func LoadWorktreeState(repoRoot string) (WorktreeState, error) {
	path := filepath.Join(WorktreeDir(repoRoot), ".state.json")
	raw, err := os.ReadFile(path)
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
	dir := WorktreeDir(repoRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state.json.tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
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
	return os.Rename(tmpName, filepath.Join(dir, ".state.json"))
}

// ReadWorktreeFile reads the file of a workflow byte for byte. A text that is
// not UTF-8 is refused rather than rewritten.
func ReadWorktreeFile(repoRoot, name string) (string, error) {
	if err := CheckWorktreeName(name); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(WorktreeFile(repoRoot, name))
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
	if err := os.MkdirAll(WorktreeDir(repoRoot), 0o755); err != nil {
		return err
	}
	return os.WriteFile(WorktreeFile(repoRoot, name), []byte(text), 0o644)
}

// WorktreeNames lists the workflows that have a file, sorted.
func WorktreeNames(repoRoot string) ([]string, error) {
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
