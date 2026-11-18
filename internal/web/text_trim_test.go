package web

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// REQ-690 AC-3 (P35): a description is shortened at the end of a word.
func TestEllipsizeWordsCutsAtAWordBoundary(t *testing.T) {
	long := "Die Datei bündelt die Dateien unter internal und schreibt sie in den Spool für den Worker"
	got := ellipsizeWords(long, 50)
	if !strings.HasSuffix(got, "…") || utf8.RuneCountInString(got) > 50 {
		t.Fatalf("got %q (%d)", got, utf8.RuneCountInString(got))
	}
	body := strings.TrimSuffix(got, "…")
	if !strings.HasPrefix(long, body) || (len(long) > len(body) && long[len(body)] != ' ') {
		t.Errorf("%q ends inside a word of %q", got, long)
	}
	if got := ellipsizeWords("kurz genug", 50); got != "kurz genug" {
		t.Errorf("short text changed: %q", got)
	}
	// A single word longer than the limit is cut after all.
	if got := ellipsizeWords(strings.Repeat("x", 80), 20); utf8.RuneCountInString(got) != 20 || !strings.HasSuffix(got, "…") {
		t.Errorf("long word: %q", got)
	}
	if got := ellipsizeWords("eins, zwei, drei, vier, fünf, sechs", 20); strings.Contains(got, ",…") {
		t.Errorf("comma before the ellipsis: %q", got)
	}
}

func TestPlainLineAndDescriptionsDropTheGardenerLabels(t *testing.T) {
	desc := "Synopsis: Der Spool übergibt Aufträge vom Listener an den Worker.\n\nContext: Pro Ziel liegt höchstens eine Datei bereit."
	if got := plainLine(desc, 160); got != "Der Spool übergibt Aufträge vom Listener an den Worker." {
		t.Errorf("plainLine = %q", got)
	}
	if got := plainLine("synopsis:   klein geschrieben", 160); got != "klein geschrieben" {
		t.Errorf("plainLine lower case = %q", got)
	}
	html := string(renderDescription(desc))
	for _, bad := range []string{"Synopsis:", "Context:"} {
		if strings.Contains(html, bad) {
			t.Errorf("the rendered description still says %q: %s", bad, html)
		}
	}
	for _, want := range []string{"Der Spool übergibt", "Pro Ziel liegt"} {
		if !strings.Contains(html, want) {
			t.Errorf("the rendered description lost %q: %s", want, html)
		}
	}
	// What only looks like a label stays.
	if got := plainLine("Synopsisfoo: bleibt", 160); got != "Synopsisfoo: bleibt" {
		t.Errorf("plainLine = %q", got)
	}
}
