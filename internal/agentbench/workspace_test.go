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
