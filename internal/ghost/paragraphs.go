package ghost

import (
	"regexp"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Paragraph is one paragraph of a description with the one who wrote it.
type Paragraph struct {
	Text string
	// Gardener is true when the gardener wrote this paragraph. Everything else
	// was written by an agent or a person and is kept word for word.
	Gardener bool
}

var blankLine = regexp.MustCompile(`\n[ \t]*\n`)

// Paragraphs splits a description at its blank lines.
func Paragraphs(text string) []string {
	var out []string
	for _, p := range blankLine.Split(strings.ReplaceAll(text, "\r\n", "\n"), -1) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Attribute says, for every paragraph of the current description, who wrote it.
// chain is the history of the path as GhostFileChain returns it, newest first;
// a paragraph belongs to the author of the oldest version in which it appears
// word for word. A paragraph the chain does not explain counts as someone
// else's, never the gardener's: what cannot be told apart is kept.
func Attribute(current string, chain []store.GhostVersion) []Paragraph {
	first := map[string]bool{}
	seen := map[string]bool{}
	for i := len(chain) - 1; i >= 0; i-- {
		for _, p := range Paragraphs(chain[i].Description) {
			if !seen[p] {
				seen[p] = true
				first[p] = chain[i].Harness == store.GardenerHarness
			}
		}
	}
	var out []Paragraph
	for _, p := range Paragraphs(current) {
		out = append(out, Paragraph{Text: p, Gardener: seen[p] && first[p]})
	}
	return out
}

// Kept lists the paragraphs the gardener may not change.
func Kept(old []Paragraph) []string {
	var out []string
	for _, p := range old {
		if !p.Gardener {
			out = append(out, p.Text)
		}
	}
	return out
}

// MergeKept builds the gardener's new description: its own text takes the place
// of its old paragraphs (at the first of them, or at the top when there were
// none), and every paragraph somebody else wrote stays where it was, word for
// word. A paragraph of its own text that repeats a kept one is dropped.
func MergeKept(old []Paragraph, own string) string {
	kept := map[string]bool{}
	for _, p := range old {
		if !p.Gardener {
			kept[p.Text] = true
		}
	}
	var mine []string
	for _, p := range Paragraphs(own) {
		if !kept[p] {
			mine = append(mine, p)
		}
	}
	var out []string
	placed := false
	for _, p := range old {
		if p.Gardener {
			if !placed {
				out = append(out, mine...)
				placed = true
			}
			continue
		}
		out = append(out, p.Text)
	}
	if !placed {
		out = append(mine, out...)
	}
	return strings.Join(out, "\n\n")
}

// MissingKept lists the paragraphs of others that a new text of the gardener
// leaves out or changes.
func MissingKept(old []Paragraph, next string) []string {
	have := map[string]bool{}
	for _, p := range Paragraphs(next) {
		have[p] = true
	}
	var out []string
	for _, p := range Kept(old) {
		if !have[p] {
			out = append(out, p)
		}
	}
	return out
}

// The references a description carries when the gardener wrote it itself: after
// a merge into the default branch ("gardener:merge:<merge commit>") or for a
// file nobody had described ("gardener:run:<HEAD of the run>"). Adopting a
// proposal on the review page writes "gardener:ghost-note:<note>" instead.
const (
	GardenerMergeRef = "gardener:merge:"
	GardenerRunRef   = "gardener:run:"
)

var gardenerWriteRef = regexp.MustCompile(`^gardener:(merge|run):[0-9a-f]{40}([0-9a-f]{24})?$`)

// IsGardenerWriteRef says whether ref is one the gardener's account may put on a
// description it writes itself.
func IsGardenerWriteRef(ref string) bool { return gardenerWriteRef.MatchString(ref) }

func shortRef(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
