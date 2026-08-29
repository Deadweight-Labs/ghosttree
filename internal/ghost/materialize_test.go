package ghost

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureExcludedWorksFromLinkedWorktree(t *testing.T) {
	mainRepo := filepath.Join(t.TempDir(), "main")
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.Mkdir(mainRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"}} {
		if out, err := exec.Command("git", append([]string{"-C", mainRepo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(mainRepo, "seed"), []byte("seed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", mainRepo, "add", "seed").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", mainRepo, "commit", "-qm", "seed").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", mainRepo, "worktree", "add", "--detach", linked).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v: %s", err, out)
	}
	if err := EnsureExcluded(linked); err != nil {
		t.Fatal(err)
	}
	excludePath := strings.TrimSpace(commandOutput(t, "git", "-C", linked, "rev-parse", "--git-path", "info/exclude"))
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(linked, excludePath)
	}
	b, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), ".ghosttree/snapshots/INDEX.md") {
		t.Fatalf("snapshot exclusion absent from common exclude file:\n%s", b)
	}
}

func commandOutput(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, out)
	}
	return string(out)
}

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
	for _, want := range []string{".ghosttree/edit/", ".ghosttree/edit" + tmpSuffix + "/"} {
		if countLines(string(b), want) != 1 {
			t.Fatalf("document worktree exclusion %q missing:\n%s", want, b)
		}
	}
	for _, want := range []string{
		".ghosttree/snapshots/INDEX.md",
		".ghosttree/snapshots/.INDEX.md.tmp-*",
	} {
		if countLines(string(b), want) != 1 {
			t.Fatalf("snapshot mirror exclusion %q missing:\n%s", want, b)
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

func TestGeneratedPathRegistryIsExactAndKeepsOperatorFilesRelevant(t *testing.T) {
	for _, path := range []string{
		".ghosttree/tree/internal/x.md",
		".ghosttree/snapshots/INDEX.md",
		".ghosttree/INDEX.md",
	} {
		if !IsGeneratedPath(path) {
			t.Fatalf("generated path %q not registered", path)
		}
	}
	for _, path := range []string{
		".ghosttree/snapshots/operator.md",
		".ghosttree/operator-note",
		".ghosttree/INDEX.md.backup",
	} {
		if IsGeneratedPath(path) {
			t.Fatalf("operator path %q treated as generated", path)
		}
	}
}

func TestFingerprintGeneratedPathsAreSeparateFromGitExclusions(t *testing.T) {
	for _, path := range []string{
		".ghosttree/tree/internal/x.md",
		".ghosttree/knowledge/note/1.md",
		".ghosttree/docs/specs/x.md",
		".ghosttree/requests/open/REQ-1.md",
		".ghosttree/INDEX.md",
		".ghosttree/snapshots/INDEX.md",
		".ghosttree/snapshots/.INDEX.md.tmp-123",
	} {
		if !IsFingerprintGeneratedPath(path) {
			t.Fatalf("regenerable path %q not excluded from fingerprint", path)
		}
	}
	for _, path := range []string{
		".ghosttree/edit/draft.md",
		".ghosttree/edit.tmp/draft.md",
		".ghosttree/operator-note",
		".ghosttree/snapshots/operator.md",
		".ghosttree/snapshots/INDEX.md.backup",
		".ghosttree/snapshots/.INDEX.md.tmp-operator",
		".ghosttree/snapshots/.INDEX.md.tmp-",
	} {
		if IsFingerprintGeneratedPath(path) {
			t.Fatalf("operator path %q excluded from fingerprint", path)
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
