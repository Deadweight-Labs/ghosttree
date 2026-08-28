package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHarnessDocumentationCoversScopedInstallAndEvidenceStates(t *testing.T) {
	root := filepath.Join("..", "..")
	paths := []string{
		"README.md",
		"deploy/README.md",
		"skills/ghosttree-setup/SKILL.md",
		"skills/ghosttree-setup/references/verification.md",
	}
	var combined strings.Builder
	for _, rel := range paths {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		combined.Write(raw)
	}
	text := combined.String()
	for _, want := range []string{
		"ctx install opencode",
		"ctx install codex --only hooks",
		"ctx doctor codex --only mcp",
		"UNVERIFIED",
		"/hooks",
		"fresh session",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("documentation missing %q", want)
		}
	}
	for _, stale := range []string{"Status: V0", "Quiet means:"} {
		if strings.Contains(text, stale) {
			t.Errorf("documentation still contains %q", stale)
		}
	}
}
