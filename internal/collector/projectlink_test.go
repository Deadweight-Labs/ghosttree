package collector

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/projectlink"
)

func realTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "home", ".config"))
	return dir
}

func mkdirs(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func initRepoAt(t *testing.T, dir, origin string) {
	t.Helper()
	mkdirs(t, dir)
	args := [][]string{{"init", dir}}
	if origin != "" {
		args = append(args, []string{"-C", dir, "remote", "add", "origin", origin})
	}
	for _, a := range args {
		if out, err := exec.Command("git", a...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", a, err, out)
		}
	}
}

func link(t *testing.T, dir, id string) {
	t.Helper()
	if _, err := projectlink.Set(dir, id); err != nil {
		t.Fatal(err)
	}
}

func TestAFolderWithoutGitBelongsToItsLinkedProject(t *testing.T) {
	root := realTemp(t)
	infra := mkdirs(t, filepath.Join(root, "infra"))
	link(t, infra, "acme/infra")
	sub := mkdirs(t, filepath.Join(infra, "hosts", "mainex"))
	for _, dir := range []string{infra, sub} {
		g := ResolveGitContext(dir)
		if g.Project != "acme/infra" || g.Branch != "" || g.Root != "" || g.RepoPath != "" || len(g.Lineage) != 0 {
			t.Errorf("ResolveGitContext(%s) = %+v", dir, g)
		}
		if p, b := ProjectInfo(dir); p != "acme/infra" || b != "" {
			t.Errorf("ProjectInfo(%s) = %q, %q", dir, p, b)
		}
	}
	if g := ResolveGitContext(mkdirs(t, filepath.Join(root, "other"))); g.Project != "" {
		t.Errorf("sibling resolved to %+v", g)
	}
}

func TestARepoInsideALinkedFolderKeepsItsIdentity(t *testing.T) {
	root := realTemp(t)
	infra := mkdirs(t, filepath.Join(root, "infra"))
	link(t, infra, "acme/infra")
	repo := filepath.Join(infra, "tools", "repo")
	initRepoAt(t, repo, "https://github.com/x/y.git")
	deep := mkdirs(t, filepath.Join(repo, "core"))
	g := ResolveGitContext(deep)
	if g.Project != "github.com/x/y" || g.Root != repo || g.RepoPath != "core" {
		t.Errorf("repo in linked folder = %+v", g)
	}
	// A repo without an origin has no identity of its own: the folder's project
	// applies, but it is no repo for the ghost tree.
	bare := filepath.Join(infra, "scratch")
	initRepoAt(t, bare, "")
	if g := ResolveGitContext(bare); g.Project != "acme/infra" || g.Root != "" || g.Branch != "" {
		t.Errorf("repo without origin in linked folder = %+v", g)
	}
}

func TestALinkedFolderInsideARepoOverridesItsSubtree(t *testing.T) {
	root := realTemp(t)
	repo := filepath.Join(root, "repo")
	initRepoAt(t, repo, "https://github.com/x/y.git")
	notes := mkdirs(t, filepath.Join(repo, "notes"))
	link(t, notes, "acme/notes")
	sub := mkdirs(t, filepath.Join(notes, "2026"))
	for _, dir := range []string{notes, sub} {
		if g := ResolveGitContext(dir); g.Project != "acme/notes" || g.Root != "" {
			t.Errorf("ResolveGitContext(%s) = %+v", dir, g)
		}
	}
	if g := ResolveGitContext(repo); g.Project != "github.com/x/y" || g.Root != repo {
		t.Errorf("repo root = %+v", g)
	}
	other := mkdirs(t, filepath.Join(repo, "src"))
	if g := ResolveGitContext(other); g.Project != "github.com/x/y" || g.RepoPath != "src" {
		t.Errorf("repo sibling = %+v", g)
	}
}

// A path that is itself a repository root and was linked locally wins: the user
// confirmed it on this machine.
func TestALinkedRepoRootWinsOverItsRemote(t *testing.T) {
	root := realTemp(t)
	repo := filepath.Join(root, "repo")
	initRepoAt(t, repo, "https://github.com/x/y.git")
	link(t, repo, "acme/infra")
	if g := ResolveGitContext(repo); g.Project != "acme/infra" || g.Root != "" {
		t.Errorf("explicit link on the repo root = %+v", g)
	}
}

// Nothing inside a folder or repository decides the assignment: a committed
// marker file from someone else's repository must change nothing.
func TestAFileInsideTheFolderNeverAssignsAProject(t *testing.T) {
	root := realTemp(t)
	repo := filepath.Join(root, "foreign")
	initRepoAt(t, repo, "https://github.com/evil/repo.git")
	for _, name := range []string{".ghosttree-project", ".ghosttree-link", "links.json"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("acme/infra\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if g := ResolveGitContext(repo); g.Project != "github.com/evil/repo" {
		t.Errorf("a file in the repo moved the session: %+v", g)
	}
	plain := mkdirs(t, filepath.Join(root, "plain"))
	if err := os.WriteFile(filepath.Join(plain, ".ghosttree-project"), []byte("acme/infra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if g := ResolveGitContext(plain); g.Project != "" {
		t.Errorf("a marker file assigned a project: %+v", g)
	}
}

func TestTheHomeDirectoryAndBrokenEntriesAreIgnored(t *testing.T) {
	root := realTemp(t)
	home := mkdirs(t, filepath.Join(root, "home"))
	work := mkdirs(t, filepath.Join(home, "work"))
	if _, err := projectlink.Set(home, "acme/infra"); err == nil {
		t.Fatal("linking the home directory must be refused")
	}
	if _, err := projectlink.Set("/", "acme/infra"); err == nil {
		t.Fatal("linking / must be refused")
	}
	// An entry that got there by other means is ignored as well.
	broken := mkdirs(t, filepath.Join(root, "broken"))
	reg := `{"links":{"` + home + `":"acme/infra","` + broken + `":"Not A Project","relative/path":"acme/infra","/":"acme/infra"}}`
	if err := os.MkdirAll(filepath.Dir(projectlink.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectlink.Path(), []byte(reg), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{work, broken} {
		if g := ResolveGitContext(dir); g.Project != "" {
			t.Errorf("%s resolved to %+v", dir, g)
		}
	}
	if err := os.WriteFile(projectlink.Path(), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if g := ResolveGitContext(work); g.Project != "" {
		t.Errorf("a broken registry assigned %+v", g)
	}
}

func TestALinkThroughASymlinkResolvesToTheSameFolder(t *testing.T) {
	root := realTemp(t)
	infra := mkdirs(t, filepath.Join(root, "infra"))
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(infra, alias); err != nil {
		t.Fatal(err)
	}
	link(t, alias, "acme/infra")
	if g := ResolveGitContext(mkdirs(t, filepath.Join(infra, "x"))); g.Project != "acme/infra" {
		t.Errorf("real path = %+v", g)
	}
	if g := ResolveGitContext(filepath.Join(alias, "x")); g.Project != "acme/infra" {
		t.Errorf("symlinked path = %+v", g)
	}
}

func TestACollectedSessionInALinkedFolderGetsTheProject(t *testing.T) {
	root := realTemp(t)
	infra := mkdirs(t, filepath.Join(root, "infra"))
	link(t, infra, "acme/infra")
	work := mkdirs(t, filepath.Join(infra, "hosts"))
	repo := filepath.Join(root, "repo")
	initRepoAt(t, repo, "https://github.com/x/y.git")
	inRepo := filepath.Join(infra, "app")
	initRepoAt(t, inRepo, "https://github.com/x/app.git")

	for name, c := range map[string]struct {
		cwd, branch string
		wantProject string
		wantBranch  string
	}{
		"linked folder":          {work, "", "acme/infra", ""},
		"harness branch dropped": {work, "main", "acme/infra", ""},
		"repo in linked folder":  {inRepo, "main", "github.com/x/app", "main"},
		"plain repo":             {repo, "dev", "github.com/x/y", "dev"},
	} {
		fp := filepath.Join(t.TempDir(), "s.jsonl")
		line := `{"type":"user","cwd":"` + c.cwd + `","gitBranch":"` + c.branch + `","sessionId":"s","message":{"role":"user","content":"hi"}}`
		if err := os.WriteFile(fp, []byte(line+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		up := &fakeUp{}
		if err := SyncFile(fp, "claude-code", up, newTestState(t.TempDir()), "box"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := up.sessions[0].Scope
		if got.Project != c.wantProject || got.Branch != c.wantBranch {
			t.Errorf("%s: session scope = %+v, want %s/%s", name, got, c.wantProject, c.wantBranch)
		}
	}
}

// A Codex transcript carries the git remote of its working directory. The local
// link still wins where it is the nearest boundary.
func TestACodexSessionInALinkedFolderGetsTheLinkedProject(t *testing.T) {
	root := realTemp(t)
	infra := mkdirs(t, filepath.Join(root, "infra"))
	link(t, infra, "acme/infra")
	if p, b := projectOfSession(infra, "github.com/embedded/remote", "main"); p != "acme/infra" || b != "" {
		t.Errorf("projectOfSession = %q, %q", p, b)
	}
	plain := mkdirs(t, filepath.Join(root, "plain"))
	if p, b := projectOfSession(plain, "github.com/embedded/remote", "main"); p != "github.com/embedded/remote" || b != "main" {
		t.Errorf("no link: %q, %q", p, b)
	}
}

func TestAVanishedFolderStillFindsItsLink(t *testing.T) {
	root := realTemp(t)
	infra := mkdirs(t, filepath.Join(root, "infra"))
	link(t, infra, "acme/infra")
	gone := filepath.Join(infra, "deleted", "dir")
	if g := ResolveGitContext(gone); g.Project != "acme/infra" {
		t.Errorf("vanished subfolder = %+v", g)
	}
}
