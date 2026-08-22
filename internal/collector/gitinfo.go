package collector

import (
	"os/exec"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

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
