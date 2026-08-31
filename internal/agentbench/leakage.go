package agentbench

import (
	"fmt"
	"os"
	"path/filepath"
)

type AllowedSurface struct {
	GhostTree  bool
	ClaudeMD   bool
	AutoMemory bool
	MemoryDir  bool
}

type LeakageFinding struct {
	Arm    ArmName `json:"arm"`
	Kind   string  `json:"kind"`
	Detail string  `json:"detail"`
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
	if exists(ws.Repo, ".claude", "rules") {
		report("rules", "the workspace contains .claude/rules")
	}
	if !allowed.AutoMemory && exists(ws.Home, ".claude", "projects") {
		report("auto_memory", "the home directory contains Claude auto-memory")
	}
	if !allowed.MemoryDir && exists(ws.Home, ".memory") {
		report("memory_dir", "the home directory contains a memory state")
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
