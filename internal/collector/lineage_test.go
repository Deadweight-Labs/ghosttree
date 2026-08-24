package collector

import (
	"os/exec"
	"slices"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// A repository with main ← develop ← feat/x, so the chain is two deep and can
// be told apart from a single hard-coded parent.
func lineageRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "--initial-branch=main")
	git(t, dir, "commit", "--allow-empty", "-m", "root")
	git(t, dir, "checkout", "-b", "develop")
	git(t, dir, "commit", "--allow-empty", "-m", "develop work")
	git(t, dir, "checkout", "-b", "feat/x")
	git(t, dir, "commit", "--allow-empty", "-m", "feature work")
	return dir
}

func TestBranchLineageComesFromTheRepository(t *testing.T) {
	dir := lineageRepo(t)

	got := BranchLineage(dir, "feat/x")
	for _, want := range []string{"main", "develop"} {
		if !slices.Contains(got, want) {
			t.Errorf("lineage of feat/x = %v, missing %q", got, want)
		}
	}
	if slices.Contains(got, "feat/x") {
		t.Errorf("a branch must not list itself: %v", got)
	}

	git(t, dir, "checkout", "develop")
	got = BranchLineage(dir, "develop")
	if !slices.Contains(got, "main") {
		t.Errorf("lineage of develop = %v, want main", got)
	}
	if slices.Contains(got, "feat/x") {
		t.Errorf("develop must not inherit from a branch cut off it: %v", got)
	}
}

// A sibling is in nobody's chain, which is what keeps deliberate branch scope
// deliberate.
func TestASiblingIsNotAnAncestor(t *testing.T) {
	dir := lineageRepo(t)
	git(t, dir, "checkout", "develop")
	git(t, dir, "checkout", "-b", "feat/y")
	git(t, dir, "commit", "--allow-empty", "-m", "other feature")

	if got := BranchLineage(dir, "feat/y"); slices.Contains(got, "feat/x") {
		t.Errorf("lineage of feat/y = %v, must not contain its sibling", got)
	}
}

// Outside a repository there is no chain to derive, and asking must not fail —
// the session simply reads at project scope.
func TestNoRepositoryYieldsNoChain(t *testing.T) {
	if got := BranchLineage(t.TempDir(), "main"); got != nil {
		t.Errorf("lineage outside a repository = %v, want none", got)
	}
	if got := BranchLineage(lineageRepo(t), ""); got != nil {
		t.Errorf("lineage without a branch = %v, want none", got)
	}
}

func TestResolveGitContextCarriesTheChain(t *testing.T) {
	dir := lineageRepo(t)
	ctx := ResolveGitContext(dir)
	if ctx.Branch != "feat/x" {
		t.Fatalf("branch = %q", ctx.Branch)
	}
	if !slices.Contains(ctx.Lineage, "develop") {
		t.Errorf("git context lineage = %v, want develop in it", ctx.Lineage)
	}
}
