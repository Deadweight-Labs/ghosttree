package redact

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// LiteralMarker replaces a value that was named as known to be secret.
const LiteralMarker = "[REDACTED:literal]"

// QuoteMarker replaces a stored quotation that cannot be kept in redacted form.
const QuoteMarker = "[REDACTED:quote]"

// MinLiteralRunes is the shortest value ParseLiterals accepts. A shorter one
// would match half of the stored text.
const MinLiteralRunes = 4

// maxLiteralsFile bounds what ParseLiterals reads; a list of known values is
// a few lines.
const maxLiteralsFile = 1 << 20

// maxLiteralPasses bounds Literals.Apply, like maxPasses bounds Apply.
const maxLiteralPasses = 4

// ErrLiterals is the error class of everything wrong with a list of known
// values. Its messages carry line numbers and reasons, never a value.
var ErrLiterals = errors.New("known values")

// Literals replaces values that are known to be secret wherever they stand,
// with or without a label in front of them. It is the answer to a leak whose
// value is known but written down without any recognisable form: in a command
// line, in the output of grep, as a fragment.
//
// A value matches in its exact spelling and in lower case: the full-text
// columns are case-insensitive, and fragments of a value turn up in lower case
// in command output. Other spellings (all capitals, mixed) do not match.
//
// In JSON mode the comparison is made on the decoded characters, so a value
// that is written as "\u0026" for "&" or as "\"" matches, and a match never
// leaves the JSON string it starts in: the result is valid JSON whenever the
// input was, and a number or a key's structure is never replaced.
type Literals struct {
	byFirst   map[rune][]litPattern
	startText [256]bool
	startJSON [256]bool
	values    int
}

type litPattern struct{ runes []rune }

// NewLiterals checks the values and prepares them for matching.
func NewLiterals(values []string) (*Literals, error) {
	return newLiterals(values, nil)
}

// ParseLiterals reads one value per line. Blank lines and lines that start
// with # are ignored; a value that itself starts with # is written \# . Spaces
// around a value, a CR before the line break and a byte order mark at the start
// are dropped. Values shorter than MinLiteralRunes characters, values that are
// not valid UTF-8 or hold a control character, and values that are part of a
// redaction marker are refused: with an error that names the line, not the
// value.
func ParseLiterals(r io.Reader) (*Literals, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxLiteralsFile+1))
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read the list: %v", ErrLiterals, err)
	}
	if len(data) > maxLiteralsFile {
		return nil, fmt.Errorf("%w: the list is larger than %d bytes", ErrLiterals, maxLiteralsFile)
	}
	var values []string
	var lines []int
	for n, line := range strings.Split(strings.TrimPrefix(string(data), "\ufeff"), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "", strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, `\#`):
			line = line[1:]
		}
		values = append(values, line)
		lines = append(lines, n+1)
	}
	return newLiterals(values, lines)
}

// newLiterals builds the matcher. lines, when given, names the line of each
// value for the error messages.
func newLiterals(values []string, lines []int) (*Literals, error) {
	where := func(i int) string {
		if lines != nil {
			return fmt.Sprintf("line %d", lines[i])
		}
		return fmt.Sprintf("value %d", i+1)
	}
	l := &Literals{byFirst: map[rune][]litPattern{}}
	seen := map[string]bool{}
	for i, v := range values {
		switch {
		case !utf8.ValidString(v):
			return nil, fmt.Errorf("%w: %s is not valid UTF-8", ErrLiterals, where(i))
		case strings.IndexFunc(v, unicode.IsControl) >= 0:
			return nil, fmt.Errorf("%w: %s holds a control character", ErrLiterals, where(i))
		case utf8.RuneCountInString(v) < MinLiteralRunes:
			return nil, fmt.Errorf("%w: %s is shorter than %d characters", ErrLiterals, where(i), MinLiteralRunes)
		case inMarker(v):
			return nil, fmt.Errorf("%w: %s is part of a redaction marker; it would be replaced again and again", ErrLiterals, where(i))
		}
		counted := false
		for _, form := range []string{v, strings.ToLower(v)} {
			if seen[form] {
				continue
			}
			seen[form] = true
			rs := []rune(form)
			l.byFirst[rs[0]] = append(l.byFirst[rs[0]], litPattern{rs})
			l.startText[firstByte(rs[0])] = true
			if !counted {
				counted = true
				l.values++
			}
		}
	}
	if l.values == 0 {
		return nil, fmt.Errorf("%w: the list holds no value", ErrLiterals)
	}
	l.startJSON = l.startText
	// A backslash may start an escaped character; a quote is tracked to know
	// whether a match would stand inside a string.
	l.startJSON['\\'], l.startJSON['"'] = true, true
	for _, pats := range l.byFirst {
		// the longest first: a value that contains another one wins
		sort.SliceStable(pats, func(i, j int) bool { return len(pats[i].runes) > len(pats[j].runes) })
	}
	return l, nil
}

