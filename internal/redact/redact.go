// Package redact strips common credential patterns before data is stored or
// leaves the machine. It is a baseline filter, not a guarantee.
//
// Three kinds of rule, applied in this order: tokens with a fixed prefix
// (private keys, sk-ant-, ghp_, AKIA, ...), the password part of a URL with
// credentials, and values behind a label ("Passwort: ...", "api_key=..."). The
// last one has no recognisable format to go by, so it looks at the value
// instead; see labeled.go.
package redact

import (
	"regexp"
	"strings"
)

type SecretMatch struct {
	Label string
	Line  int
}

// Mode says what kind of string is being redacted.
type Mode int

const (
	// Text is ordinary text, such as the extracted text of a chunk.
	Text Mode = iota
	// JSON is a JSON document in its written form, such as the raw JSONL line
	// of a chunk: quotes, newlines and non-ASCII characters appear as escape
	// sequences, and an unescaped quote ends the string the text sits in. The
	// result is valid JSON again whenever the input was.
	JSON
)

// Stats counts replacements by kind (a pattern label, "url" or
// "labeled:<kind>"). It never holds a value.
type Stats map[string]int64

type pattern struct {
	label    string
	re       *regexp.Regexp
	literals []string
}

var patterns = []pattern{
	{"privatekey", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`), []string{"-----BEGIN "}},
	{"anthropic", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}\b`), []string{"sk-ant-"}},
	{"openai", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{28,}\b`), []string{"sk-"}},
	{"github", regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{30,}\b|\bgithub_pat_[A-Za-z0-9_]{30,}\b`), []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_"}},
	{"aws", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), []string{"AKIA", "ASIA"}},
	{"slack", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`), []string{"xox"}},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`), []string{"eyJ"}},
}

func (p *pattern) possible(s string) bool {
	if len(p.literals) == 0 {
		return true
	}
	for _, literal := range p.literals {
		if strings.Contains(s, literal) {
			return true
		}
	}
	return false
}

// spans returns the byte ranges of the matches in s. In JSON mode the pattern
// runs on a copy in which every escape sequence is blanked out, so that
// "\nsk-ant-..." is a token after a newline and not a word that starts with
// "n"; the ranges are the same in both strings.
func (p *pattern) spans(s string, mode Mode) [][]int {
	if !p.possible(s) {
		return nil
	}
	if mode == JSON {
		s = jsonShadow(s)
	}
	return p.re.FindAllStringIndex(s, -1)
}

// jsonShadow returns s with every JSON escape sequence replaced by spaces of
// the same length.
func jsonShadow(s string) string {
	if strings.IndexByte(s, '\\') < 0 {
		return s
	}
	b := []byte(s)
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' || i+1 >= len(b) {
			continue
		}
		n := 2
		if b[i+1] == 'u' && i+6 <= len(b) {
			if _, ok := hex4(s[i+2 : i+6]); ok {
				n = 6
			}
		}
		for k := 0; k < n && i+k < len(b); k++ {
			b[i+k] = ' '
		}
		i += n - 1
	}
	return string(b)
}

// Redact replaces the secrets in ordinary text.
func Redact(s string) string { return Apply(s, Text, nil) }

// RedactJSON replaces the secrets in a JSON document in its written form, for
// example one stored JSONL line.
func RedactJSON(s string) string { return Apply(s, JSON, nil) }

// maxPasses bounds Apply. A value that is replaced can take with it a quote
// mark that closed an earlier quotation, which changes how the text before it
// reads; a second pass settles that. Text without a secret is read once.
const maxPasses = 6

// Apply is Redact and RedactJSON with a counter. stats may be nil. Redacting
// the result again changes nothing.
func Apply(s string, mode Mode, stats Stats) string {
	for pass := 0; pass < maxPasses; pass++ {
		next := applyOnce(s, mode, stats)
		if next == s {
			break
		}
		s = next
	}
	return s
}

func applyOnce(s string, mode Mode, stats Stats) string {
	for i := range patterns {
		p := &patterns[i]
		spans := p.spans(s, mode)
		if len(spans) == 0 {
			continue
		}
		s = replaceSpans(s, spans, "[REDACTED:"+p.label+"]")
		if stats != nil {
			stats[p.label] += int64(len(spans))
		}
	}
	s = redactURLCredentials(s, mode, stats)
	return redactLabeled(s, mode, stats)
}

func replaceSpans(s string, spans [][]int, marker string) string {
	var b strings.Builder
	b.Grow(len(s))
	prev := 0
	for _, sp := range spans {
		b.WriteString(s[prev:sp[0]])
		b.WriteString(marker)
		prev = sp[1]
	}
	b.WriteString(s[prev:])
	return b.String()
}

// FindSecrets reports where Redact would act, without changing anything. It
// never returns a value.
func FindSecrets(s string) []SecretMatch { return FindSecretsIn(s, Text) }

// FindSecretsIn is FindSecrets for a string of the given mode.
func FindSecretsIn(s string, mode Mode) []SecretMatch {
	var matches []SecretMatch
	var covered [][]int
	line := func(at int) int { return strings.Count(s[:at], "\n") + 1 }
	for i := range patterns {
		p := &patterns[i]
		for _, location := range p.spans(s, mode) {
			matches = append(matches, SecretMatch{Label: p.label, Line: line(location[0])})
			covered = append(covered, location)
		}
	}
	for _, location := range urlCredentialSpans(s, mode) {
		matches = append(matches, SecretMatch{Label: "url", Line: line(location[0])})
		covered = append(covered, location)
	}
	for _, h := range scanLabeled(s, mode) {
		// Redact would have replaced this value already: a token behind a label
		// is one finding, not two.
		if overlaps(covered, h.start, h.end) {
			continue
		}
		matches = append(matches, SecretMatch{Label: "labeled", Line: line(h.start)})
	}
	return matches
}

func overlaps(spans [][]int, start, end int) bool {
	for _, sp := range spans {
		if sp[0] < end && start < sp[1] {
			return true
		}
	}
	return false
}
