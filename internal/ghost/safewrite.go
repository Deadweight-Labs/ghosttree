package ghost

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Ein Repo bringt .ghosttree/ oder eine Ebene darin selbst mit, als eingecheckten
// Symlink. Alles, was unter .ghosttree/ geschrieben oder gelöscht wird, geht
// deshalb durch dieses Verzeichnis-Handle (os.Root) und prüft vorher jede Ebene
// per Lstat: ein Symlink bricht den Durchlauf ab, bevor etwas gelöscht oder
// geschrieben wird.

// Rechte der Projektion: Wissen, Dokumente und Ledger eines privaten Projekts
// gehören nur dem Nutzer.
const (
	dirPerm  = 0o700
	filePerm = 0o600
)

// ErrUnsafe meldet einen Pfad, den die Projektion nicht anfasst.
var ErrUnsafe = errors.New("unsafe path")

func errLink(p string) error {
	return fmt.Errorf("%w: %s is not a plain directory (a symbolic link?); ghosttree does not write through it and removes nothing, delete or move it yourself", ErrUnsafe, p)
}

// CheckDocPath prüft einen Pfad innerhalb des Spiegels (Schrägstrich-Form): nur
// lokale, saubere Teile. Ein Pfad kann Teile enthalten, die ein Server liefert
// (Typ, Zustand), und die dürfen den Spiegel nicht verlassen.
func CheckDocPath(p string) error {
	if p == "" || strings.ContainsRune(p, 0) || strings.HasPrefix(p, "/") {
		return fmt.Errorf("%w: %q", ErrUnsafe, p)
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%w: %q", ErrUnsafe, p)
		}
		// Laufwerksangabe auch auf Systemen, wo sie nur ein Name wäre: der Spiegel
		// wird zwischen Rechnern kopiert.
		if len(part) >= 2 && part[1] == ':' && (part[0]|0x20 >= 'a' && part[0]|0x20 <= 'z') {
			return fmt.Errorf("%w: %q", ErrUnsafe, p)
		}
	}
	if !filepath.IsLocal(filepath.FromSlash(p)) {
		return fmt.Errorf("%w: %q", ErrUnsafe, p)
	}
	return nil
}

// checkChain verlangt, dass jede vorhandene Ebene von rel ein echtes Verzeichnis
// ist. Fehlt eine, endet die Prüfung: darunter kann nichts liegen.
func checkChain(r *os.Root, rel string) error {
	parts := strings.Split(rel, "/")
	for i := range parts {
		cur := path.Join(parts[:i+1]...)
		fi, err := r.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return errLink(cur)
		}
	}
	return nil
}

// ensureDir legt die Ebenen von rel an (0700), nachdem sie geprüft sind, und
// prüft danach noch einmal. .ghosttree selbst wird auf 0700 nachgezogen, auch
// wenn ein älterer Lauf es weiter geöffnet hatte.
func ensureDir(r *os.Root, rel string) error {
	if rel == "." || rel == "" {
		return nil
	}
	if err := checkChain(r, rel); err != nil {
		return err
	}
	if err := r.MkdirAll(rel, dirPerm); err != nil {
		return err
	}
	if err := checkChain(r, rel); err != nil {
		return err
	}
	if top := strings.SplitN(rel, "/", 2)[0]; top == ".ghosttree" {
		return r.Chmod(top, dirPerm)
	}
	return nil
}

func cleanRel(rel string) (string, error) {
	rel = filepath.ToSlash(rel)
	if err := CheckDocPath(rel); err != nil {
		return "", err
	}
	return rel, nil
}

// Materialize schreibt den Baum vollständig neu, statt ihn abzugleichen. Der
// ganze Baum dieses Repos sind rund 47 KB — ein kompletter Durchlauf dauert
// Millisekunden, und "vollständig neu" hat keine Abgleichfehler.
//
// Erst daneben bauen, dann tauschen: bricht es mittendrin ab, bleibt der alte
// Baum stehen. Das kurze Fenster zwischen Entfernen und Umbenennen ist
// hinnehmbar, weil der Baum eine Projektion ist — was dort verlorengeht, ist
// beim nächsten Schreibzugriff wieder da.
//
// base ist das vertrauenswürdige Verzeichnis (Repo-Wurzel oder Home), rel der
// Zielpfad darunter. Alles unterhalb von base darf kein Symlink sein; die Pfade
// der Docs müssen lokal bleiben.
func Materialize(base, rel string, docs []Doc) error {
	rel, err := cleanRel(rel)
	if err != nil {
		return err
	}
	for _, d := range docs {
		if err := CheckDocPath(d.Path); err != nil {
			return err
		}
	}
	r, err := os.OpenRoot(base)
	if err != nil {
		return err
	}
	defer r.Close()

	tmp := rel + tmpSuffix
	parent := path.Dir(rel)
	if err := ensureDir(r, parent); err != nil {
		return err
	}
	if err := checkChain(r, tmp); err != nil {
		return err
	}
	if err := r.RemoveAll(tmp); err != nil {
		return err
	}
	if err := r.Mkdir(tmp, dirPerm); err != nil {
		return err
	}
	for _, d := range docs {
		full := path.Join(tmp, d.Path)
		if err := r.MkdirAll(path.Dir(full), dirPerm); err != nil {
			return err
		}
		body := d.Body
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		if err := writeRootFile(r, full, []byte(body), os.O_WRONLY|os.O_CREATE|os.O_TRUNC); err != nil {
			return err
		}
	}
	// Zwischen Bauen und Tauschen kann jemand die Ebenen ausgetauscht haben.
	if err := checkChain(r, rel); err != nil {
		return err
	}
	if err := r.RemoveAll(rel); err != nil {
		return err
	}
	return r.Rename(tmp, rel)
}

// WriteFile schreibt eine einzelne Datei der Projektion (INDEX.md) über eine
// neue Temporärdatei und ein Umbenennen: das ersetzt den Verzeichniseintrag und
// schreibt nie durch einen Symlink hindurch.
func WriteFile(base, rel string, body []byte) error {
	rel, err := cleanRel(rel)
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(base)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := ensureDir(r, path.Dir(rel)); err != nil {
		return err
	}
	if fi, err := r.Lstat(rel); err == nil && !fi.Mode().IsRegular() {
		return errLink(rel)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp := path.Join(path.Dir(rel), "."+path.Base(rel)+".tmp")
	_ = r.Remove(tmp)
	if err := writeRootFile(r, tmp, body, os.O_WRONLY|os.O_CREATE|os.O_EXCL); err != nil {
		return err
	}
	if err := r.Rename(tmp, rel); err != nil {
		_ = r.Remove(tmp)
		return err
	}
	return nil
}

func writeRootFile(r *os.Root, name string, body []byte, flag int) error {
	f, err := r.OpenFile(name, flag, filePerm)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
