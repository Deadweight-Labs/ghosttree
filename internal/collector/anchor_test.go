package collector

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// anchorGitRun führt git in dir aus und liefert die getrimmte Ausgabe.
func anchorGitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func anchorRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	anchorGitRun(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("eins\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	anchorGitRun(t, dir, "add", "a.txt")
	anchorGitRun(t, dir, "commit", "-q", "-m", "erste")
	return dir
}

func TestResolveAnchorNamesHeadBranchAndCleanTree(t *testing.T) {
	dir := anchorRepo(t)
	head := anchorGitRun(t, dir, "rev-parse", "HEAD")
	a := ResolveAnchor(dir)
	if a.Commit != head || len(a.Commit) < 40 {
		t.Fatalf("commit = %q, want the full HEAD %q", a.Commit, head)
	}
	if a.Branch != "main" || a.Dirty {
		t.Fatalf("branch/dirty = %q/%v, want main/false", a.Branch, a.Dirty)
	}
	if a.MergeBase != "" {
		t.Fatalf("without a remote default branch there is no merge base, got %q", a.MergeBase)
	}
	if a.PR != nil {
		t.Fatal("the client never sets a pull request number")
	}
}

func TestResolveAnchorDirtyOnlyForTrackedChanges(t *testing.T) {
	dir := anchorRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "neu.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ResolveAnchor(dir).Dirty {
		t.Fatal("an untracked file must not make the tree dirty")
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("zwei\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !ResolveAnchor(dir).Dirty {
		t.Fatal("a modified tracked file must make the tree dirty")
	}
}

// Die Merge-Base geht gegen den Default-Branch des Remotes: origin/HEAD, sonst
// origin/main, sonst origin/dev.
func TestResolveAnchorMergeBaseAgainstTheRemoteDefault(t *testing.T) {
	dir := anchorRepo(t)
	base := anchorGitRun(t, dir, "rev-parse", "HEAD")
	anchorGitRun(t, dir, "update-ref", "refs/remotes/origin/dev", base)
	anchorGitRun(t, dir, "switch", "-q", "-c", "feature")
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	anchorGitRun(t, dir, "add", "b.txt")
	anchorGitRun(t, dir, "commit", "-q", "-m", "feature")

	a := ResolveAnchor(dir)
	if a.Branch != "feature" || a.MergeBase != base || a.Commit == base {
		t.Fatalf("origin/dev fallback: %+v, want merge base %s", a, base)
	}

	// origin/main hat Vorrang vor origin/dev.
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("c"), 0o600); err != nil {
		t.Fatal(err)
	}
	anchorGitRun(t, dir, "add", "c.txt")
	anchorGitRun(t, dir, "commit", "-q", "-m", "zwei")
	mid := anchorGitRun(t, dir, "rev-parse", "HEAD")
	anchorGitRun(t, dir, "update-ref", "refs/remotes/origin/main", mid)
	if a := ResolveAnchor(dir); a.MergeBase != mid {
		t.Fatalf("origin/main must win over origin/dev: merge base %q, want %q", a.MergeBase, mid)
	}

	// Ein gesetztes origin/HEAD sagt, was das Remote für seinen Default hält.
	anchorGitRun(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/dev")
	if a := ResolveAnchor(dir); a.MergeBase != base {
		t.Fatalf("origin/HEAD must win over the fallbacks: merge base %q, want %q", a.MergeBase, base)
	}
}

func TestResolveAnchorDetachedHeadHasNoBranch(t *testing.T) {
	dir := anchorRepo(t)
	anchorGitRun(t, dir, "checkout", "-q", "--detach")
	a := ResolveAnchor(dir)
	if a.Commit == "" || a.Branch != "" {
		t.Fatalf("detached head: %+v, want a commit without a branch", a)
	}
}

// Kein Repo, ungeborener Branch, kein Verzeichnis, kein git: leer, nie geraten.
func TestResolveAnchorIsEmptyWhenGitHasNothingToSay(t *testing.T) {
	if a := ResolveAnchor(t.TempDir()); !a.IsZero() {
		t.Fatalf("outside a repository: %+v", a)
	}
	unborn := t.TempDir()
	anchorGitRun(t, unborn, "init", "-q", "-b", "main")
	if a := ResolveAnchor(unborn); !a.IsZero() {
		t.Fatalf("unborn branch: %+v", a)
	}
	if a := ResolveAnchor(filepath.Join(t.TempDir(), "gibt-es-nicht")); !a.IsZero() {
		t.Fatalf("missing directory: %+v", a)
	}
	if a := ResolveAnchor(""); !a.IsZero() {
		t.Fatalf("empty directory: %+v", a)
	}
	dir := anchorRepo(t)
	t.Setenv("PATH", t.TempDir())
	if a := ResolveAnchor(dir); !a.IsZero() {
		t.Fatalf("without git on the PATH: %+v", a)
	}
}

func TestBranchLineageOfNamedBranch(t *testing.T) {
	dir := anchorRepo(t)
	anchorGitRun(t, dir, "switch", "-q", "-c", "dev")
	anchorGitRun(t, dir, "switch", "-q", "-c", "feat/x")
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	anchorGitRun(t, dir, "add", "b.txt")
	anchorGitRun(t, dir, "commit", "-q", "-m", "x")
	anchorGitRun(t, dir, "switch", "-q", "main")

	// Ausgecheckt ist main; gefragt wird nach feat/x.
	got := strings.Join(BranchLineageOf(dir, "feat/x"), ",")
	if !strings.Contains(got, "dev") || !strings.Contains(got, "main") {
		t.Fatalf("lineage of feat/x = %q, want dev and main", got)
	}
	if l := BranchLineageOf(dir, "gibt-es-nicht"); len(l) != 0 {
		t.Fatalf("unknown branch must have no lineage, got %v", l)
	}
}
