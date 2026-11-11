package web

import (
	"strings"
	"testing"
	"time"
)

// Many equal headings get distinct anchors in linear time, and a heading whose
// own text already looks like a numbered anchor does not collide with it.
func TestUniqueAnchorIsLinearAndDistinct(t *testing.T) {
	used := anchorSet{}
	seen := map[string]bool{}
	start := time.Now()
	for i := 0; i < 20000; i++ {
		id := uniqueAnchor("Same", used)
		if seen[id] {
			t.Fatalf("anchor %q given twice", id)
		}
		seen[id] = true
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("20000 equal headings took %v", d)
	}
	used = anchorSet{}
	got := []string{uniqueAnchor("a 2", used), uniqueAnchor("a", used), uniqueAnchor("a", used), uniqueAnchor("a", used)}
	want := []string{"h-a-2", "h-a", "h-a-3", "h-a-4"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("anchors %v, want %v", got, want)
	}
}
