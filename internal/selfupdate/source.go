package selfupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BuildFromSource fetches branch of repoURL into cacheDir (clone on first use,
// fetch afterwards), builds ./cmd/ctx with `go` and writes the binary to a
// temp file in destDir. The version is <base>-dev+<sha>, base being the
// X.Y.Z of the VERSION file. Trust anchor: the git remote and the Go toolchain
// on this machine; nothing built here is run before it is installed.
func BuildFromSource(ctx context.Context, repoURL, branch, cacheDir, destDir string) (path, version string, err error) {
	run := func(dir string, env []string, name string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "", "", fmt.Errorf("git not found: %w", err)
	}
	if _, err := exec.LookPath("go"); err != nil {
		return "", "", fmt.Errorf("go not found: --source needs the Go toolchain: %w", err)
	}
	if _, serr := os.Stat(filepath.Join(cacheDir, ".git")); serr != nil {
		if err := os.MkdirAll(filepath.Dir(cacheDir), 0o755); err != nil {
			return "", "", err
		}
		if _, err := run("", nil, "git", "clone", "--quiet", "--branch", branch, "--single-branch", repoURL, cacheDir); err != nil {
			return "", "", err
		}
	} else if _, err := run(cacheDir, nil, "git", "fetch", "--quiet", "origin", branch); err != nil {
		return "", "", err
	}
	if _, err := run(cacheDir, nil, "git", "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
		// A fresh clone has no FETCH_HEAD yet.
		if _, err2 := run(cacheDir, nil, "git", "checkout", "--quiet", "--detach", "origin/"+branch); err2 != nil {
			return "", "", err
		}
	}
	sha, err := run(cacheDir, nil, "git", "rev-parse", "--short=7", "HEAD")
	if err != nil {
		return "", "", err
	}
	base := "0.0.0"
	if b, err := os.ReadFile(filepath.Join(cacheDir, "VERSION")); err == nil {
		base, _, _ = strings.Cut(strings.TrimSpace(string(b)), "-")
	}
	version = base + "-dev+" + sha
	tmp, err := os.CreateTemp(destDir, ".ctx-update-*")
	if err != nil {
		return "", "", err
	}
	tmp.Close()
	if _, err := run(cacheDir, []string{"CGO_ENABLED=0"}, "go", "build", "-trimpath",
		"-ldflags", "-s -w -X main.version="+version, "-o", tmp.Name(), "./cmd/ctx"); err != nil {
		os.Remove(tmp.Name())
		return "", "", err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		os.Remove(tmp.Name())
		return "", "", err
	}
	return tmp.Name(), version, nil
}
