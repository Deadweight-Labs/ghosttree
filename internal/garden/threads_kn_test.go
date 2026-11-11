package garden

import (
	"strings"
	"testing"
)

// A thread asked before KN-n was the spelling carries "#12 (hash)" in its question;
// one asked now carries "KN-12 (hash)". Both are read, or an old thread would be
// asked again.
func TestParseStandReadsKNAndTheOldHash(t *testing.T) {
	h := strings.Repeat("a", 8)
	g := strings.Repeat("b", 16)
	for question, want := range map[string]map[int64]string{
		standLabel + " KN-12 (" + h + "), KN-15 (" + g + ")": {12: h, 15: g},
		standLabel + " #12 (" + h + "), #15 (" + g + ")":     {12: h, 15: g},
		standLabel + " #12 (" + h + "), KN-15 (" + g + ")":   {12: h, 15: g},
	} {
		got := parseStand("Behaupten diese Einträge Unvereinbares?\n\n" + question)
		if len(got) != len(want) || got[12] != want[12] || got[15] != want[15] {
			t.Errorf("%q read as %v, want %v", question, got, want)
		}
	}
	if got := parseStand("keine Stand-Zeile"); got != nil {
		t.Errorf("a question without the line read as %v", got)
	}
}

func TestThreadTextNamesEntriesAsKN(t *testing.T) {
	r := Relation{Kind: "contradiction", IDs: []int64{12, 15}}
	title := ThreadTitle(r, map[int64]string{12: "Dienst läuft auf a", 15: "Dienst läuft auf b"})
	if !strings.Contains(title, "KN-12 „Dienst läuft auf a“ und KN-15") || strings.Contains(title, "#") {
		t.Errorf("title = %q", title)
	}
	q := threadQuestion(r, map[int64]Entry{12: {ID: 12, Title: "a"}, 15: {ID: 15, Title: "b"}})
	if !strings.Contains(q, "KN-12 (") || !strings.Contains(q, "KN-15 (") || strings.Contains(q, "#") {
		t.Errorf("question = %q", q)
	}
	if got := parseStand(q); len(got) != 2 {
		t.Errorf("the question the gardener writes is not read back: %v", got)
	}
}
