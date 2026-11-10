package mcpserver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestNormalizeGhostPathAcceptsRepoRelativeAndRejectsEscapes(t *testing.T) {
	ok := map[string]string{
		".":                             "",
		"internal/store":                "internal/store",
		"internal/store/knowledge.go":   "internal/store/knowledge.go",
		"./internal/store/knowledge.go": "internal/store/knowledge.go",
	}
	for in, want := range ok {
		got, err := normalizeGhostPath(in)
		if err != nil {
			t.Fatalf("normalizeGhostPath(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("normalizeGhostPath(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"/etc/passwd", "../ausserhalb", "a\\b", ".ghosttree/tree/x.md"} {
		if _, err := normalizeGhostPath(bad); err == nil {
			t.Fatalf("normalizeGhostPath(%q) must be rejected", bad)
		}
	}
}

// Der Baum hat die Form von `git ls-files`. Eine Beschreibung fuer etwas, das
// dort nicht vorkommt, wuerde geschrieben, ausgeliefert — und waere im Baum
// nirgends zu finden. Lieber beim Schreiben nein sagen als still verlieren.
func TestTrackedInRepoRejectsWhatTheTreeCannotShow(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	for _, name := range []string{"versioniert.go", "ignoriert.log"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "versioniert.go")
	run("commit", "-m", "i")
	if err := os.WriteFile(filepath.Join(repo, "neu.go"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := trackedInRepo(repo, "versioniert.go"); err != nil {
		t.Fatalf("eine versionierte Datei muss beschreibbar sein: %v", err)
	}
	for _, bad := range []string{"neu.go", "ignoriert.log"} {
		err := trackedInRepo(repo, bad)
		if err == nil {
			t.Fatalf("%q ist nicht versioniert und muss abgewiesen werden", bad)
		}
		if !strings.Contains(err.Error(), "versioniert") {
			t.Fatalf("die Meldung muss den Grund nennen, got: %v", err)
		}
	}
}

// Fuenfzehn Beschreibungen hintereinander beantworteten fuenfzehnmal mit
// demselben Satz. Genau das Rauschen, gegen das die Auslieferung mit ihrer
// Einmal-je-Sitzung-Regel antritt — der Schreibpfad hatte sie nicht.
func TestTheSameNudgeIsNotRepeatedWithinASession(t *testing.T) {
	s := &Server{}
	if !s.firstMentionOf("internal") {
		t.Fatal("beim ersten Mal wird genannt")
	}
	if s.firstMentionOf("internal") {
		t.Fatal("beim zweiten Mal nicht mehr")
	}
	if !s.firstMentionOf("cmd") {
		t.Fatal("ein anderer Pfad ist ein anderer Hinweis")
	}
}

// Das go-sdk behandelt Werkzeugaufrufe asynchron (jsonrpc2.Async in
// server.go), zwei handleDescribe koennen also gleichzeitig laufen. Eine
// ungeschuetzte Map darunter ist kein theoretisches Rennen, sondern ein
// "concurrent map writes"-Absturz mitten in einer Sitzung. Mit -race gefunden.
func TestFirstMentionOfSurvivesConcurrentCalls(t *testing.T) {
	s := &Server{}
	var wg sync.WaitGroup
	var first int64
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.firstMentionOf("internal") {
				atomic.AddInt64(&first, 1)
			}
		}()
	}
	wg.Wait()
	if first != 1 {
		t.Fatalf("genau ein Aufrufer darf den Zuschlag bekommen, waren %d", first)
	}
}

func TestRenderGhostHitIsOneLineAndNamesThePath(t *testing.T) {
	g := store.GhostFile{Path: "internal/store/knowledge.go", Kind: "file",
		Description: "Rangfolge nach Vertrauen.\nZweite Zeile, die nicht umbrechen darf."}
	got := renderGhostHit(g)
	if strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
		t.Fatalf("a hit is exactly one line: %q", got)
	}
	if !strings.Contains(got, "internal/store/knowledge.go") {
		t.Fatalf("the path is the point of the hit: %q", got)
	}
	if !strings.Contains(got, "Zweite Zeile") {
		t.Fatalf("the snippet must survive the newline: %q", got)
	}
}

// context_get mit Pfaden folgt dem Branch der Sitzung: auf feat/a steht dessen
// Beschreibung, auf einem anderen Branch die der Projektebene (REQ-597).
func TestGetWithPathsFollowsTheBranchOfTheSession(t *testing.T) {
	c, st := newTestClient(t)
	project := "github.com/x/y"
	for _, g := range []store.GhostFile{
		{Project: project, Path: "internal/a.go", Description: "Beschreibung der Projektebene"},
		{Project: project, Path: "internal/a.go", Description: "Beschreibung nur auf feat/a", Branch: "feat/a"},
		{Project: project, Path: "internal/nur.go", Description: "Pfad nur auf feat/a", Branch: "feat/a"},
	} {
		if _, err := st.PutGhostFile(g); err != nil {
			t.Fatal(err)
		}
	}
	get := func(branch string, lineage []string) string {
		t.Helper()
		s := NewServer(c, scope.Axes{Project: project, Branch: branch, Lineage: lineage})
		res, _, err := s.handleGet(context.Background(), nil, GetInput{Paths: []string{"internal/a.go", "internal/nur.go"}})
		if err != nil {
			t.Fatal(err)
		}
		return text(t, res)
	}
	onBranch := get("feat/a", nil)
	if !strings.Contains(onBranch, "Beschreibung nur auf feat/a") || !strings.Contains(onBranch, "Pfad nur auf feat/a") || strings.Contains(onBranch, "Beschreibung der Projektebene") {
		t.Errorf("on the branch: %s", onBranch)
	}
	cut := get("feat/a-x", []string{"feat/a"})
	if !strings.Contains(cut, "Beschreibung nur auf feat/a") {
		t.Errorf("a branch cut from it inherits: %s", cut)
	}
	sibling := get("feat/b", nil)
	if !strings.Contains(sibling, "Beschreibung der Projektebene") || strings.Contains(sibling, "feat/a") || strings.Contains(sibling, "Pfad nur auf feat/a") {
		t.Errorf("a sibling reads the project level: %s", sibling)
	}
}

// An agent sent into another worktree of the repository described its files
// by their relative path and was told they do not exist: the server looks in
// the checkout it started in. The absolute path names the worktree, and with
// it the branch the description belongs to.
func TestDescribeFindsAFileInAnotherWorktreeOfTheRepository(t *testing.T) {
	base := t.TempDir()
	if real, err := filepath.EvalSymlinks(base); err == nil {
		base = real
	}
	repo, tree := filepath.Join(base, "repo"), filepath.Join(base, "tree")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run(repo, "init", "-b", "main")
	run(repo, "remote", "add", "origin", "https://github.com/x/y.git")
	write(filepath.Join(repo, "first.go"))
	run(repo, "add", ".")
	run(repo, "commit", "-m", "first")
	run(repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	run(repo, "worktree", "add", "-b", "feat/tree", tree)
	write(filepath.Join(tree, "second.go"))
	run(tree, "add", ".")
	run(tree, "commit", "-m", "second")

	c, st := newTestClient(t)
	project := "github.com/x/y"
	s := NewServer(c, scope.Axes{Project: project})
	s.SetRepoRoot(repo)
	describe := func(path string) (string, error) {
		res, _, err := s.handleDescribe(context.Background(), nil, DescribeInput{Path: path, Description: "reads the second half"})
		if err != nil {
			return "", err
		}
		return text(t, res), nil
	}

	_, err := describe("second.go")
	if err == nil || !strings.Contains(err.Error(), "absolute path") || !strings.Contains(err.Error(), repo) {
		t.Fatalf("the relative path of a file in the other worktree must say where it was looked for and what to pass: %v", err)
	}
	if out, err := describe(filepath.Join(tree, "second.go")); err != nil || !strings.Contains(out, "second.go") {
		t.Fatalf("the absolute path: %v %s", err, out)
	}
	g, err := st.GhostFileAt(project, "second.go", "feat/tree", nil)
	if err != nil || g.Description != "reads the second half" || g.Branch != "feat/tree" {
		t.Fatalf("the description belongs to the branch of the worktree: %+v %v", g, err)
	}
	if g, err := st.GhostFileAt(project, "second.go", "", nil); err == nil && g.Description != "" {
		t.Fatalf("the description must not land on the project level: %+v", g)
	}
	// The absolute path of a file in the first checkout is the same as its
	// relative one.
	if out, err := describe(filepath.Join(repo, "first.go")); err != nil || !strings.Contains(out, "first.go") {
		t.Fatalf("the absolute path in the first checkout: %v %s", err, out)
	}

	other := filepath.Join(base, "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	run(other, "init", "-b", "main")
	run(other, "remote", "add", "origin", "https://github.com/x/z.git")
	write(filepath.Join(other, "third.go"))
	run(other, "add", ".")
	run(other, "commit", "-m", "third")
	if _, err := describe(filepath.Join(other, "third.go")); err == nil || !strings.Contains(err.Error(), "github.com/x/z") {
		t.Fatalf("a file of another project must be refused by name: %v", err)
	}
}
