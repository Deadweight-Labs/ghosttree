package collector

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func realTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(dir, "home")) // keeps the real home out of the walk
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

func link(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ProjectMarker), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAFolderWithoutGitBelongsToItsLinkedProject(t *testing.T) {
	root := realTemp(t)
	infra := mkdirs(t, filepath.Join(root, "infra"))
	link(t, infra, "# linked by ctx project link\ninfra\n")
	sub := mkdirs(t, filepath.Join(infra, "hosts", "mainex"))
	for _, dir := range []string{infra, sub} {
		g := ResolveGitContext(dir)
		if g.Project != "infra" || g.Branch != "" || g.Root != "" || g.RepoPath != "" || len(g.Lineage) != 0 {
			t.Errorf("ResolveGitContext(%s) = %+v", dir, g)
		}
		if p, b := ProjectInfo(dir); p != "infra" || b != "" {
			t.Errorf("ProjectInfo(%s) = %q, %q", dir, p, b)
		}
	}
	// A sibling folder is not part of it.
	if g := ResolveGitContext(mkdirs(t, filepath.Join(root, "other"))); g.Project != "" {
		t.Errorf("sibling resolved to %+v", g)
	}
}

func TestARepoInsideALinkedFolderKeepsItsIdentity(t *testing.T) {
	root := realTemp(t)
	infra := mkdirs(t, filepath.Join(root, "infra"))
	link(t, infra, "infra\n")
	repo := filepath.Join(infra, "tools", "repo")
	initRepoAt(t, repo, "https://github.com/x/y.git")
	deep := mkdirs(t, filepath.Join(repo, "core"))
	g := ResolveGitContext(deep)
	if g.Project != "github.com/x/y" || g.Root != repo || g.RepoPath != "core" {
		t.Errorf("repo in linked folder = %+v", g)
	}
	// A repo without an origin has no identity of its own: the folder's project applies,
	// but it is no repo for the ghost tree.
	bare := filepath.Join(infra, "scratch")
	initRepoAt(t, bare, "")
	if g := ResolveGitContext(bare); g.Project != "infra" || g.Root != "" || g.Branch != "" {
		t.Errorf("repo without origin in linked folder = %+v", g)
	}
}

func TestALinkedFolderInsideARepoOverridesItsSubtree(t *testing.T) {
	root := realTemp(t)
	repo := filepath.Join(root, "repo")
	initRepoAt(t, repo, "https://github.com/x/y.git")
	notes := mkdirs(t, filepath.Join(repo, "notes"))
	link(t, notes, "notes\n")
	sub := mkdirs(t, filepath.Join(notes, "2026"))
	for _, dir := range []string{notes, sub} {
		if g := ResolveGitContext(dir); g.Project != "notes" || g.Root != "" {
			t.Errorf("ResolveGitContext(%s) = %+v", dir, g)
		}
	}
	// The rest of the repository is untouched.
	if g := ResolveGitContext(repo); g.Project != "github.com/x/y" || g.Root != repo {
		t.Errorf("repo root = %+v", g)
	}
	other := mkdirs(t, filepath.Join(repo, "src"))
	if g := ResolveGitContext(other); g.Project != "github.com/x/y" || g.RepoPath != "src" {
		t.Errorf("repo sibling = %+v", g)
	}
}

func TestAMarkerInTheRepoRootWinsOverItsRemote(t *testing.T) {
	root := realTemp(t)
	repo := filepath.Join(root, "repo")
	initRepoAt(t, repo, "https://github.com/x/y.git")
	link(t, repo, "infra\n")
	if g := ResolveGitContext(repo); g.Project != "infra" || g.Root != "" {
		t.Errorf("explicit link in the repo root = %+v", g)
	}
}

func TestTheHomeDirectoryAndBrokenMarkersAreIgnored(t *testing.T) {
	root := realTemp(t)
	home := mkdirs(t, filepath.Join(root, "home"))
	link(t, home, "infra\n")
	work := mkdirs(t, filepath.Join(home, "work"))
	if g := ResolveGitContext(work); g.Project != "" {
		t.Errorf("a marker in $HOME applied: %+v", g)
	}
	for name, content := range map[string]string{
		"empty": "", "comments only": "# nothing\n", "a remote": "github.com/x/y\n",
		"two names": "a\nb\n", "uppercase": "Infra\n",
	} {
		dir := mkdirs(t, filepath.Join(root, "broken", name))
		link(t, dir, content)
		if g := ResolveGitContext(dir); g.Project != "" {
			t.Errorf("marker %q resolved to %+v", name, g)
		}
	}
}

func TestReadProjectMarker(t *testing.T) {
	dir := realTemp(t)
	link(t, dir, "\n# a comment\n\n  sky-go  \n")
	if got, err := ReadProjectMarker(filepath.Join(dir, ProjectMarker)); err != nil || got != "sky-go" {
		t.Errorf("got %q, %v", got, err)
	}
	if _, err := ReadProjectMarker(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing marker must be an error")
	}
}

func TestACollectedSessionInALinkedFolderGetsTheProject(t *testing.T) {
	root := realTemp(t)
	infra := mkdirs(t, filepath.Join(root, "infra"))
	link(t, infra, "infra\n")
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
		"linked folder":          {work, "", "infra", ""},
		"harness branch dropped": {work, "main", "infra", ""},
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

func TestAVanishedFolderStillFindsItsLink(t *testing.T) {
	root := realTemp(t)
	infra := mkdirs(t, filepath.Join(root, "infra"))
	link(t, infra, "infra\n")
	gone := filepath.Join(infra, "deleted", "dir")
	if g := ResolveGitContext(gone); g.Project != "infra" {
		t.Errorf("vanished subfolder = %+v", g)
	}
}
