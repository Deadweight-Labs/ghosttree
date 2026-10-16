package garden

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// safeGitConfig is what every git call of the gardener runs with. A session
// that could write into the checkout may have planted a hook, an fsmonitor
// command or a filter in .git/config; the command line beats the repository
// config, so these settings hold whatever the checkout says.
func safeGitConfig(root string) []string {
	return []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "core.attributesFile=/dev/null",
		"-c", "core.pager=cat",
		"-c", "core.sshCommand=false",
		"-c", "protocol.allow=never",
		"-c", "diff.external=",
		"-c", "safe.directory=" + root,
	}
}

// SafeGitEnv is the environment of those calls, built from nothing: no system
// config, a neutral global one, no GIT_* variable of the parent (GIT_DIR,
// GIT_EXTERNAL_DIFF, GIT_SSH_COMMAND and the like) and no home directory to
// look in.
func SafeGitEnv() []string {
	env := []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"HOME=/nonexistent",
		"LC_ALL=C",
	}
	if p := os.Getenv("PATH"); p != "" {
		env = append(env, "PATH="+p)
	}
	return env
}

// SafeGit builds a git command for a checkout with the safe settings.
func SafeGit(root string, args ...string) *exec.Cmd {
	full := append([]string{}, safeGitConfig(root)...)
	full = append(full, "-C", root)
	full = append(full, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = SafeGitEnv()
	return cmd
}

// guarded are the places inside .git that decide what git runs or ignores:
// the config (hooks, fsmonitor, filters), the hooks, the exclude and
// attributes files, and HEAD.
var guarded = []string{"config", "config.worktree", "hooks", "info", "HEAD"}

// Fingerprint is a hash over what could make a later git call run foreign code
// or read the tree differently: .git/config, .git/hooks, .git/info, HEAD, and
// the .git entry itself when it is a file pointing elsewhere. It is taken
// without running git.
func (r *Repo) Fingerprint() (string, error) {
	h := sha256.New()
	gitPath := filepath.Join(r.Root, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		// A worktree or submodule: .git is a file that points at the real one.
		b, err := os.ReadFile(gitPath)
		if err != nil {
			return "", err
		}
		h.Write([]byte("gitfile\x00"))
		h.Write(b)
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	var paths []string
	for _, g := range guarded {
		root := filepath.Join(gitPath, g)
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil {
				paths = append(paths, p)
			}
			return nil
		})
	}
	sort.Strings(paths)
	for _, p := range paths {
		rel, _ := filepath.Rel(gitPath, p)
		fi, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		h.Write([]byte(rel + "\x00" + fi.Mode().String() + "\x00"))
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			t, _ := os.Readlink(p)
			h.Write([]byte(t))
		case fi.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return "", err
			}
			h.Write(b)
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
