package collector

import (
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

type GitContext struct {
	Project  string
	Branch   string
	Root     string
	RepoPath string
}

func ResolveGitContext(cwd string) GitContext {
	project, branch := GitInfo(cwd)
	root, err := gitOut(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return GitContext{Project: project, Branch: branch}
	}
	rel, err := filepath.Rel(root, cwd)
	if err != nil {
		return GitContext{Project: project, Branch: branch, Root: root}
	}
	if rel == "." {
		rel = ""
	}
	return GitContext{Project: project, Branch: branch, Root: root, RepoPath: filepath.ToSlash(rel)}
}

// GitInfo derives the project and branch axes from a working directory.
// Both are empty outside a repo or without an origin remote.
func GitInfo(cwd string) (project, branch string) {
	if out, err := gitOut(cwd, "remote", "get-url", "origin"); err == nil {
		project = scope.NormalizeRemote(out)
	}
	if out, err := gitOut(cwd, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && out != "HEAD" {
		branch = out
	}
	return project, branch
}

func gitOut(cwd string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
