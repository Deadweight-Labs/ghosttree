package migrate

import (
	"os"
	"path/filepath"
	"strings"
)

// PruneEmptyDirs entfernt die Verzeichnisse, die nach dem Bereinigen leer
// zurückbleiben, aufwärts bis zur Repo-Wurzel.
//
// Ein leeres docs/superpowers/specs/ ist immer noch sichtbarer Rest, und
// Sichtbarkeit ist der Grund, aus dem überhaupt bereinigt wird — der Betreiber
// hat als Motivation nicht Nutzlosigkeit genannt, sondern dass er ein Repo
// herzeigen können will.
//
// Aufgehört wird, sobald ein Verzeichnis noch etwas enthält; die Wurzel selbst
// wird nie angefasst, und ein Pfad, der aus dem Repo herausführt, auch nicht.
func PruneEmptyDirs(repo string, rels []string) error {
	root, err := filepath.Abs(repo)
	if err != nil {
		return err
	}
	for _, rel := range rels {
		dir := filepath.Dir(filepath.Join(root, filepath.FromSlash(rel)))
		for {
			inside, err := filepath.Rel(root, dir)
			if err != nil || inside == "." || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
				break
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) > 0 {
				break
			}
			if err := os.Remove(dir); err != nil {
				break
			}
			dir = filepath.Dir(dir)
		}
	}
	return nil
}
