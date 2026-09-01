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
	// ClaudeDirSource is the .claude directory the arm is entitled to. It
	// belongs to the same surface as CLAUDE.md: hand-kept markdown the project
	// really carries. Deleting it unconditionally would take the claudemd arm
	// exactly the baseline the comparison is against — Robcord keeps a
	// 1825-line .claude/wiki, and a control arm measured without it flatters
	// the treatment.
	ClaudeDirSource string
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
	//
	// Gesucht wird ueberall, nicht nur an der Wurzel: Ein Monorepo traegt je
	// Unterrepository einen eigenen Baum (Robcord hat sechs). Nur die Wurzel
	// zu leeren hiesse, jedem Arm — auch bare — fuenf vollstaendige
	// Ghost-Baeume mitzugeben, und die Leckpruefung faende nichts.
	ghostTree := filepath.Join(ws.Repo, ".ghosttree")
	if err := removeDirsNamed(ws.Repo, ".ghosttree"); err != nil {
		return Workspace{}, err
	}
	if spec.GhostTreeSource != "" {
		if err := os.CopyFS(ghostTree, os.DirFS(spec.GhostTreeSource)); err != nil {
			return Workspace{}, err
		}
	}
	// Wie beim Ghost-Baum: was der Checkout mitbrachte, fliegt immer raus, und
	// nur der berechtigte Arm bekommt danach den fixierten Stand zurueck.
	claudeDir := filepath.Join(ws.Repo, ".claude")
	if err := removeDirsNamed(ws.Repo, ".claude"); err != nil {
		return Workspace{}, err
	}
	if spec.ClaudeDirSource != "" {
		if arm == ArmBare {
			return Workspace{}, fmt.Errorf("arm %q was handed a .claude directory it is not entitled to", arm)
		}
		if err := os.CopyFS(claudeDir, os.DirFS(spec.ClaudeDirSource)); err != nil {
			return Workspace{}, err
		}
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

// removeDirsNamed deletes every directory with this name below root, not only
// the one at the top. A monorepo carries one memory per sub-repository; taking
// only the top one away leaves the rest in place for every arm, and the leakage
// check — which also looked only at the root — would call that clean.
//
// .git is skipped: a repository may well have an object path that looks like
// the name being removed, and the history is deliberately left intact.
func removeDirsNamed(root, name string) error {
	var found []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if entry.Name() == name {
			found = append(found, path)
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, path := range found {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

// findDirNamed reports the first directory with this name below root, or "".
// The leakage check uses it for the same reason PrepareWorkspace uses
// removeDirsNamed: a nested memory is still a memory.
func findDirNamed(root, name string) string {
	var hit string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return nil //nolint:nilerr // ein unlesbarer Pfad ist kein Fund
		}
		if entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if entry.Name() == name {
			hit = path
			return filepath.SkipAll
		}
		return nil
	})
	return hit
}
