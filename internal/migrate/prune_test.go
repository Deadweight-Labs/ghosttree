package migrate

import (
	"os"
	"path/filepath"
	"testing"
)

// Ein leergeräumtes docs/superpowers/specs/ ist immer noch sichtbarer Rest im
// Repo, und Sichtbarkeit ist der ganze Punkt der Bereinigung. Gefunden am
// 2026-08-25 an kindlemon: die Spec war weg, das Verzeichnis stand noch.
func TestPruneEmptyDirsRemovesWhatTheCleanupLeftBehind(t *testing.T) {
	repo := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(repo, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
	}
	write("docs/README.md")
	write("docs/superpowers/specs/gone.md")
	write(".superpowers/sdd/gone.md")
	os.Remove(filepath.Join(repo, "docs/superpowers/specs/gone.md"))
	os.Remove(filepath.Join(repo, ".superpowers/sdd/gone.md"))

	if err := PruneEmptyDirs(repo, []string{"docs/superpowers/specs/gone.md", ".superpowers/sdd/gone.md"}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"docs/superpowers/specs", "docs/superpowers", ".superpowers/sdd", ".superpowers"} {
		if _, err := os.Stat(filepath.Join(repo, rel)); !os.IsNotExist(err) {
			t.Errorf("empty directory %s is still there", rel)
		}
	}
	// docs/ trägt noch eine echte Datei und bleibt, die Wurzel sowieso.
	for _, rel := range []string{"docs", "."} {
		if _, err := os.Stat(filepath.Join(repo, rel)); err != nil {
			t.Errorf("%s must not be removed: %v", rel, err)
		}
	}
}

// Ein Verzeichnis, in dem noch etwas anderes liegt, gehört nicht der Migration.
func TestPruneEmptyDirsLeavesDirectoriesThatStillHoldSomething(t *testing.T) {
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, "docs/superpowers/plans"), 0o755)
	os.WriteFile(filepath.Join(repo, "docs/superpowers/plans/kept.md"), []byte("x"), 0o644)

	if err := PruneEmptyDirs(repo, []string{"docs/superpowers/plans/gone.md"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "docs/superpowers/plans/kept.md")); err != nil {
		t.Fatalf("a directory with remaining files was pruned: %v", err)
	}
}

// Der Pfad darf nicht aus dem Repo herausführen — auch dann nicht, wenn der
// Aufrufer ihm etwas Unsinniges übergibt.
func TestPruneEmptyDirsStaysInsideTheRepository(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	sibling := filepath.Join(parent, "empty-sibling")
	os.MkdirAll(repo, 0o755)
	os.MkdirAll(sibling, 0o755)

	if err := PruneEmptyDirs(repo, []string{"../empty-sibling/gone.md"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("pruned outside the repository: %v", err)
	}
}
