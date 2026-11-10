package ghost

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestParagraphsSplitAtBlankLines(t *testing.T) {
	got := Paragraphs("  Eins\nzwei\n\n\n Drei \n \t\nVier\r\n\r\nFünf\n")
	want := []string{"Eins\nzwei", "Drei", "Vier", "Fünf"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Paragraphs = %q, want %q", got, want)
	}
}

// The history of one path: an agent wrote A, the gardener added G, a person
// added P and kept G. A paragraph belongs to whoever wrote it first.
func TestAttributeGivesEachParagraphToItsFirstAuthor(t *testing.T) {
	chain := []store.GhostVersion{ // newest first, as GhostFileChain returns it
		{Description: "G\n\nA\n\nP", Harness: "mcp"},
		{Description: "G\n\nA", Harness: store.GardenerHarness},
		{Description: "A", Harness: "mcp"},
	}
	got := Attribute("G\n\nA\n\nP\n\nX", chain)
	want := []Paragraph{{"G", true}, {"A", false}, {"P", false}, {"X", false}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Attribute = %+v, want %+v", got, want)
	}
	if k := Kept(got); !reflect.DeepEqual(k, []string{"A", "P", "X"}) {
		t.Errorf("Kept = %q", k)
	}
	// Without a history nothing can be told apart, so everything is kept.
	for _, p := range Attribute("G\n\nA", nil) {
		if p.Gardener {
			t.Errorf("%q counted as the gardener's without a history", p.Text)
		}
	}
}

func TestMergeKeptReplacesOnlyTheGardenersParagraphs(t *testing.T) {
	old := []Paragraph{{"Mensch oben", false}, {"Gärtner alt 1", true}, {"Agent Mitte", false}, {"Gärtner alt 2", true}}
	got := MergeKept(old, "Gärtner neu 1\n\nAgent Mitte\n\nGärtner neu 2")
	want := "Mensch oben\n\nGärtner neu 1\n\nGärtner neu 2\n\nAgent Mitte"
	if got != want {
		t.Fatalf("MergeKept = %q, want %q", got, want)
	}
	if miss := MissingKept(old, got); len(miss) != 0 {
		t.Errorf("paragraphs lost: %q", miss)
	}
	// An old text without a paragraph of the gardener: the new one goes on top.
	got = MergeKept([]Paragraph{{"Nur Mensch", false}}, "Neu")
	if got != "Neu\n\nNur Mensch" {
		t.Errorf("MergeKept on a text of others = %q", got)
	}
	// A first description is just the text.
	if got := MergeKept(nil, "Erste\n\nZweite"); got != "Erste\n\nZweite" {
		t.Errorf("MergeKept on nothing = %q", got)
	}
}

func TestMissingKeptNamesAChangedParagraph(t *testing.T) {
	old := []Paragraph{{"Wörtlich zu halten.", false}, {"Darf weg.", true}}
	miss := MissingKept(old, "Wörtlich zu halten, leicht geändert.")
	if len(miss) != 1 || !strings.HasPrefix(miss[0], "Wörtlich") {
		t.Fatalf("MissingKept = %q", miss)
	}
}
