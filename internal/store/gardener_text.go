package store

import (
	"regexp"
	"strings"
)

// instructionPatterns find the phrases with which a text written for an agent
// to obey is usually built. A ghost description is data about a path; the
// gardener writes it from code and merge text that anyone may have shaped, and
// the hooks put it in front of other agents. A description that talks to its
// reader, overrides rules, asks for a command or an upload, or asks for secrecy
// is not filed. The list is a heuristic and catches the common wording only; the
// delivery frames every description as data besides.
var instructionPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"override of earlier instructions", regexp.MustCompile(`(?i)\b(ignore|disregard|forget|override|discard)\b[^.\n]{0,40}\b(previous|prior|above|earlier|all|any|your|these|the)\b[^.\n]{0,30}\b(instructions?|rules?|prompts?|context|guidelines?|constraints?)\b`)},
	{"override of earlier instructions", regexp.MustCompile(`(?i)\b(ignoriere|vergiss|missachte|übergehe)\b[^.\n]{0,40}\b(anweisung|regel|vorgabe|prompt|kontext)`)},
	{"new instructions", regexp.MustCompile(`(?i)\b(new|updated|revised|real|actual) (instructions?|system prompt|task)\b|\bneue anweisung`)},
	{"addresses the reader", regexp.MustCompile(`(?im)^\s*(?:[-*>#]+\s*)?(?:to the |note to |hinweis an |an die )?(?:agents?|agenten|assistants?|assistent|ai|llm|claude|codex|model|modell)\s*[:,\-–—]`)},
	{"addresses the reader", regexp.MustCompile(`(?i)\b(you (must|have to|need to|should|are required to|will)|du musst|du sollst|ihr müsst)\b`)},
	{"asks for a command", regexp.MustCompile("(?im)(?:^|[.!:]\\s+)(?:[-*>]+\\s*)?(run|execute|exec|invoke|führe|starte)\\b[^\\n]{0,24}(`[^`\\n]+`|\\$\\()")},
	{"pipes a download into a shell", regexp.MustCompile(`(?i)\b(curl|wget)\b[^\n|]*\|\s*(ba|z|da)?sh\b|\bbase64\s+(-d|--decode)\b|\brm\s+-rf\s+[/~]`)},
	{"asks to fetch or send somewhere", regexp.MustCompile(`(?i)\b(fetch|visit|open|send|post|upload|download|exfiltrate|forward|besuche|öffne|sende|lade|schicke)\b[^\n]{0,60}https?://`)},
	{"asks for secrecy", regexp.MustCompile(`(?i)\b(do not|don't|never|without) (tell|inform|mention|reveal|show|ask)\b[^.\n]{0,30}\b(user|human|owner|anyone|developer|person)\b|\b(secretly|silently without|verschweige|ohne (den nutzer|zu fragen))\b`)},
}

// instructionLike says which pattern a text matches, "" if it matches none.
func instructionLike(s string) string {
	for _, p := range instructionPatterns {
		if p.re.MatchString(s) {
			return p.name
		}
	}
	return ""
}

// InstructionLike is instructionLike for the callers outside the store.
func InstructionLike(s string) string { return strings.TrimSpace(instructionLike(s)) }
