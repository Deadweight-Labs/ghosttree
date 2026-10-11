package collector

import (
	"path/filepath"

	"github.com/Deadweight-Labs/ghosttree/internal/projectlink"
)

// linkWins sagt, ob der lokal bestätigte Link für linkDir die Identität
// bestimmt, wenn das Repo mit der Wurzel repoRoot und Origin ebenfalls
// zuständig wäre. Die nächste Grenze gewinnt: liegt das Repo echt unterhalb des
// verlinkten Ordners, behält es seine Identität; liegt der verlinkte Ordner im
// Repo oder ist er dessen Wurzel (ausdrücklich lokal bestätigt), gilt der Link.
func linkWins(linkDir, repoRoot string) bool {
	if repoRoot == "" {
		return true
	}
	root := repoRoot
	if r, err := filepath.EvalSymlinks(repoRoot); err == nil {
		root = r
	}
	return !projectlink.Inside(root, linkDir)
}

// ProjectInfo ist GitInfo mit verlinkten Ordnern: Projekt und Branch eines
// Arbeitsverzeichnisses, wie sie eine Sitzung dort hätte.
func ProjectInfo(cwd string) (project, branch string) {
	if dir, _ := projectlink.Find(cwd); dir == "" {
		return GitInfo(cwd)
	}
	g := ResolveGitContext(cwd)
	return g.Project, g.Branch
}
