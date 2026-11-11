// Package chunktext turns one transcript line into the role and text that the
// archive keeps for it, and derives them again from a stored line.
//
// It is a leaf: the collector uses it to fill Chunk.Text before upload, and the
// store uses it to read the text of a chunk whose column is empty
// (session_chunks.text is no longer written, IDEA-25). The collector imports
// the store, so the parsers cannot live in the collector.
//
// Parsers never fail: anything not understood yields an empty ParsedLine and
// the caller still stores the raw line.
package chunktext

import (
	"github.com/Deadweight-Labs/ghosttree/internal/redact"
	"github.com/Deadweight-Labs/ghosttree/internal/slim"
)

// Version changes when Derive yields a different text for lines that are
// already stored. Change the parsers, the redaction or the cap, and this
// number goes up with them.
const Version = 1

// ParsedLine is what a parser reads from one line.
type ParsedLine struct {
	Role string // "user"|"assistant"|"tool"|... ("" for line types it does not know)
	Text string // extracted text, "" if none
}

// Parse returns the parser of a harness: Codex has its own, everything else
// reads Claude Code lines.
func Parse(harness string) func([]byte) ParsedLine {
	if harness == "codex" {
		return ParseCodexLine
	}
	return ParseClaudeLine
}

// Derive returns role and text of a stored line the way the server would have
// stored them: parse, redact, cap. storedRaw is session_chunks.raw after
// unpacking, the slimmed line, not the original. The two differ only where the
// slimming shortened a long tool output, so the derived text is the shorter
// view of the same output.
func Derive(harness, storedRaw string) (role, text string) {
	p := Parse(harness)([]byte(storedRaw))
	text, _ = slim.Cap(redact.Redact(p.Text))
	return p.Role, text
}
