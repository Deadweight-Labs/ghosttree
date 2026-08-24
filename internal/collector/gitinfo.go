package collector

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

type GitContext struct {
	Project  string
	Branch   string
	Lineage  []string
	Root     string
	RepoPath string
}

func ResolveGitContext(cwd string) GitContext {
	project, branch := GitInfo(cwd)
	if project == "" {
		// The directory may be gone rather than unversioned. Worktrees under
		// .claude/worktrees are removed once the work is done, and by the time a
		// transcript is collected git has nothing left to answer about: 165 of
		// the 432 projectless sessions in the archive are exactly that.
		//
		// The branch is deliberately not carried over. It died with the
		// directory, and the surviving parent is on a branch of its own that
		// this session was never on.
		if ancestor := nearestExistingAncestor(cwd); ancestor != "" {
			project, _ = GitInfo(ancestor)
			branch = ""
		}
	}
	lineage := BranchLineage(cwd, branch)
	root, err := gitOut(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return GitContext{Project: project, Branch: branch, Lineage: lineage}
	}
	rel, err := filepath.Rel(root, cwd)
	if err != nil {
		return GitContext{Project: project, Branch: branch, Lineage: lineage, Root: root}
	}
	if rel == "." {
		rel = ""
	}
	return GitContext{Project: project, Branch: branch, Lineage: lineage, Root: root, RepoPath: filepath.ToSlash(rel)}
}

// BranchLineage names the branches the current one was cut from and still
// contains. Derived, never guessed: `git branch --merged HEAD` lists exactly
// the branches whose tip is reachable from here, which is the same question as
// "does my working tree carry what that branch had".
//
// Git does not record which branch a branch was created from — the reflog says
// so only until it expires and says nothing at all in a fresh clone — so
// containment is the strongest claim the repository can actually support.
//
// The cost is honest and worth naming: once the parent moves on, its tip is no
// longer contained here and it drops out of the lineage until the next merge.
// The alternative, treating any shared merge base as ancestry, would make every
// branch an ancestor of every other, which is not a lineage but a repository.
func BranchLineage(cwd, branch string) []string {
	if branch == "" {
		return nil
	}
	out, err := gitOut(cwd, "branch", "--format=%(refname:short)", "--merged", "HEAD")
	if err != nil {
		// No repository, a detached head, or a git too old for --format. A
		// session with no derivable chain reads at project scope, which is what
		// it did before lineage existed.
		return nil
	}
	var lineage []string
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "* "))
		if name == "" || name == branch || strings.HasPrefix(name, "(") {
			continue
		}
		lineage = append(lineage, name)
	}
	return lineage
}

// nearestExistingAncestor returns the closest parent of a vanished path that
// still exists, or "" if the path itself is there — in which case there is
// nothing to recover and asking again would only repeat the same answer.
func nearestExistingAncestor(path string) string {
	if path == "" {
		return ""
	}
	if _, err := os.Stat(path); err == nil {
		return ""
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
		if parent := filepath.Dir(dir); parent == dir {
			return ""
		}
	}
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
