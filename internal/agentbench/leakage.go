package agentbench

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type AllowedSurface struct {
	GhostTree  bool
	ClaudeMD   bool
	AutoMemory bool
	MemoryDir  bool
	// OpenNetwork suppresses the finding that the run can reach hosts beyond
	// the model endpoint. It is never right for a campaign — an arm that can
	// research on the web compensates for a missing memory — and exists so a
	// single exploratory run can be made without a sealed network.
	OpenNetwork bool
}

type LeakageFinding struct {
	Arm    ArmName `json:"arm"`
	Kind   string  `json:"kind"`
	Detail string  `json:"detail"`
}

// pathContains reports whether sub lies at or below root. Both are cleaned
// first, so a trailing slash or a "." segment does not decide the answer.
func pathContains(root, sub string) bool {
	root, sub = filepath.Clean(root), filepath.Clean(sub)
	if root == sub {
		return true
	}
	return strings.HasPrefix(sub, root+string(filepath.Separator))
}

func CheckLeakage(ws Workspace, arm ArmName, allowed AllowedSurface) []LeakageFinding {
	var findings []LeakageFinding
	report := func(kind, detail string) {
		findings = append(findings, LeakageFinding{Arm: arm, Kind: kind, Detail: detail})
	}
	exists := func(parts ...string) bool {
		_, err := os.Stat(filepath.Join(parts...))
		return err == nil
	}

	// Ueberall gesucht, nicht nur an der Wurzel: In einem Monorepo liegt je
	// Unterrepository ein eigener Baum, und ein verschachteltes Gedaechtnis
	// ist genauso ein Gedaechtnis.
	if !allowed.GhostTree {
		if hit := findDirNamed(ws.Repo, ".ghosttree"); hit != "" {
			report("ghost_tree", "the workspace contains a ghost tree at "+hit)
		}
	}
	if !allowed.ClaudeMD && exists(ws.Repo, "CLAUDE.md") {
		report("claude_md", "the workspace contains CLAUDE.md")
	}
	if !allowed.ClaudeMD && exists(ws.Repo, "AGENTS.md") {
		report("agents_md", "the workspace contains AGENTS.md")
	}
	// .claude/ im Repository gehoert zur selben Oberflaeche wie CLAUDE.md:
	// handgepflegte Markdown-Anleitung. Ein Arm, der die eine sehen darf,
	// darf auch die andere sehen — der bare-Arm keins von beidem.
	if !allowed.ClaudeMD {
		if hit := findDirNamed(ws.Repo, ".claude"); hit != "" {
			report("claude_dir", "the workspace contains a .claude directory at "+hit)
		}
	}
	// Vor dem ersten Lauf ist schon das Verzeichnis verdaechtig; waehrend der
	// Kampagne legt Claude Code es selbst an und laesst es leer. Geprueft wird
	// darum, ob Inhalt darin steht — siehe CheckHomeDrift.
	if !allowed.AutoMemory && exists(ws.Home, ".claude", "projects") {
		report("auto_memory", "the home directory contains Claude auto-memory")
	}
	if !allowed.MemoryDir && exists(ws.Home, ".memory") {
		report("memory_dir", "the home directory contains a memory state")
	}
	// Der Wirtsheimatordner traegt die globalen Agentenregeln und die
	// Auto-Memory. Liegt er innerhalb des Arbeitsbereichs, mountet der
	// Container ihn nach /work und alle Arme sehen ihn auf einen Schlag.
	if home, err := os.UserHomeDir(); err == nil && ws.Root != "" && pathContains(ws.Root, home) {
		report("host_home", fmt.Sprintf("the workspace root %s contains the host home %s", ws.Root, home))
	}
	if ws.Env["PATH"] == "" {
		report("path", "PATH is unset, so the host PATH would be inherited")
	}
	if ws.Env["HOME"] == "" {
		report("home", "HOME is unset, so the host home would be inherited")
	} else if ws.Home != "" && ws.Env["HOME"] != ws.Home {
		report("home", fmt.Sprintf("HOME points outside the workspace: %s", ws.Env["HOME"]))
	}
	return findings
}

// CheckHomeDrift asks whether the arm's last run left something behind that its
// next run would read.
//
// One workspace and one HOME serve all of an arm's runs, so a memory written
// during run 3 is context for run 4. An arm that accumulates notes across tasks
// stops being the arm it is named after — `bare` would slowly turn into
// `claude-native` — and the numbers would still look plausible.
//
// It checks for content, not for the directory. Claude Code creates
// ~/.claude/projects/<project>/memory itself and leaves it empty; in 72 pilot
// runs that is exactly what happened. Treating the empty directory as a finding
// would stop every campaign for nothing.
func CheckHomeDrift(ws Workspace, arm ArmName, allowed AllowedSurface) []LeakageFinding {
	if allowed.AutoMemory || ws.Home == "" {
		return nil
	}
	var findings []LeakageFinding
	root := filepath.Join(ws.Home, ".claude", "projects")
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Base(filepath.Dir(path)) != "memory" {
			return nil
		}
		findings = append(findings, LeakageFinding{
			Arm: arm, Kind: "auto_memory_written",
			Detail: "the run left a memory behind that the arm's next run would read: " + path,
		})
		return nil
	})
	return findings
}
