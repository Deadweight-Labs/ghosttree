// Package knref is the one spelling of a knowledge entry in text: KN-12.
//
// A bare #12 is what GitHub means by it, a pull request or an issue, and that is
// how agents write it. So ghosttree names its knowledge entries KN-12 in every
// output, card and instruction, and reads KN-12 wherever it reads a knowledge
// number. The old #12 is still accepted as input.
package knref

import (
	"strconv"
	"strings"
)

// Prefix is the key of a knowledge entry in text, the way REQ- is for a request.
const Prefix = "KN-"

// Format writes the number of a knowledge entry as KN-12.
func Format(id int64) string { return Prefix + strconv.FormatInt(id, 10) }

// List writes several numbers as "KN-12, KN-15".
func List(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = Format(id)
	}
	return strings.Join(parts, ", ")
}

// Parse reads KN-12 (any case), the old #12 and a bare 12. ok is false for
// anything else, for a number below 1 and for a trailing remainder.
func Parse(s string) (id int64, ok bool) {
	s = strings.TrimSpace(s)
	if len(s) >= len(Prefix) && strings.EqualFold(s[:len(Prefix)], Prefix) {
		s = s[len(Prefix):]
	} else {
		s = strings.TrimPrefix(s, "#")
	}
	if s == "" || s[0] < '0' || s[0] > '9' {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n > 0
}

// Has says whether s names a knowledge entry in the KN- form.
func Has(s string) bool {
	s = strings.TrimSpace(s)
	return len(s) > len(Prefix) && strings.EqualFold(s[:len(Prefix)], Prefix)
}