func firstByte(r rune) byte {
	if r < utf8.RuneSelf {
		return byte(r)
	}
	var b [utf8.UTFMax]byte
	utf8.EncodeRune(b[:], r)
	return b[0]
}

// inMarker reports whether v is, in either spelling, a part of a marker or
// holds the start of one.
func inMarker(v string) bool {
	lower := strings.ToLower(v)
	if strings.Contains(lower, "[redacted:") {
		return true
	}
	for _, m := range markers() {
		if strings.Contains(strings.ToLower(m), lower) {
			return true
		}
	}
	return false
}

func markers() []string {
	m := []string{labeledMarker, urlMarker, LiteralMarker, QuoteMarker}
	for _, p := range patterns {
		m = append(m, "[REDACTED:"+p.label+"]")
	}
	return m
}

// Len is the number of distinct values.
func (l *Literals) Len() int {
	if l == nil {
		return 0
	}
	return l.values
}

// Apply replaces every known value in s by LiteralMarker and counts the
// replacements in stats["literal"] (stats may be nil). Replacing the result
// again changes nothing. A nil *Literals leaves s alone.
func (l *Literals) Apply(s string, mode Mode, stats Stats) string {
	if l == nil {
		return s
	}
	for pass := 0; pass < maxLiteralPasses; pass++ {
		spans := l.scan(s, mode, false)
		if len(spans) == 0 {
			break
		}
		s = replaceSpans(s, spans, LiteralMarker)
		if stats != nil {
			stats["literal"] += int64(len(spans))
		}
	}
	return s
}

// Contains reports whether ordinary text holds a known value.
func (l *Literals) Contains(s string) bool {
	return l != nil && len(l.scan(s, Text, true)) > 0
}

// scan returns the byte ranges of the matches, left to right and without
// overlap. At one position the longest value wins. With first set it stops at
// the first match.
func (l *Literals) scan(s string, mode Mode, first bool) [][]int {
	json := mode == JSON
	start := &l.startText
	if json {
		start = &l.startJSON
	}
	sc := labelScanner{s: s, json: json}
	// Plain text has no structure; in JSON a match must stand inside a string.
	inString := !json
	var spans [][]int
	for i := 0; i < len(s); {
		c := s[i]
		if !start[c] {
			i++
			continue
		}
		if json && c == '"' {
			inString = !inString
			i++
			continue
		}
		// From here i is the start of a character: the loop moves a whole
		// character or escape sequence at a time, so a match never starts in
		// the middle of one.
		u := sc.unit(i)
		if inString {
			if end := l.matchAt(&sc, i, u); end > 0 {
				spans = append(spans, []int{i, end})
				if first {
					return spans
				}
				i = end
				continue
			}
		}
		i += u.n
	}
	return spans
}

// matchAt returns the end of the longest value that starts with the character
// u at s[i], or 0.
func (l *Literals) matchAt(sc *labelScanner, i int, u unit) int {
	for _, p := range l.byFirst[u.r] {
		j := i + u.n
		matched := true
		for _, r := range p.runes[1:] {
			if j >= len(sc.s) {
				matched = false
				break
			}
			v := sc.unit(j)
			// A quote that is not escaped ends the string: no value contains it.
			if v.r != r || sc.structQuote(v) {
				matched = false
				break
			}
			j += v.n
		}
		if matched {
			return j
		}
	}
	return 0
}
