package web

import (
	"strings"
	"unicode"
)

// ellipsizeWords shortens s to at most max characters, at the end of a word, and
// puts "…" after it. Runs of white space become one space. ellipsize cuts
// wherever the limit falls, mid-word; a description in the code tree read
// "…die Dateien unter inter…" (P35). A word that is longer than the limit by itself
// is cut after all, there is no better place.
func ellipsizeWords(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	cut := max - 1
	// The word the limit falls into goes entirely; end at the space before it.
	for i := cut; i > max/2; i-- {
		if unicode.IsSpace(r[i]) {
			cut = i
			break
		}
	}
	return strings.TrimRightFunc(string(r[:cut]), func(c rune) bool {
		return unicode.IsSpace(c) || c == ',' || c == ';' || c == ':' || c == '(' || c == '-'
	}) + "…"
}

// ghostLabels are the prefixes a description may start with: the gardener
// wrote "Synopsis:" and "Context:" as the head of a section and they stayed in
// front of every description in the tree (P35).
var ghostLabels = []string{"Synopsis:", "Context:", "Kontext:", "Zusammenfassung:", "Summary:"}

// withoutGhostLabel removes a leading label (and the white space after it) from a
// description, once, whatever the case.
func withoutGhostLabel(s string) string {
	s = strings.TrimSpace(s)
	for _, label := range ghostLabels {
		if len(s) >= len(label) && strings.EqualFold(s[:len(label)], label) {
			return strings.TrimSpace(s[len(label):])
		}
	}
	return s
}
