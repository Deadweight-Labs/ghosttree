package ghost

import (
	"os"
	"path/filepath"
	"strings"
)

// Doc ist ein Stück Text an einem Pfad. Der Materialisierer kennt bewusst keine
// Ghost-Dateien: derselbe Mechanismus schreibt später auch Wissen, Dokumente
// und den Ledger unter .ghosttree/ (REQ-175), und der soll nicht zweimal
// gebaut werden.
type Doc struct {
	Path string
	Body string
}

// Materialize schreibt den Baum vollständig neu, statt ihn abzugleichen. Der
// ganze Baum dieses Repos sind rund 47 KB — ein kompletter Durchlauf dauert
// Millisekunden, und "vollständig neu" hat keine Abgleichfehler.
//
// Erst daneben bauen, dann tauschen: bricht es mittendrin ab, bleibt der alte
// Baum stehen. Das kurze Fenster zwischen Entfernen und Umbenennen ist
// hinnehmbar, weil der Baum eine Projektion ist — was dort verlorengeht, ist
// beim nächsten Schreibzugriff wieder da.
func Materialize(root string, docs []Doc) error {
	tmp := root + tmpSuffix
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	for _, d := range docs {
		full := filepath.Join(tmp, filepath.FromSlash(d.Path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		body := d.Body
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	return os.Rename(tmp, root)
}

// DirDoc ist der Dateiname der Verzeichnisbeschreibung. Nicht README.md: der
// Baum steht neben echten Repos voller echter READMEs, und ein Leser, der
// .ghosttree/tree/internal/README.md sieht, hält es für die README von
// internal/ statt für deren Beschreibung. Der Name sagt stattdessen, was es
// ist, und die Unterstriche sortieren ihn in einem `ls` nicht mitten unter die
// Geschwister.
const DirDoc = "__dir.md"

// MirrorPath bildet einen Repo-Pfad auf seine Stelle im Ghost-Baum ab. Datei X
// wird X.md, Verzeichnis D wird D/__dir.md. Kollisionen gibt es dadurch nicht:
// eine Repo-Datei __dir.md wird __dir.md.md.
func MirrorPath(path, kind string) string {
	if kind == "dir" {
		if path == "" {
			return DirDoc
		}
		return path + "/" + DirDoc
	}
	return path + ".md"
}

// TreeRoot ist das Verzeichnis, in das der Baum geschrieben wird. Im Repo,
// weil der Agent ihn dort findet, ohne dass jemand ihm einen Pfad nennt — er
// sucht ohnehin schon nach .claude/ und .codex/. Ohne Repo bleibt das
// Maschinen-Zuhause.
func TreeRoot(repoRoot, project, home string) string {
	if repoRoot != "" {
		return filepath.Join(repoRoot, ".ghosttree", "tree")
	}
	return filepath.Join(home, ".ghosttree", "trees", filepath.FromSlash(project))
}

// tmpSuffix hängt an der Baumwurzel und macht daraus das Geschwisterverzeichnis,
// in dem daneben gebaut wird. Geschwister, weil das Umbenennen am Ende innerhalb
// desselben Dateisystems bleiben muss.
const tmpSuffix = ".tmp"

// excludeLines sind absichtlich eng. `.ghosttree/` als Ganzes auszuschliessen
// wäre ein Pauschalausschluss, unter dem alles künftig Hinzukommende
// stillschweigend verschwindet — und ein `!.ghosttree/config.toml` daneben
// wirkt NICHT, weil git in ein ausgeschlossenes Verzeichnis nicht hineinsteigt.
//
// Das Bauverzeichnis steht mit in der Liste, obwohl es normalerweise nur
// Millisekunden existiert: bricht ein Durchlauf mittendrin ab, bleibt es liegen,
// und dann stünde es im `git status` von jemandem, der von seiner Existenz nie
// erfahren hat.
var excludeLines = []string{".ghosttree/tree/", ".ghosttree/tree" + tmpSuffix + "/"}

// EnsureExcluded trägt den Baum in .git/info/exclude ein, nicht in .gitignore:
// so ändert ghosttree keine versionierte Datei. Punktverzeichnisse werden von
// git nicht automatisch ignoriert — .claude/ und .env stehen ohne Eintrag
// genauso im git status.
func EnsureExcluded(repoRoot string) error {
	path := filepath.Join(repoRoot, ".git", "info", "exclude")
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	have := map[string]bool{}
	for _, line := range strings.Split(string(old), "\n") {
		have[strings.TrimSpace(line)] = true
	}
	var missing []string
	for _, want := range excludeLines {
		if !have[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content := string(old)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return os.WriteFile(path, []byte(content+strings.Join(missing, "\n")+"\n"), 0o644)
}
