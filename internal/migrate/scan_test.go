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

// Ein eingebetteter Arbeitsbaum ist ein zweiter Checkout desselben Repos, kein
// Fundus. Gefunden am 2026-08-25 an SampleProxy: dort liegt unter
// .claude/worktrees/release-0.5.0 ein vollständiger Checkout, und der Scan
// destillierte dessen CLAUDE.md ein zweites Mal — bezahlt, doppelt abgelegt,
// und beim Bereinigen hätte er eine Datei aus einem fremden Arbeitsbaum
// entfernt. Der Grund, warum die .git-Regel nicht griff: in einem Worktree ist
// .git eine DATEI, kein Verzeichnis.
func TestScanDoesNotDescendIntoAnEmbeddedWorktreeOrRepository(t *testing.T) {
	repo := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(repo, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	write("CLAUDE.md", "# eigene Regeln")
	write(".claude/worktrees/release-0.5.0/.git", "gitdir: /somewhere/.git/worktrees/release-0.5.0")
	write(".claude/worktrees/release-0.5.0/CLAUDE.md", "# dieselbe Datei, zweiter Checkout")
	write(".claude/worktrees/release-0.5.0/docs/superpowers/plans/2026-01-02-thing.md", "# plan")
	write("third-party/forked-repo/.git/config", "[core]")
	write("third-party/forked-repo/AGENTS.md", "# fremde Regeln")

	got, err := Scan(repo)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, a := range got {
		found[a.Rel] = true
	}
	if !found["CLAUDE.md"] {
		t.Fatalf("the repository's own rules must still be found: %+v", got)
	}
	for _, rel := range []string{
		".claude/worktrees/release-0.5.0/CLAUDE.md",
		".claude/worktrees/release-0.5.0/docs/superpowers/plans/2026-01-02-thing.md",
		"third-party/forked-repo/AGENTS.md",
	} {
		if found[rel] {
			t.Errorf("scanned into an embedded checkout: %s", rel)
		}
	}
}

// "spec" steckt als Zeichenfolge auch in "perspective". Gefunden am 2026-08-25:
// .superpowers/brainstorm/.../forced-perspective.html wurde als Spezifikation
// erkannt und als Volltext archiviert — eine HTML-Datei aus einem
// Brainstorm-Verzeichnis. Erkannt wird deshalb an Wortgrenzen, und nur an
// Markdown: ein Plan ist ein Text, kein gerendertes Artefakt.
func TestScanDoesNotMistakePerspectiveForASpec(t *testing.T) {
	repo := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(repo, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("# x"), 0o644)
	}
	for _, rel := range []string{
		".superpowers/brainstorm/1/content/forced-perspective.html",
		"docs/planning-tool.png",
		"docs/superpowers/specs/2026-01-01-thing-design.md",
		"docs/superpowers/plans/2026-01-02-thing.md",
		"docs/2026-01-03-spec-of-something.md",
	} {
		write(rel)
	}
	got, err := Scan(repo)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, a := range got {
		found[a.Rel] = a.Kind
	}
	for _, rel := range []string{".superpowers/brainstorm/1/content/forced-perspective.html", "docs/planning-tool.png"} {
		if kind, ok := found[rel]; ok {
			t.Errorf("%s was taken for a %s", rel, kind)
		}
	}
	for rel, want := range map[string]string{
		"docs/superpowers/specs/2026-01-01-thing-design.md": "spec",
		"docs/superpowers/plans/2026-01-02-thing.md":        "plan",
		"docs/2026-01-03-spec-of-something.md":              "spec",
	} {
		if found[rel] != want {
			t.Errorf("%s = %q, want %q", rel, found[rel], want)
		}
	}
}

func TestScanIncludesEveryMarkdownDocumentUnderDocs(t *testing.T) {
	repo := t.TempDir()
	for _, rel := range []string{
		"docs/architecture.md",
		"docs/evaluations/2026-08-24-reliability-roadmap.md",
		"docs/operations/request-ledger-rollout.md",
	} {
		path := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("# Document\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	artifacts, err := Scan(repo)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, artifact := range artifacts {
		got[artifact.Rel] = artifact.Kind
	}
	for _, rel := range []string{
		"docs/architecture.md",
		"docs/evaluations/2026-08-24-reliability-roadmap.md",
		"docs/operations/request-ledger-rollout.md",
	} {
		if got[rel] == "" {
			t.Errorf("%s was missed: %+v", rel, got)
		}
	}
}

// Ein Verzeichnis, in das der Nutzer nicht hineinsehen darf, geht die Migration
// nichts an. Gefunden am 2026-08-25 an NurBlindspot: data/postgres gehört einem
// Containernutzer, und der ganze Repo-Lauf brach daran ab, statt das eine
// Verzeichnis auszulassen.
func TestScanSkipsWhatItMayNotRead(t *testing.T) {
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("# rules"), 0o644)
	locked := filepath.Join(repo, "data", "postgres")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	got, err := Scan(repo)
	if err != nil {
		t.Fatalf("an unreadable directory must not fail the whole scan: %v", err)
	}
	if len(got) != 1 || got[0].Rel != "CLAUDE.md" {
		t.Fatalf("artifacts = %+v, want the repository's own rules", got)
	}
}

func TestOnlyRuleArtifactsAreDistilledAsCurrentKnowledge(t *testing.T) {
	for _, tc := range []struct {
		kind string
		want bool
	}{{"rules", true}, {"spec", false}, {"plan", false}} {
		if got := ShouldDistill(Artifact{Kind: tc.kind}); got != tc.want {
			t.Errorf("ShouldDistill(%q) = %v, want %v", tc.kind, got, tc.want)
		}
	}
}
