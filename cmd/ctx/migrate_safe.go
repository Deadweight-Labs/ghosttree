package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/migrate"
)

// readArtifact liest eine Datei, die der Scan im Repo gefunden hat, ohne einem
// Symlink zu folgen. Ein Repo kann Symlinks mitbringen (eingecheckt oder nach
// dem Scan ausgetauscht); eine Regeldatei, die auf ~/.ssh/id_ed25519 zeigt,
// wird sonst gelesen, hochgeladen und ins Backup kopiert. Jede Ebene des Pfads
// unter dem Repo muss ein echtes Verzeichnis sein, die Datei selbst eine
// gewöhnliche Datei; gelesen wird durch ein Verzeichnis-Handle (os.Root).
func readArtifact(a migrate.Artifact) ([]byte, fs.FileMode, error) {
	root, ok := strings.CutSuffix(a.Path, filepath.FromSlash(a.Rel))
	if !ok || root == "" {
		// Not below a repository by its name (a path that was not made by the scan):
		// the file itself is still read without following a link.
		return readPlainFile(filepath.Dir(a.Path), filepath.Base(a.Path))
	}
	return readPlainFile(filepath.Clean(root), a.Rel)
}

func readPlainFile(base, rel string) ([]byte, fs.FileMode, error) {
	r, err := os.OpenRoot(base)
	if err != nil {
		return nil, 0, err
	}
	defer r.Close()
	parts := strings.Split(rel, "/")
	for i := range parts {
		cur := path.Join(parts[:i+1]...)
		fi, err := r.Lstat(cur)
		if err != nil {
			return nil, 0, err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil, 0, fmt.Errorf("%s is a symbolic link; it is not followed", cur)
		}
		if i < len(parts)-1 && !fi.IsDir() {
			return nil, 0, fmt.Errorf("%s is not a directory", cur)
		}
		if i == len(parts)-1 && !fi.Mode().IsRegular() {
			return nil, 0, fmt.Errorf("%s is not a regular file", cur)
		}
	}
	f, err := r.Open(rel)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	raw, err := io.ReadAll(f)
	return raw, info.Mode().Perm(), err
}
