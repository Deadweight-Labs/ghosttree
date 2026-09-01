package agentbench

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCheckHomeDriftAcceptsTheEmptyDirectory keeps the guard from stopping
// every campaign. Claude Code creates the memory directory itself; in 72 pilot
// runs it stayed empty, and an empty directory is not a memory.
func TestCheckHomeDriftAcceptsTheEmptyDirectory(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude", "projects", "-work-repo", "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws := Workspace{Home: home}
	if findings := CheckHomeDrift(ws, ArmBare, AllowedSurface{}); len(findings) > 0 {
		t.Fatalf("an empty memory directory is not a leak: %+v", findings)
	}
}

// TestCheckHomeDriftCatchesAMemoryWrittenMidCampaign is the hazard itself: one
// HOME serves all of an arm's runs, so a note written during run 3 is context
// for run 4 and `bare` quietly becomes `claude-native`.
func TestCheckHomeDriftCatchesAMemoryWrittenMidCampaign(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "-work-repo", "memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("- irgendwas"), 0o644); err != nil {
		t.Fatal(err)
	}
	findings := CheckHomeDrift(Workspace{Home: home}, ArmBare, AllowedSurface{})
	if len(findings) != 1 || findings[0].Kind != "auto_memory_written" {
		t.Fatalf("a memory written mid-campaign must be caught: %+v", findings)
	}
}

// TestCheckHomeDriftLeavesTheEntitledArmAlone: for claude-native the memory is
// the treatment, not a leak.
func TestCheckHomeDriftLeavesTheEntitledArmAlone(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "-work-repo", "memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("- x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if findings := CheckHomeDrift(Workspace{Home: home}, ArmClaudeNative,
		AllowedSurface{AutoMemory: true}); len(findings) > 0 {
		t.Fatalf("for the arm whose treatment this is, it is not a leak: %+v", findings)
	}
}
