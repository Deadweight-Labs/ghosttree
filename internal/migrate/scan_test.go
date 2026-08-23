package migrate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
)

func TestScanFindsArtifactsAndSkipsToolState(t *testing.T) {
	repo := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(repo, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("# x"), 0o644)
	}
	for _, rel := range []string{"CLAUDE.md", "AGENTS.md", "core/AGENTS.md", "ui/CLAUDE.md", "vendor/pkg/AGENTS.md", "node_modules/x/CLAUDE.md", "deps/x/AGENTS.md", "_build/x/CLAUDE.md", "docs/superpowers/specs/2026-01-01-thing-design.md", "docs/superpowers/plans/2026-01-02-thing.md", ".superpowers/sdd/x/progress.md", ".claude/settings.local.json", ".git/AGENTS.md", "README.md"} {
		write(rel)
	}
	got, err := Scan(repo)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, a := range got {
		kinds[a.Rel] = a.Kind
	}
	want := map[string]string{"CLAUDE.md": "rules", "AGENTS.md": "rules", "docs/superpowers/specs/2026-01-01-thing-design.md": "spec", "docs/superpowers/plans/2026-01-02-thing.md": "plan"}
	for rel, kind := range want {
		if kinds[rel] != kind {
			t.Errorf("%s=%q want %q", rel, kinds[rel], kind)
		}
	}
	for _, rel := range []string{".superpowers/sdd/x/progress.md", ".claude/settings.local.json", ".git/config", "README.md"} {
		if _, ok := kinds[rel]; ok {
			t.Errorf("must skip %s", rel)
		}
	}
	gates := map[string]activation.Rule{}
	for _, a := range got {
		if a.Kind == "rules" {
			gates[a.Rel] = a.Activation
		}
	}
	if len(gates["CLAUDE.md"].Paths) != 0 {
		t.Fatalf("root activation = %+v", gates["CLAUDE.md"])
	}
	for rel, want := range map[string]string{"core/AGENTS.md": "core/**", "ui/CLAUDE.md": "ui/**"} {
		if len(gates[rel].Paths) != 1 || gates[rel].Paths[0] != want {
			t.Errorf("%s activation = %+v, want %s", rel, gates[rel], want)
		}
	}
	for _, rel := range []string{"vendor/pkg/AGENTS.md", "node_modules/x/CLAUDE.md", "deps/x/AGENTS.md", "_build/x/CLAUDE.md", ".git/AGENTS.md"} {
		if _, ok := gates[rel]; ok {
			t.Errorf("must skip nested third-party rule %s", rel)
		}
	}
}
