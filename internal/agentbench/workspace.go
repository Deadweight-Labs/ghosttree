package agentbench

import (
	"fmt"
	"os"
	"path/filepath"
)

type WorkspaceSpec struct {
	RepoSource string
	ClaudeMD   string
	// GhostTreeSource is the pinned .ghosttree mirror for the ghosttree arm.
	// The tree that happens to sit in RepoSource is never used: it is whatever
	// the checkout carried, not the snapshot the campaign declared.
	GhostTreeSource string
	MemorySource    string
	AllowedPath     string
}

type Workspace struct {
	Root string
	Repo string
	Home string
	Env  map[string]string
}

func PrepareWorkspace(root string, arm ArmName, spec WorkspaceSpec) (Workspace, error) {
	// Ein wiederverwendeter Arbeitsbereich traegt die Reste des vorigen Laufs:
	// Dateien, die der Agent angelegt hat, und im schlimmsten Fall den Baum
	// eines anderen Arms. os.CopyFS meldet das als "file exists" — eine
	// Meldung, aus der niemand den Grund liest.
	if entries, err := os.ReadDir(root); err == nil && len(entries) > 0 {
		return Workspace{}, fmt.Errorf(
			"workspace %s already exists and is not empty; a campaign never reuses one, "+
				"because it would carry the previous run's files into this one", root)
	}
	ws := Workspace{
		Root: root,
		Repo: filepath.Join(root, "repo"),
		Home: filepath.Join(root, "home"),
		Env:  map[string]string{},
	}
	for _, dir := range []string{ws.Repo, ws.Home, filepath.Join(ws.Home, ".config")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Workspace{}, err
		}
	}
	if spec.GhostTreeSource != "" && arm != ArmGhosttree {
		return Workspace{}, fmt.Errorf("arm %q was handed a ghost tree it is not entitled to", arm)
	}
	if err := os.CopyFS(ws.Repo, os.DirFS(spec.RepoSource)); err != nil {
		return Workspace{}, err
	}
	// Der Baum aus dem Checkout fliegt immer raus; der ghosttree-Arm bekommt
	// danach den fixierten Stand, nicht den zufaellig mitgekommenen.
	ghostTree := filepath.Join(ws.Repo, ".ghosttree")
	if err := os.RemoveAll(ghostTree); err != nil {
		return Workspace{}, err
	}
	if spec.GhostTreeSource != "" {
		if err := os.CopyFS(ghostTree, os.DirFS(spec.GhostTreeSource)); err != nil {
			return Workspace{}, err
		}
	}
	if err := os.RemoveAll(filepath.Join(ws.Repo, ".claude")); err != nil {
		return Workspace{}, err
	}
	claudeMD := filepath.Join(ws.Repo, "CLAUDE.md")
	if err := os.RemoveAll(claudeMD); err != nil {
		return Workspace{}, err
	}
	if arm != ArmBare && spec.ClaudeMD != "" {
		if err := os.WriteFile(claudeMD, []byte(spec.ClaudeMD), 0o644); err != nil {
			return Workspace{}, err
		}
	}
	if spec.MemorySource != "" {
		if err := os.CopyFS(filepath.Join(ws.Home, ".memory"), os.DirFS(spec.MemorySource)); err != nil {
			return Workspace{}, err
		}
	}
	path := spec.AllowedPath
	if path == "" {
		path = "/usr/bin:/bin"
	}
	ws.Env["HOME"] = ws.Home
	ws.Env["XDG_CONFIG_HOME"] = filepath.Join(ws.Home, ".config")
	ws.Env["PATH"] = path
	ws.Env["AGENTBENCH_ARM"] = string(arm)
	return ws, nil
}
