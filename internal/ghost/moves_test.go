package ghost

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func write(t *testing.T, repo, rel, body string) string {
	t.Helper()
	full := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, blob, _, err := HashFile(full)
	if err != nil {
		t.Fatal(err)
	}
	return blob
}

// Der Normalfall, und der wichtigste: eine KOPIE ist kein Umzug. Das Original
// existiert noch, also ist gar nichts verwaist und es wird nichts umgehaengt.
// Genau hier verlor die alte, blobbasierte Erkennung die Beschreibung des
// Originals an die Kopie.
func TestACopyIsNotAMove(t *testing.T) {
	repo := t.TempDir()
	blob := write(t, repo, "vorlage.go", "package x\n")
	write(t, repo, "kopie.go", "package x\n")

	entries := []Entry{{Path: "vorlage.go", Kind: "file"}, {Path: "kopie.go", Kind: "file"}}
	described := map[string]store.GhostFile{
		"vorlage.go": {Path: "vorlage.go", Kind: "file", Description: "die Vorlage", GitBlob: blob},
	}
	if moves := DetectMoves(repo, entries, described); len(moves) != 0 {
		t.Fatalf("eine Kopie darf nichts umhaengen, got %v", moves)
	}
}

// Eine echte Verschiebung: der alte Pfad ist weg, der Inhalt taucht unter genau
// einem neuen Pfad wieder auf.
func TestARealMoveIsDetected(t *testing.T) {
	repo := t.TempDir()
	blob := write(t, repo, "neu/ort.go", "package x\n")

	entries := []Entry{{Path: "neu/ort.go", Kind: "file"}}
	described := map[string]store.GhostFile{
		"alt/ort.go": {Path: "alt/ort.go", Kind: "file", Description: "der Text", GitBlob: blob},
	}
	moves := DetectMoves(repo, entries, described)
	if len(moves) != 1 || moves["alt/ort.go"] != "neu/ort.go" {
		t.Fatalf("die Verschiebung muss erkannt werden, got %v", moves)
	}
}

// Zwei gleiche Kandidaten sind keine Verschiebung, sondern eine Verdopplung.
// Dort wird nicht geraten — eine falsch zugeordnete Beschreibung ist schlimmer
// als eine, die neu geschrieben werden muss.
func TestTwoCandidatesAreNotGuessed(t *testing.T) {
	repo := t.TempDir()
	blob := write(t, repo, "a.go", "package x\n")
	write(t, repo, "b.go", "package x\n")

	entries := []Entry{{Path: "a.go", Kind: "file"}, {Path: "b.go", Kind: "file"}}
	described := map[string]store.GhostFile{
		"weg.go": {Path: "weg.go", Kind: "file", Description: "der Text", GitBlob: blob},
	}
	if moves := DetectMoves(repo, entries, described); len(moves) != 0 {
		t.Fatalf("bei zwei Kandidaten wird nicht geraten, got %v", moves)
	}
}

// Zwei verwaiste Beschreibungen mit demselben Blob und ein einziger Kandidat:
// welche der beiden ihn bekommt, ist nicht entscheidbar. Die Reihenfolge, in
// der die Verwaisten anfallen, kommt aus einer Map und ist damit zufaellig —
// eine Zuordnung daraus waere geraten, nur unsichtbar.
func TestTwoOrphansSharingABlobAreBothLeftAlone(t *testing.T) {
	repo := t.TempDir()
	blob := write(t, repo, "ueberlebt.go", "package x\n")

	entries := []Entry{{Path: "ueberlebt.go", Kind: "file"}}
	described := map[string]store.GhostFile{
		"weg1.go": {Path: "weg1.go", Kind: "file", Description: "der eine", GitBlob: blob},
		"weg2.go": {Path: "weg2.go", Kind: "file", Description: "der andere", GitBlob: blob},
	}
	// Zehn Laeufe: bei zufaelliger Reihenfolge faellt ein Nichtdeterminismus
	// sonst erst in Produktion auf.
	for i := 0; i < 10; i++ {
		if moves := DetectMoves(repo, entries, described); len(moves) != 0 {
			t.Fatalf("nicht entscheidbar heisst nicht umhaengen, got %v", moves)
		}
	}
}

// Ein Ziel, das schon beschrieben ist, wird nicht ueberschrieben.
func TestADescribedTargetIsLeftAlone(t *testing.T) {
	repo := t.TempDir()
	blob := write(t, repo, "neu.go", "package x\n")

	entries := []Entry{{Path: "neu.go", Kind: "file"}}
	described := map[string]store.GhostFile{
		"weg.go": {Path: "weg.go", Kind: "file", Description: "der alte Text", GitBlob: blob},
		"neu.go": {Path: "neu.go", Kind: "file", Description: "hat schon einen", GitBlob: blob},
	}
	if moves := DetectMoves(repo, entries, described); len(moves) != 0 {
		t.Fatalf("ein beschriebenes Ziel wird nicht ueberschrieben, got %v", moves)
	}
}

// Solange nichts verwaist ist, wird auch keine einzige Datei gehasht. Das ist
// der Grund, warum die Erkennung ueberhaupt im Baumschreiben stehen darf: im
// Normalfall kostet sie nichts.
func TestNothingOrphanedMeansNoWork(t *testing.T) {
	repo := t.TempDir()
	blob := write(t, repo, "da.go", "package x\n")
	entries := []Entry{{Path: "da.go", Kind: "file"}}
	described := map[string]store.GhostFile{
		"da.go": {Path: "da.go", Kind: "file", Description: "steht", GitBlob: blob},
	}
	if moves := DetectMoves(repo, entries, described); len(moves) != 0 {
		t.Fatalf("nichts verwaist, nichts zu tun, got %v", moves)
	}
}

// Verzeichnisse haben keinen Blob und wandern nicht ueber ihren Inhalt.
func TestDirectoriesAreNeverMoved(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "neu/x.go", "package x\n")
	entries := []Entry{{Path: "neu", Kind: "dir"}, {Path: "neu/x.go", Kind: "file"}}
	described := map[string]store.GhostFile{
		"alt": {Path: "alt", Kind: "dir", Description: "ein Verzeichnis"},
	}
	if moves := DetectMoves(repo, entries, described); len(moves) != 0 {
		t.Fatalf("Verzeichnisse wandern nicht ueber Inhaltsgleichheit, got %v", moves)
	}
}
