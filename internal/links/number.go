package links

import (
	"regexp"
	"strconv"
	"strings"
)

// NumberTerm is a term that names objects by their number and says nothing else:
// the whole term, trimmed, is one of #217, REQ-12, TASK-3, PAIN-5, IDEA-7, pr:217
// or pull:217 (any case). REQ-, TASK-, PAIN-, IDEA- and pr: name one kind; #217
// names none and stands for every kind that is written with a hash number
// (HashKinds). Every place that reads such a term (the search page and its live
// stream, the filter of the pull request list, the qualifier pr: of the issue
// list, the references of a page, the palette of the terminal) asks ParseNumber,
// so that they agree on what a number is.
type NumberTerm struct {
	N int64
	// Kind is the kind the term names, empty for #N.
	Kind string
}

// HashKinds are the kinds #N may mean, in the order a search shows them: an
// issue (request), a pull request and a knowledge entry.
var HashKinds = []string{KindRequest, KindPull, KindKnowledge}

// The number is at most 15 digits, which keeps it well inside int64.
var numberRE = regexp.MustCompile(`(?i)^(?:#(\d{1,15})|(req|task|pain|idea)-(\d{1,15})|(pr|pull):\s*#?(\d{1,15}))$`)

var numberPrefixKind = map[string]string{"req": KindRequest, "task": KindTask, "pain": KindPain, "idea": KindIdea, "pr": KindPull, "pull": KindPull}

// ParseNumber reads term as a NumberTerm. A number of zero is no number, and a
// term with more than the number does not name one object ("REQ-5 login" is a
// search).
func ParseNumber(term string) (NumberTerm, bool) {
	m := numberRE.FindStringSubmatch(strings.TrimSpace(term))
	if m == nil {
		return NumberTerm{}, false
	}
	kind, digits := "", m[1]
	switch {
	case m[2] != "":
		kind, digits = numberPrefixKind[strings.ToLower(m[2])], m[3]
	case m[4] != "":
		kind, digits = KindPull, m[5]
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 {
		return NumberTerm{}, false
	}
	return NumberTerm{N: n, Kind: kind}, true
}

// Kinds are the kinds the term can mean: the one it names, or HashKinds.
func (t NumberTerm) Kinds() []string {
	if t.Kind != "" {
		return []string{t.Kind}
	}
	return HashKinds
}

// Refs are the references the term can mean, one per kind in order.
func (t NumberTerm) Refs() []Ref {
	kinds := t.Kinds()
	out := make([]Ref, len(kinds))
	for i, k := range kinds {
		out[i] = Ref{Kind: k, ID: t.N}
	}
	return out
}

// Is says whether the term can mean the kind.
func (t NumberTerm) Is(kind string) bool {
	for _, k := range t.Kinds() {
		if k == kind {
			return true
		}
	}
	return false
}

// Short is the short form of one of the references of the term, as the
// terminal opens it: REQ-12, pr:217, #217 (knowledge).
func Short(r Ref) string {
	n := strconv.FormatInt(r.ID, 10)
	switch r.Kind {
	case KindRequest:
		return "REQ-" + n
	case KindTask:
		return "TASK-" + n
	case KindPain:
		return "PAIN-" + n
	case KindIdea:
		return "IDEA-" + n
	case KindPull:
		return "pr:" + n
	case KindKnowledge:
		return "#" + n
	}
	return n
}
