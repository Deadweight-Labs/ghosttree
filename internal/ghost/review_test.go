package ghost

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Die Blob-Bindung ist der ganze Zweck des Zustands: "hier gibt es nichts zu
// sagen" galt EINER Fassung. Ohne die Prüfung wäre es eine Entscheidung für
// immer, getroffen von jemandem, der eine andere Datei gelesen hat.
func TestReviewedEmptyKeepsMatchingBlobsAndDropsChangedOnes(t *testing.T) {
	repo := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(repo, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		_, blob, _, err := HashFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return blob
	}
	unchangedBlob := write("unchanged.go", "package a\n")
	write("changed.go", "package b\n")

	got := ReviewedEmpty(repo, []store.GhostReview{
		{Path: "unchanged.go", GitBlob: unchangedBlob},
		{Path: "changed.go", GitBlob: "ein alter blob"},
		{Path: "weg.go", GitBlob: "irgendwas"},
	})

	if !got["unchanged.go"] {
		t.Error("eine unveränderte Datei muss angesehen bleiben")
	}
	if got["changed.go"] {
		t.Error("eine geänderte Datei muss wieder Kandidat werden")
	}
	// Gelöscht oder unlesbar: hier nichts zu melden. Der Pfad steht ohnehin
	// nicht mehr in der Eintragsliste.
	if got["weg.go"] {
		t.Error("eine fehlende Datei darf nicht als angesehen gelten")
	}
}

func TestReviewedEmptyIgnoresIncompleteRows(t *testing.T) {
	repo := t.TempDir()
	got := ReviewedEmpty(repo, []store.GhostReview{{Path: "", GitBlob: "x"}, {Path: "a.go"}})
	if len(got) != 0 {
		t.Errorf("unvollständige Reviews dürfen nichts erzeugen, got %v", got)
	}
}
