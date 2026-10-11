package collector

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// ProjectMarker ist die Markerdatei, die einen Ordner einem benannten Projekt
// (ohne Git) zuordnet: eine Zeile mit der Projekt-ID, Kommentarzeilen mit "#".
const ProjectMarker = ".ghosttree-project"

// ReadProjectMarker liest die Projekt-ID aus einer Markerdatei. Leere Zeilen
// und Kommentare zählen nicht; genau eine gültige ID muss übrig bleiben.
func ReadProjectMarker(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var id string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024), 4096)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if id != "" {
			return "", fmt.Errorf("%s: more than one project line", path)
		}
		id = line
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if !scope.IsNamedProject(id) {
		return "", fmt.Errorf("%s: %q is not a project name", path, id)
	}
	return id, nil
}

// findLink geht vom Ordner aufwärts und liefert den nächsten verlinkten Ordner
// samt Projekt. Ein Marker im Home-Verzeichnis oder in "/" gilt nicht (dort
// ordnete er jede Sitzung der Maschine einem Projekt zu), und ein kaputter
// Marker wird übergangen, statt die Auflösung zu brechen. Der Weg ist rein
// lexikalisch, damit auch ein inzwischen gelöschter Unterordner seinen Marker
// findet.
func findLink(cwd string) (dir, project string) {
	if cwd == "" {
		return "", ""
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", ""
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		home = filepath.Clean(home)
	}
	for d := abs; ; d = filepath.Dir(d) {
		if parent := filepath.Dir(d); parent == d {
			return "", "" // "/" selbst wird nie verlinkt
		}
		if d != home {
			if id, err := ReadProjectMarker(filepath.Join(d, ProjectMarker)); err == nil {
				return d, id
			}
		}
	}
}

// FindProjectLink liefert den nächsten verlinkten Ordner oberhalb von (oder
// gleich) cwd und sein Projekt, sonst leere Strings.
func FindProjectLink(cwd string) (dir, project string) { return findLink(cwd) }

// linkWins sagt, ob der Marker in linkDir die Identität bestimmt, wenn das Repo
// mit der Wurzel repoRoot und Origin ebenfalls zuständig wäre. Die nächste
// Grenze gewinnt: liegt das Repo echt unterhalb des verlinkten Ordners, behält
// es seine Identität; liegt der verlinkte Ordner im Repo oder ist er dessen
// Wurzel (ausdrücklich verlinkt), gilt der Marker.
func linkWins(linkDir, repoRoot string) bool {
	if repoRoot == "" {
		return true
	}
	real := linkDir
	if r, err := filepath.EvalSymlinks(linkDir); err == nil {
		real = r
	}
	return !strings.HasPrefix(repoRoot, real+string(filepath.Separator))
}

// ProjectInfo ist GitInfo mit verlinkten Ordnern: Projekt und Branch eines
// Arbeitsverzeichnisses, wie sie eine Sitzung dort hätte.
func ProjectInfo(cwd string) (project, branch string) {
	if dir, _ := findLink(cwd); dir == "" {
		return GitInfo(cwd)
	}
	g := ResolveGitContext(cwd)
	return g.Project, g.Branch
}
