package ghost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializeWritesTheTreeAndRemovesWhatIsGone(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tree")
	if err := Materialize(root, []Doc{
		{Path: "__dir.md", Body: "das Repo"},
		{Path: "internal/__dir.md", Body: "internes"},
		{Path: "internal/store/knowledge.go.md", Body: "Wissenspfade"},
	}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "internal", "store", "knowledge.go.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "Wissenspfade") {
		t.Fatalf("body missing: %q", b)
	}

	if err := Materialize(root, []Doc{{Path: "__dir.md", Body: "das Repo"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "internal")); !os.IsNotExist(err) {
		t.Fatal("a full rewrite must drop what is no longer in the set")
	}
}

func TestMirrorPathAppendsMdForFilesAndDirDocForDirectories(t *testing.T) {
	cases := map[string]string{
		"internal/store/knowledge.go|file": "internal/store/knowledge.go.md",
		"internal/store|dir":               "internal/store/__dir.md",
		"|dir":                             "__dir.md",
		// Eine echte README bleibt von der Verzeichnisbeschreibung getrennt —
		// genau die Verwechslung, wegen der der Name nicht README.md ist.
		"README.md|file": "README.md.md",
		"__dir.md|file":  "__dir.md.md",
	}
	for in, want := range cases {
		parts := strings.SplitN(in, "|", 2)
		if got := MirrorPath(parts[0], parts[1]); got != want {
			t.Fatalf("MirrorPath(%q,%q) = %q, want %q", parts[0], parts[1], got, want)
		}
	}
}

func TestEnsureExcludedIsIdempotentAndNarrow(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := EnsureExcluded(repo); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range excludeLines {
		if n := countLines(string(b), want); n != 1 {
			t.Fatalf("%q muss genau einmal dastehen, war %d mal:\n%s", want, n, b)
		}
	}
	// Das Bauverzeichnis ist ein Geschwister von tree/ und faellt deshalb NICHT
	// unter dessen Ausschluss. Bleibt es nach einem Abbruch liegen, stuende es
	// sonst im git status.
	if !strings.Contains(string(b), "tree"+tmpSuffix) {
		t.Fatalf("das Bauverzeichnis gehoert in den Ausschluss:\n%s", b)
	}
	// Nicht .ghosttree/ als Ganzes: unter einem ausgeschlossenen Verzeichnis
	// steigt git nicht hinein, ein spaeter versioniertes config.toml waere
	// damit unsichtbar.
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == ".ghosttree/" {
			t.Fatal("excluding the whole directory would hide a future config.toml")
		}
	}
}

// Zaehlt ganze Zeilen, nicht Vorkommen: ".ghosttree/tree/" steckt als
// Zeichenkette auch in ".ghosttree/tree/x", und danach ist gefragt, wie oft der
// Eintrag dasteht.
func countLines(content, want string) int {
	n := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == want {
			n++
		}
	}
	return n
}
