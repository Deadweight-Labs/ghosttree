package ghost

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHashFileIsStableAndCountsLines(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, []byte("eins\nzwei\ndrei\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, blob, lines, err := HashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if lines != 3 {
		t.Fatalf("expected 3 lines, got %d", lines)
	}
	// Der Blob ist derselbe, den `git hash-object` liefert: sha1 über
	// "blob <len>\0" plus Inhalt. Fester Wert, damit ein Umbau auffällt;
	// gegengeprüft mit `printf 'eins\nzwei\ndrei\n' | git hash-object --stdin`.
	if blob != "f00189ac48a4a63bba57bd991542fe29377c40f2" {
		t.Fatalf("git blob id does not match git's own: %s", blob)
	}
	again, _, _, _ := HashFile(p)
	if sha != again {
		t.Fatal("hashing the same bytes twice must give the same answer")
	}
}

func TestHashDirIgnoresOrderButNotMembership(t *testing.T) {
	a := HashDir([]string{"b.go", "a.go", "sub/"})
	b := HashDir([]string{"sub/", "a.go", "b.go"})
	if a != b {
		t.Fatal("the same children in another order are the same directory")
	}
	if a == HashDir([]string{"a.go", "b.go"}) {
		t.Fatal("a removed child must change the directory hash")
	}
}

func TestJudgeGradesDriftAndNamesTheUnknown(t *testing.T) {
	cases := []struct {
		name            string
		stored, current string
		changed, of     int
		reachable       bool
		wantState       string
		wantPercent     int
	}{
		{"unveraendert", "x", "x", 0, 100, true, "fresh", 0},
		{"leicht", "x", "y", 10, 100, true, "drifted", 10},
		{"an der schwelle bleibt leicht", "x", "y", 25, 100, true, "drifted", 25},
		{"darueber ist veraltet", "x", "y", 26, 100, true, "stale", 26},
		{"blob weg", "x", "y", 0, 100, false, "unknown", 0},
		{"nie beschrieben", "", "y", 0, 100, true, "undescribed", 0},
	}
	for _, c := range cases {
		got := Judge(c.stored, c.current, c.changed, c.of, c.reachable)
		if got.State != c.wantState || got.Percent != c.wantPercent {
			t.Fatalf("%s: got %+v, want %s/%d", c.name, got, c.wantState, c.wantPercent)
		}
	}
}

func TestLabelSaysWhatToDoNotJustWhatItIs(t *testing.T) {
	if l := Judge("x", "x", 0, 10, true).Label(); l != "" {
		t.Fatalf("a fresh entry needs no annotation, got %q", l)
	}
	if l := Judge("x", "y", 60, 100, true).Label(); l == "" {
		t.Fatal("a stale entry must say so")
	}
}

// Ein Verzeichnis hat keine Zeilen, also auch keinen Prozentsatz. Es bekommt
// deshalb einen eigenen Ausgang statt eines "stale" mit erfundener Null —
// sonst muesste Label() raten, welche der beiden Arten es gerade beschreibt.
func TestJudgeDirHasNoPercentAndSaysWhatChanged(t *testing.T) {
	if got := JudgeDir("", "y"); got.State != "undescribed" {
		t.Fatalf("nie beschrieben: got %+v", got)
	}
	if got := JudgeDir("x", "x"); got.State != "fresh" {
		t.Fatalf("gleiche Kinderliste ist frisch: got %+v", got)
	}
	got := JudgeDir("x", "y")
	if got.State != "dirchanged" || got.Percent != 0 {
		t.Fatalf("geaenderte Kinderliste: got %+v", got)
	}
	if l := got.Label(); l == "" {
		t.Fatal("eine geaenderte Kinderliste muss sich melden")
	}
	if l := JudgeDir("x", "x").Label(); l != "" {
		t.Fatalf("ein frisches Verzeichnis braucht keine Anmerkung, got %q", l)
	}
}

// Die wichtigste Eigenschaft von ChildNames: Hook, Schreibpfad und Baum
// rechnen daraus denselben Hash. Waere die Liste je Aufrufer verschieden,
// waere jede Verzeichnisbeschreibung sofort veraltet.
func TestChildNamesMarksDirectoriesAndSkipsOurOwnFootprint(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"pkg", ".git", ".ghosttree"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	names, err := ChildNames(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.go", "pkg/"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("got %v, want %v", names, want)
		}
	}
}
