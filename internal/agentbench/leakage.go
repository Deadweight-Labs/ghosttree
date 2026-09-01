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

	if !allowed.GhostTree && exists(ws.Repo, ".ghosttree") {
		report("ghost_tree", "the workspace contains .ghosttree")
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
	if !allowed.ClaudeMD && exists(ws.Repo, ".claude") {
		report("claude_dir", "the workspace contains .claude")
	}
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
