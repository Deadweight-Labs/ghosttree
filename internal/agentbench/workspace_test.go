package agentbench

import (
	"os"
	"path/filepath"
	"testing"
)

// repoWithTree builds a source repository that already carries the artefacts a
// wrongly prepared arm would inherit.
func repoWithTree(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, ".ghosttree", "tree"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		".ghosttree/tree/a.md": "ghost",
		"CLAUDE.md":            "alte regeln",
		"main.go":              "package main",
	} {
		if err := os.WriteFile(filepath.Join(src, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return src
}

func TestPrepareWorkspaceStripsTreeAndRulesForBare(t *testing.T) {
	ws, err := PrepareWorkspace(t.TempDir(), ArmBare, WorkspaceSpec{RepoSource: repoWithTree(t)})
	if err != nil {
		t.Fatalf("PrepareWorkspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws.Repo, ".ghosttree")); !os.IsNotExist(err) {
		t.Fatal("bare arm must not see a ghost tree")
	}
	if _, err := os.Stat(filepath.Join(ws.Repo, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatal("bare arm must not see a CLAUDE.md")
	}
	if _, err := os.Stat(filepath.Join(ws.Repo, "main.go")); err != nil {
		t.Fatalf("the repository itself must survive: %v", err)
	}
	if ws.Env["PATH"] == "" || ws.Env["HOME"] != ws.Home {
		t.Fatalf("workspace must pin PATH and HOME: %+v", ws.Env)
	}
}

func TestPrepareWorkspaceWritesTheReconstructedClaudeMD(t *testing.T) {
	ws, err := PrepareWorkspace(t.TempDir(), ArmClaudeNative, WorkspaceSpec{
		RepoSource: repoWithTree(t),
		ClaudeMD:   "rekonstruierte regeln",
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(ws.Repo, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("the arm is entitled to its CLAUDE.md: %v", err)
	}
	if string(body) != "rekonstruierte regeln" {
		t.Fatalf("the repository's own CLAUDE.md must be replaced, got %q", body)
	}
}

func TestPrepareWorkspaceIsolatesHomeFromTheHost(t *testing.T) {
	root := t.TempDir()
	ws, err := PrepareWorkspace(root, ArmGhosttree, WorkspaceSpec{RepoSource: repoWithTree(t)})
	if err != nil {
		t.Fatal(err)
	}
	if ws.Env["XDG_CONFIG_HOME"] != filepath.Join(ws.Home, ".config") {
		t.Fatalf("config home must live inside the workspace: %+v", ws.Env)
	}
	if hostHome := os.Getenv("HOME"); ws.Env["HOME"] == hostHome {
		t.Fatal("workspace HOME must never be the host HOME")
	}
}

func TestPrepareWorkspaceCopiesTheMemoryState(t *testing.T) {
	memory := t.TempDir()
	if err := os.WriteFile(filepath.Join(memory, "state.db"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := PrepareWorkspace(t.TempDir(), ArmGhosttree, WorkspaceSpec{
		RepoSource: repoWithTree(t), MemorySource: memory,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws.Home, ".memory", "state.db")); err != nil {
		t.Fatalf("memory state was not copied in: %v", err)
	}
}

func TestCheckLeakageFindsForeignMemoryInBareArm(t *testing.T) {
	ws, err := PrepareWorkspace(t.TempDir(), ArmBare, WorkspaceSpec{RepoSource: repoWithTree(t)})
	if err != nil {
		t.Fatal(err)
	}
	// Nachtraeglich eingeschleust, so wie es ein falsch gebautes Image taete.
	if err := os.MkdirAll(filepath.Join(ws.Repo, ".ghosttree"), 0o755); err != nil {
		t.Fatal(err)
	}

	findings := CheckLeakage(ws, ArmBare, AllowedSurface{})

	if len(findings) == 0 {
		t.Fatal("a ghost tree inside the bare arm must be reported")
	}
}

func TestCheckLeakageAcceptsAnEntitledSurface(t *testing.T) {
	ws, err := PrepareWorkspace(t.TempDir(), ArmGhosttree, WorkspaceSpec{
		RepoSource: repoWithTree(t), ClaudeMD: "regeln",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws.Repo, ".ghosttree"), 0o755); err != nil {
		t.Fatal(err)
	}

	findings := CheckLeakage(ws, ArmGhosttree, AllowedSurface{GhostTree: true, ClaudeMD: true})

	if len(findings) != 0 {
		t.Fatalf("the ghosttree arm is entitled to its tree and rules: %+v", findings)
	}
}

func TestCheckLeakageReportsAnUnsetPath(t *testing.T) {
	ws := Workspace{Root: t.TempDir(), Env: map[string]string{}}
	findings := CheckLeakage(ws, ArmBare, AllowedSurface{})
	var found bool
	for _, f := range findings {
		if f.Kind == "path" {
			found = true
		}
	}
	if !found {
		t.Fatalf("an unset PATH inherits the host PATH and must be reported: %+v", findings)
	}
}

func TestPrepareWorkspaceGivesTheGhosttreeArmItsTree(t *testing.T) {
	treeSource := t.TempDir()
	if err := os.MkdirAll(filepath.Join(treeSource, "tree"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(treeSource, "INDEX.md"), []byte("index"), 0o644); err != nil {
		t.Fatal(err)
	}

	ws, err := PrepareWorkspace(t.TempDir(), ArmGhosttree, WorkspaceSpec{
		RepoSource: repoWithTree(t), GhostTreeSource: treeSource,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Der Arm, der ohne Baum nichts misst, muss ihn bekommen — und zwar den
	// mitgegebenen Stand, nicht den zufaellig im Repo liegenden.
	body, err := os.ReadFile(filepath.Join(ws.Repo, ".ghosttree", "INDEX.md"))
	if err != nil {
		t.Fatalf("the ghosttree arm must receive its tree: %v", err)
	}
	if string(body) != "index" {
		t.Fatalf("the tree must come from the pinned source, got %q", body)
	}
}

func TestPrepareWorkspaceRefusesATreeForAnotherArm(t *testing.T) {
	treeSource := t.TempDir()
	if err := os.WriteFile(filepath.Join(treeSource, "INDEX.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := PrepareWorkspace(t.TempDir(), ArmClaudeNative, WorkspaceSpec{
		RepoSource: repoWithTree(t), GhostTreeSource: treeSource,
	})

	if err == nil {
		t.Fatal("handing a ghost tree to a non-ghosttree arm is a configuration error, not a silent no-op")
	}
}

func TestCheckLeakageRefusesAWorkspaceThatSwallowsTheHostHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no host home to test against")
	}
	// Der Container mountet Root nach /work. Waere das der Heimatordner,
	// saehe jeder Arm das globale CLAUDE.md und die Auto-Memory.
	ws := Workspace{Root: home, Repo: filepath.Join(home, "repo"), Home: filepath.Join(home, "home"),
		Env: map[string]string{"PATH": "/usr/bin", "HOME": filepath.Join(home, "home")}}

	findings := CheckLeakage(ws, ArmBare, AllowedSurface{})

	var caught bool
	for _, f := range findings {
		if f.Kind == "host_home" {
			caught = true
		}
	}
	if !caught {
		t.Fatalf("a workspace containing the host home must be refused: %+v", findings)
	}
}

func TestCheckLeakageAcceptsAWorkspaceBelowTheHostHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no host home to test against")
	}
	root := filepath.Join(home, "runs", "pilot", "ws-bare")
	ws := Workspace{Root: root, Repo: filepath.Join(root, "repo"), Home: filepath.Join(root, "home"),
		Env: map[string]string{"PATH": "/usr/bin", "HOME": filepath.Join(root, "home")}}

	for _, f := range CheckLeakage(ws, ArmBare, AllowedSurface{}) {
		if f.Kind == "host_home" {
			t.Fatalf("a directory inside the home is the normal place to run: %+v", f)
		}
	}
}

// TestPrepareWorkspaceGivesTheMarkdownArmItsClaudeDirectory guards the control
// arm's honest baseline. Robcord keeps a hand-written .claude/wiki of 1825
// lines that nobody generated; deleting it would measure a repository poorer
// than the one that exists, and the treatment arm would look better for it.
func TestPrepareWorkspaceGivesTheMarkdownArmItsClaudeDirectory(t *testing.T) {
	src := repoWithTree(t)
	wiki := filepath.Join(src, ".claude", "wiki")
	if err := os.MkdirAll(wiki, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wiki, "arch.md"), []byte("handgeschrieben"), 0o644); err != nil {
		t.Fatal(err)
	}

	ws, err := PrepareWorkspace(t.TempDir(), ArmClaudeMD, WorkspaceSpec{
		RepoSource: src, ClaudeDirSource: filepath.Join(src, ".claude"),
	})
	if err != nil {
		t.Fatalf("PrepareWorkspace: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(ws.Repo, ".claude", "wiki", "arch.md"))
	if err != nil || string(body) != "handgeschrieben" {
		t.Fatalf("the markdown arm must keep its .claude directory: %v", err)
	}
	if findings := CheckLeakage(ws, ArmClaudeMD, AllowedSurface{ClaudeMD: true}); len(findings) > 0 {
		t.Fatalf("an entitled .claude directory is not a leak: %+v", findings)
	}
}

func TestPrepareWorkspaceRefusesAClaudeDirectoryForBare(t *testing.T) {
	src := repoWithTree(t)
	if err := os.MkdirAll(filepath.Join(src, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := PrepareWorkspace(t.TempDir(), ArmBare, WorkspaceSpec{
		RepoSource: src, ClaudeDirSource: filepath.Join(src, ".claude"),
	})
	if err == nil {
		t.Fatal("the bare arm must not be handed a .claude directory")
	}
}

// TestPrepareWorkspaceStripsTheClaudeDirectoryWhenNoneIsGranted keeps the
// default honest: what the checkout carried never survives by accident.
func TestPrepareWorkspaceStripsTheClaudeDirectoryWhenNoneIsGranted(t *testing.T) {
	src := repoWithTree(t)
	if err := os.MkdirAll(filepath.Join(src, ".claude", "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := PrepareWorkspace(t.TempDir(), ArmBare, WorkspaceSpec{RepoSource: src})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws.Repo, ".claude")); !os.IsNotExist(err) {
		t.Fatal("without an explicit grant the checkout's .claude must go")
	}
}

// TestPrepareWorkspaceStripsNestedMemories is the monorepo case. Robcord holds
// six repositories side by side, each with its own ghost tree; emptying only the
// top level would have handed every arm — including bare — five complete
// memories, and the leakage check, which also looked only at the top, would
// have called that clean.
func TestPrepareWorkspaceStripsNestedMemories(t *testing.T) {
	src := repoWithTree(t)
	for _, dir := range []string{
		filepath.Join(src, "sub-a", ".ghosttree", "tree"),
		filepath.Join(src, "sub-b", ".claude", "wiki"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte("geheim"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ws, err := PrepareWorkspace(t.TempDir(), ArmBare, WorkspaceSpec{RepoSource: src})
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{
		filepath.Join(ws.Repo, "sub-a", ".ghosttree"),
		filepath.Join(ws.Repo, "sub-b", ".claude"),
	} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("a nested memory survived: %s", gone)
		}
	}
	if findings := CheckLeakage(ws, ArmBare, AllowedSurface{}); len(findings) > 0 {
		t.Fatalf("nothing should be left to find: %+v", findings)
	}
}

// TestCheckLeakageFindsANestedGhostTree proves the check itself, not just the
// cleanup: a tree two levels down must be reported, or the guard is decorative.
func TestCheckLeakageFindsANestedGhostTree(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "sub", ".ghosttree", "tree"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws := Workspace{Root: root, Repo: repo, Home: filepath.Join(root, "home"),
		Env: map[string]string{"PATH": "/usr/bin", "HOME": filepath.Join(root, "home")}}

	findings := CheckLeakage(ws, ArmBare, AllowedSurface{})
	var caught bool
	for _, f := range findings {
		if f.Kind == "ghost_tree" {
			caught = true
		}
	}
	if !caught {
		t.Fatalf("a nested ghost tree must be reported: %+v", findings)
	}
}
