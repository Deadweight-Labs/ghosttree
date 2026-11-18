package ghost

import (
	"os"
	"os/exec"
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

// TreeRoot ist das Verzeichnis, in das der Baum geschrieben wird, als
// vertrauenswürdige Wurzel und Pfad darunter. Im Repo,
// weil der Agent ihn dort findet, ohne dass jemand ihm einen Pfad nennt — er
// sucht ohnehin schon nach .claude/ und .codex/. Ohne Repo bleibt das
// Maschinen-Zuhause.
func TreeRoot(repoRoot, project, home string) (base, rel string) {
	if repoRoot != "" {
		return repoRoot, ".ghosttree/tree"
	}
	return home, ".ghosttree/trees/" + project
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
// Seit REQ-175 liegen neben dem Baum auch Wissen, Dokumente und der Ledger
// dort — jedes mit seinem Bauverzeichnis daneben, und INDEX.md als einzige
// Datei direkt darunter. Aufgezählt statt pauschal: der Grund von oben gilt
// weiter, und was hier fehlt, fällt beim ersten `git status` auf, statt still
// zu verschwinden.
var excludeLines = []string{
	".ghosttree/tree/", ".ghosttree/tree" + tmpSuffix + "/",
	".ghosttree/knowledge/", ".ghosttree/knowledge" + tmpSuffix + "/",
	".ghosttree/docs/", ".ghosttree/docs" + tmpSuffix + "/",
	".ghosttree/edit/", ".ghosttree/edit" + tmpSuffix + "/",
	// The worktree of workflows (ctx workflow pull/push), written by people and
	// never by the mirror, so there is no building directory beside it.
	".ghosttree/workflows/",
	".ghosttree/requests/", ".ghosttree/requests" + tmpSuffix + "/",
	".ghosttree/snapshots/INDEX.md", ".ghosttree/snapshots/.INDEX.md.tmp-*",
	".ghosttree/INDEX.md",
}

// IsGeneratedPath reports whether path is hidden from ordinary Git status.
// This list includes the user-owned document worktree and is deliberately
// broader than the paths omitted from a context-snapshot fingerprint.
func IsGeneratedPath(path string) bool {
	path = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "./")
	for _, pattern := range excludeLines {
		isTree := strings.HasSuffix(pattern, "/")
		pattern = strings.TrimSuffix(pattern, "/")
		if path == pattern || (isTree && strings.HasPrefix(path, pattern+"/")) {
			return true
		}
	}
	return isSnapshotIndexTempPath(path)
}

var fingerprintGeneratedTrees = []string{
	".ghosttree/tree",
	".ghosttree/tree" + tmpSuffix,
	".ghosttree/knowledge",
	".ghosttree/knowledge" + tmpSuffix,
	".ghosttree/docs",
	".ghosttree/docs" + tmpSuffix,
	".ghosttree/requests",
	".ghosttree/requests" + tmpSuffix,
}

var fingerprintGeneratedFiles = []string{
	".ghosttree/INDEX.md",
	".ghosttree/snapshots/INDEX.md",
}

const fingerprintSnapshotTempPrefix = ".ghosttree/snapshots/.INDEX.md.tmp-"

// IsFingerprintGeneratedPath is the narrow allowlist of bytes Ghosttree can
// regenerate. In particular, .ghosttree/edit is user-authored and never
// belongs here even though ordinary Git status hides it.
func IsFingerprintGeneratedPath(path string) bool {
	path = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "./")
	for _, tree := range fingerprintGeneratedTrees {
		if path == tree || strings.HasPrefix(path, tree+"/") {
			return true
		}
	}
	for _, file := range fingerprintGeneratedFiles {
		if path == file {
			return true
		}
	}
	return isSnapshotIndexTempPath(path)
}

func isSnapshotIndexTempPath(path string) bool {
	if !strings.HasPrefix(path, fingerprintSnapshotTempPrefix) {
		return false
	}
	suffix := strings.TrimPrefix(path, fingerprintSnapshotTempPrefix)
	if suffix == "" {
		return false
	}
	for _, char := range suffix {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

// EnsureExcluded trägt den Baum in .git/info/exclude ein, nicht in .gitignore:
// so ändert ghosttree keine versionierte Datei. Punktverzeichnisse werden von
// git nicht automatisch ignoriert — .claude/ und .env stehen ohne Eintrag
// genauso im git status.
func EnsureExcluded(repoRoot string) error {
	path := excludePath(repoRoot)
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

func excludePath(repoRoot string) string {
	out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--git-path", "info/exclude").Output()
	if err != nil {
		return filepath.Join(repoRoot, ".git", "info", "exclude")
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return filepath.Join(repoRoot, ".git", "info", "exclude")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoRoot, path)
	}
	return filepath.Clean(path)
}
