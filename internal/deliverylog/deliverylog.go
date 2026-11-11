// Package deliverylog describes what a hook delivered to an agent, in the form
// that travels between server, hook and the delivery log (REQ-636, P0 of the
// concept "Schlankes Profil und Kontext, der hilft").
//
// A delivery is the text a hook put in front of the model. The log breaks it
// into parts: one per knowledge entry, per path description, per runbook, per
// ledger section, and one "frame" part for everything that is not one of
// those (headings, glue, truncation notices). The parts of one delivery add
// up to exactly the characters the session budget counted for it, so the log
// can be reconciled with the budget receipt.
//
// A part names an object by number or by repository path and carries a size.
// It never carries text and never a word from the prompt: a hint line, which
// is built from a word of the prompt, is logged as a hint of some size and
// nothing more.
package deliverylog

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// How a delivery reached the session.
const (
	ViaBootstrap = "bootstrap" // the briefing at session start
	ViaRelevance = "relevance" // an entry a word in the prompt brought
	ViaHint      = "hint"      // the line that says where a rare word came up
	ViaRunbook   = "runbook"   // a procedure a prompt, command or path triggered
	ViaPath      = "path"      // what is known about a path a tool call touched
	ViaSubagent  = "subagent"  // the briefing a subagent is started with
)

// What was delivered.
const (
	KindKnowledge = "knowledge"
	KindGhost     = "ghost" // a path description
	KindRunbook   = "runbook"
	KindLedger    = "ledger" // work ledger, package, tasks, interrupted work
	KindFeature   = "feature"
	KindHint      = "hint"
	KindFrame     = "frame" // everything around the parts above
)

// HeaderName is the response header with which the server tells the hook how
// the body it just sent divides into parts.
const HeaderName = "X-Ghosttree-Delivered"

const (
	// MaxHeaderParts bounds what the server announces and the hook believes.
	MaxHeaderParts = 64
	// MaxPartsPerDelivery bounds the parts of one delivery in the log; the
	// remainder is counted as frame.
	MaxPartsPerDelivery = 48
	// MaxPathRunes bounds a repository path in the log.
	MaxPathRunes = 300
)

// Part is one piece of a delivery.
type Part struct {
	Via   string `json:"via"`
	Kind  string `json:"kind"`
	ID    int64  `json:"id,omitempty"`
	Path  string `json:"path,omitempty"`
	Chars int    `json:"chars"`
}

// Runes counts characters the way the session budget does.
func Runes(s string) int { return utf8.RuneCountInString(s) }

// ValidVia and ValidKind say whether a name is one of the above.
func ValidVia(v string) bool {
	switch v {
	case ViaBootstrap, ViaRelevance, ViaHint, ViaRunbook, ViaPath, ViaSubagent:
		return true
	}
	return false
}

func ValidKind(k string) bool {
	switch k {
	case KindKnowledge, KindGhost, KindRunbook, KindLedger, KindFeature, KindHint, KindFrame:
		return true
	}
	return false
}

// Clean returns the part in the form the log stores, and false when it cannot
// be stored: an unknown name, a negative size or a path that is not text.
func (p Part) Clean() (Part, bool) {
	if !ValidVia(p.Via) || !ValidKind(p.Kind) || p.Chars < 0 || p.ID < 0 || !utf8.ValidString(p.Path) {
		return Part{}, false
	}
	if strings.ContainsAny(p.Path, "\x00\n\r") {
		return Part{}, false
	}
	if utf8.RuneCountInString(p.Path) > MaxPathRunes {
		p.Path = string([]rune(p.Path)[:MaxPathRunes])
	}
	if p.Kind != KindGhost {
		p.Path = ""
	}
	return p, true
}

// Header encodes parts as "via:kind:id:chars" separated by commas, in the
// order they stand in the body. Paths are not part of it (they would need
// escaping, and the server does not know them: the hook renders those).
func Header(parts []Part) string {
	if len(parts) == 0 {
		return ""
	}
	if len(parts) > MaxHeaderParts {
		parts = parts[:MaxHeaderParts]
	}
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(p.Via)
		b.WriteByte(':')
		b.WriteString(p.Kind)
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(p.ID, 10))
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(p.Chars))
	}
	return b.String()
}

// ParseHeader reads what Header wrote. It is forgiving: a malformed part is
// skipped, because the header is an annotation and never a reason to drop the
// text it annotates.
func ParseHeader(h string) []Part {
	if h == "" || len(h) > 16<<10 {
		return nil
	}
	var out []Part
	for field := range strings.SplitSeq(h, ",") {
		f := strings.Split(field, ":")
		if len(f) != 4 {
			continue
		}
		id, err1 := strconv.ParseInt(f[2], 10, 64)
		chars, err2 := strconv.Atoi(f[3])
		if err1 != nil || err2 != nil {
			continue
		}
		p, ok := Part{Via: f[0], Kind: f[1], ID: id, Chars: chars}.Clean()
		if !ok {
			continue
		}
		out = append(out, p)
		if len(out) == MaxHeaderParts {
			break
		}
	}
	return out
}

// Fit turns the parts a hook collected into the parts of one delivery of
// exactly total characters: parts keep their order and their size until the
// budget runs out, a part that would not fit is cut to what is left, and
// everything the parts do not account for becomes one frame part under
// frameVia. total is what the session budget counted, so a delivery the budget
// cut is cut here in the same place.
func Fit(parts []Part, total int, frameVia string) []Part {
	if total <= 0 {
		return nil
	}
	out := make([]Part, 0, min(len(parts), MaxPartsPerDelivery)+1)
	left := total
	for _, p := range parts {
		if left == 0 || len(out) == MaxPartsPerDelivery {
			break
		}
		p, ok := p.Clean()
		if !ok || p.Chars == 0 || p.Kind == KindFrame {
			continue
		}
		if p.Chars > left {
			p.Chars = left
		}
		left -= p.Chars
		out = append(out, p)
	}
	if left > 0 {
		out = append(out, Part{Via: frameVia, Kind: KindFrame, Chars: left})
	}
	return out
}

// Sum is the characters in parts.
func Sum(parts []Part) int {
	n := 0
	for _, p := range parts {
		n += p.Chars
	}
	return n
}
