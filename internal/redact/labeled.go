package redact

import (
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Secrets behind a label have no format of their own, so the rule has two
// halves: a label (passwort, password, pw, secret, token, api key, ...), then
// a separator, then a value that looks like a secret. Only the value is
// replaced; the label stays, so the text still reads.
//
//	Passwort: Hx7!kQ2pz            ->  Passwort: [REDACTED:labeled]
//	pw=Sommer2026!                 ->  pw=[REDACTED:labeled]
//	"api_key": "k7k7k7k7m2m2m2m2"  ->  "api_key": "[REDACTED:labeled]"
//	das Passwort ist zu kurz       ->  unchanged
//	password := r.FormValue("pw")  ->  unchanged
//
// What looks like a secret is decided in value.go.

const labeledMarker = "[REDACTED:labeled]"

// maxValueRunes bounds how far a value is read; a longer run is no password.
const maxValueRunes = 200

type labelKind uint8

const (
	kindPassword labelKind = iota + 1
	kindPIN
	kindSecret
	kindAPIKey
	kindToken
	kindCredentials
)

var kindName = [...]string{"", "password", "pin", "secret", "apikey", "token", "credentials"}

// How firmly a label is tied to the text after it.
const (
	levelBare   = iota // only whitespace: "Passwort Hx7!kQ2pz"
	levelWord          // "Passwort ist Hx7!kQ2pz", "password is ..."
	levelSymbol        // "=", ":=", "=>": in code the value is mostly a name
	levelColon         // ":", "->", or a quotation behind a symbol: "Passwort: x", password = "x"
)

type hit struct {
	start, end int // the value
	kind       labelKind
}

// labelStart marks the first letters of all labels, in both cases.
var labelStart = func() (t [256]bool) {
	for _, c := range []byte("pkstazcb") {
		t[c] = true
		t[c-'a'+'A'] = true
	}
	return t
}()

func hasFold(s string, i int, lit string) bool {
	if i < 0 || i+len(lit) > len(s) {
		return false
	}
	for k := 0; k < len(lit); k++ {
		c := s[i+k]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != lit[k] {
			return false
		}
	}
	return true
}

// sepWord matches an optional separator and then the word.
func sepWord(s string, i int, word string) (int, bool) {
	if i < len(s) && (s[i] == '_' || s[i] == '-' || s[i] == ' ') {
		i++
	}
	if hasFold(s, i, word) {
		return i + len(word), true
	}
	return 0, false
}

// labelAt reports the label that starts at s[i]. long labels are unambiguous
// words that may end a German or English compound (Mailpasswort, csrftoken);
// the short ones need a boundary on the left.
func labelAt(s string, i int) (end int, kind labelKind, long bool) {
	switch s[i] | 0x20 {
	case 'p':
		switch {
		case hasFold(s, i, "pass"):
			j := i + 4
			switch {
			case hasFold(s, j, "word"), hasFold(s, j, "wort"):
				return j + 4, kindPassword, true
			case hasFold(s, j, "wd"):
				return j + 2, kindPassword, true
			case hasFold(s, j, "phrase"):
				return j + 6, kindPassword, true
			}
			return j, kindPassword, false
		case hasFold(s, i, "pwd"):
			return i + 3, kindPassword, false
		case hasFold(s, i, "pw"):
			return i + 2, kindPassword, false
		case hasFold(s, i, "pin"):
			return i + 3, kindPIN, false
		}
	case 'k':
		if hasFold(s, i, "kennwort") {
			return i + 8, kindPassword, true
		}
	case 's':
		if hasFold(s, i, "secret") {
			j := i + 6
			// secret_key, secret key, secretKey, secret_access_key
			if e, ok := sepWord(s, j, "access"); ok {
				if e2, ok := sepWord(s, e, "key"); ok {
					return e2, kindSecret, true
				}
			}
			if e, ok := sepWord(s, j, "key"); ok {
				return e, kindSecret, true
			}
			return j, kindSecret, true
		}
	case 't':
		if hasFold(s, i, "token") {
			return i + 5, kindToken, true
		}
	case 'a':
		if hasFold(s, i, "api") {
			j := i + 3
			if j < len(s) && (s[j] == '_' || s[j] == '-' || s[j] == ' ') {
				j++
			}
			for _, w := range [...]string{"key", "secret", "token"} {
				if hasFold(s, j, w) {
					return j + len(w), kindAPIKey, false
				}
			}
		}
	case 'z':
		if hasFold(s, i, "zugangsdaten") {
			return i + 12, kindCredentials, true
		}
	case 'c':
		if hasFold(s, i, "credential") {
			j := i + 10
			if j < len(s) && (s[j] == 's' || s[j] == 'S') {
				j++
			}
			return j, kindCredentials, true
		}
	case 'b':
		if hasFold(s, i, "bearer") {
			return i + 6, kindToken, false
		}
	}
	return 0, 0, false
}

type unit struct {
	r   rune
	n   int
	esc bool // written as an escape sequence (JSON only)
}

type labelScanner struct {
	s    string
	json bool
}

func hex4(s string) (rune, bool) {
	if len(s) != 4 {
		return 0, false
	}
	var r rune
	for i := 0; i < 4; i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			r = r<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	return r, true
}

// unit reads the character that starts at s[i]: in JSON mode an escape
// sequence counts as one character, so that a span never cuts one in half.
func (sc *labelScanner) unit(i int) unit {
	s := sc.s
	c := s[i]
	if c == '\\' && sc.json && i+1 < len(s) {
		switch s[i+1] {
		case '"':
			return unit{'"', 2, true}
		case '\\':
			return unit{'\\', 2, true}
		case '/':
			return unit{'/', 2, true}
		case 'n':
			return unit{'\n', 2, true}
		case 'r':
			return unit{'\r', 2, true}
		case 't':
			return unit{'\t', 2, true}
		case 'b':
			return unit{'\b', 2, true}
		case 'f':
			return unit{'\f', 2, true}
		case 'u':
			if i+6 <= len(s) {
				if r, ok := hex4(s[i+2 : i+6]); ok {
					if utf16.IsSurrogate(r) && i+12 <= len(s) && s[i+6] == '\\' && s[i+7] == 'u' {
						if r2, ok := hex4(s[i+8 : i+12]); ok {
							if d := utf16.DecodeRune(r, r2); d != utf8.RuneError {
								return unit{d, 12, true}
							}
						}
					}
					return unit{r, 6, true}
				}
			}
		}
		return unit{'\\', 1, false}
	}
	if c < utf8.RuneSelf {
		return unit{rune(c), 1, false}
	}
	r, n := utf8.DecodeRuneInString(s[i:])
	return unit{r, n, false}
}

// structQuote is a quote that ends a JSON string, not a character in it.
func (sc *labelScanner) structQuote(u unit) bool { return sc.json && u.r == '"' && !u.esc }

func isBlank(r rune) bool { return r == ' ' || r == '\t' }

func (sc *labelScanner) skipBlanks(j int) (int, int) {
	n := 0
	for j < len(sc.s) {
		u := sc.unit(j)
		if !isBlank(u.r) {
			break
		}
		j += u.n
		n++
	}
	return j, n
}

// leftBoundary is true when a short label at s[i] starts a word: not inside
// one, but after a separator, an escaped line break or a camelCase hump
// (dbPassword).
func (sc *labelScanner) leftBoundary(i int) bool {
	if i == 0 {
		return true
	}
	r := sc.prevRune(i)
	if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
		return true
	}
	return (unicode.IsLower(r) || unicode.IsDigit(r)) && sc.s[i] >= 'A' && sc.s[i] <= 'Z'
}

// escapeStartsAt reports whether the backslash at s[k] begins an escape
// sequence, not the second half of an escaped backslash.
func escapeStartsAt(s string, k int) bool {
	run := 0
	for ; k >= 0 && s[k] == '\\'; k-- {
		run++
	}
	return run%2 == 1
}

// prevRune is the character that ends just before s[i], with an escape
// sequence decoded in JSON mode.
func (sc *labelScanner) prevRune(i int) rune {
	s := sc.s
	if sc.json {
		if i >= 6 && s[i-6] == '\\' && s[i-5] == 'u' && escapeStartsAt(s, i-6) {
			if r, ok := hex4(s[i-4 : i]); ok {
				return r
			}
		}
		if i >= 2 && s[i-2] == '\\' && escapeStartsAt(s, i-2) {
			switch s[i-1] {
			case 'n':
				return '\n'
			case 'r':
				return '\r'
			case 't':
				return '\t'
			case 'b':
				return '\b'
			case 'f':
				return '\f'
			case '"', '\\', '/':
				return rune(s[i-1])
			}
		}
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return r
}

// rightBoundary is true when the label that ends at e is a whole word.
func (sc *labelScanner) rightBoundary(e int) bool {
	if e >= len(sc.s) {
		return true
	}
	r := sc.unit(e).r
	return r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

func isKeyQuote(r rune) bool {
	switch r {
	case '"', '\'', '`', '”', '’', '“':
		return true
	}
	return false
}

// closes reports whether c ends a quotation that open began.
func closes(open, c rune) bool {
	switch open {
	case '"', '\'', '`':
		return c == open
	case '„': // „
		return c == '“' || c == '”' || c == '"'
	case '“', '”': // “ ”
		return c == '”' || c == '"'
	case '‘', '’': // ‘ ’
		return c == '’' || c == '\''
	case '‚': // ‚
		return c == '‘' || c == '’' || c == '\''
	case '«': // «
		return c == '»'
	case '»': // »
		return c == '«'
	case '‹': // ‹
		return c == '›'
	case '›': // ›
		return c == '‹'
	}
	return false
}

// closesQuote reports whether u ends the quotation that open began. A quote
// mark that closes a quotation opened with the same mark is written the same
// way as the opener (structural or escaped); one that closes a quotation
// opened with another mark is a character inside the JSON string.
func (sc *labelScanner) closesQuote(open, u unit) bool {
	if u.r == '"' && open.r == '"' {
		return u.esc == open.esc
	}
	if u.r == '"' && sc.json && !u.esc {
		return false
	}
	return closes(open.r, u.r)
}

func isOpener(r rune) bool {
	switch r {
	case '"', '\'', '`', '„', '“', '”', '‘', '’', '‚', '«', '»', '‹', '›':
		return true
	}
	return false
}

// connector reads the separator between label and value from s[j:], after
// optional blanks (blanks says how many there were). It returns the position
// after it and how firmly it ties the value to the label.
func (sc *labelScanner) connector(j, blanks int) (int, int, bool) {
	s := sc.s
	if j >= len(s) {
		return j, levelBare, blanks > 0
	}
	switch u := sc.unit(j); u.r {
	case ':':
		next := j + u.n
		if next < len(s) {
			switch n := sc.unit(next); n.r {
			case ':': // "::" is a scope, not a label
				return j, 0, false
			case '=':
				return next + n.n, levelSymbol, true
			}
		}
		return next, levelColon, true
	case '=':
		next := j + u.n
		if next < len(s) {
			if n := sc.unit(next); n.r == '=' || n.r == '>' {
				next += n.n
			}
		}
		return next, levelSymbol, true
	case '-':
		if next := j + u.n; next < len(s) {
			if n := sc.unit(next); n.r == '>' {
				return next + n.n, levelColon, true
			}
		}
	case '→':
		return j + u.n, levelColon, true
	case '⇒':
		return j + u.n, levelSymbol, true
	}
	if blanks > 0 {
		for _, w := range [...]string{"ist", "is", "lautet"} {
			if hasFold(s, j, w) {
				e := j + len(w)
				if e >= len(s) {
					break
				}
				u := sc.unit(e)
				if isBlank(u.r) || isOpener(u.r) {
					return e, levelWord, true
				}
			}
		}
		return j, levelBare, true
	}
	return j, 0, false
}

// value reads what follows the label that ends at e. It returns the byte
// range of the value, how firmly the label is tied to it, and false when
// nothing that could be a value follows.
func (sc *labelScanner) value(e int) (vs, ve, level int, ok bool) {
	s := sc.s
	j := e
	// **Passwort**: x
	for j < len(s) && s[j] == '*' {
		j++
	}
	keyStruct := false
	if j < len(s) {
		if u := sc.unit(j); isKeyQuote(u.r) {
			keyStruct = sc.structQuote(u)
			j += u.n
		}
	}
	j, blanks := sc.skipBlanks(j)
	var good bool
	j, level, good = sc.connector(j, blanks)
	if !good {
		return 0, 0, 0, false
	}
	if level != levelBare {
		var after int
		j, after = sc.skipBlanks(j)
		// KEY=value, without spaces, is an environment variable or an option;
		// "key = value" with spaces is code.
		if level == levelSymbol && blanks == 0 && after == 0 {
			level = levelColon
		}
		// **Passwort:** x
		if k := j; k < len(s) && s[k] == '*' {
			for k < len(s) && s[k] == '*' {
				k++
			}
			if k >= len(s) || isBlank(sc.unit(k).r) {
				j, _ = sc.skipBlanks(k)
			}
		}
	}
	if j >= len(s) {
		return 0, 0, 0, false
	}
	u := sc.unit(j)
	if unicode.IsSpace(u.r) || sc.structQuote(u) && !keyStruct {
		return 0, 0, 0, false
	}
	var open unit
	quoted := false
	if isOpener(u.r) {
		quoted, open = true, u
		j += u.n
		if level == levelSymbol {
			level = levelColon
		}
	}
	// A JSON number, true or null behind a key: leave it, a placeholder in
	// its place would not be valid JSON.
	if sc.json && keyStruct && !quoted {
		return 0, 0, 0, false
	}
	if quoted {
		switch end, state := sc.closeQuote(j, open); state {
		case quoteClosed:
			return j, end, level, end > j
		case quoteSpaced:
			return 0, 0, 0, false
		}
		// not closed: read it as a bare value after the quote mark
	}
	k, count := j, 0
	for k < len(s) {
		u := sc.unit(k)
		if unicode.IsSpace(u.r) || sc.structQuote(u) {
			break
		}
		k += u.n
		if count++; count > maxValueRunes {
			return 0, 0, 0, false
		}
	}
	if k == j {
		return 0, 0, 0, false
	}
	return j, k, level, true
}

const (
	quoteOpen = iota
	quoteClosed
	quoteSpaced
)

// closeQuote looks for the end of a quoted value that starts at j. A quoted
// value holds one token: if the quotation has whitespace inside, it is not a
// password (quoteSpaced); if it never ends in the line, the value is read as
// if the quote mark were not there (quoteOpen).
func (sc *labelScanner) closeQuote(j int, open unit) (int, int) {
	s := sc.s
	spaced := false
	for k, count := j, 0; k < len(s); count++ {
		if count > maxValueRunes+1 {
			break
		}
		u := sc.unit(k)
		switch {
		case sc.structQuote(u) && !(open.r == '"' && !open.esc):
			// the JSON string ends before the quotation does
			return 0, quoteOpen
		case sc.closesQuote(open, u):
			if spaced {
				return k, quoteSpaced
			}
			return k, quoteClosed
		case u.r == '\n' || u.r == '\r':
			return 0, quoteOpen
		case unicode.IsSpace(u.r):
			spaced = true
		}
		k += u.n
	}
	return 0, quoteOpen
}

// decode returns the characters of s[a:b] as a plain string.
func (sc *labelScanner) decode(a, b int) string {
	if !sc.json || strings.IndexByte(sc.s[a:b], '\\') < 0 {
		return sc.s[a:b]
	}
	var sb strings.Builder
	for k := a; k < b; {
		u := sc.unit(k)
		sb.WriteRune(u.r)
		k += u.n
	}
	return sb.String()
}

// scanLabeled returns the values behind labels that look like secrets.
func scanLabeled(s string, mode Mode) []hit { return scanLabeledWith(s, mode, &labelStart) }

// scanLabeledWith is scanLabeled with the table of first letters as a
// parameter, so a test can run it without the shortcut.
func scanLabeledWith(s string, mode Mode, first *[256]bool) []hit {
	sc := labelScanner{s: s, json: mode == JSON}
	var hits []hit
	for i := 0; i < len(s); i++ {
		if !first[s[i]] {
			continue
		}
		e, kind, long := labelAt(s, i)
		if e == 0 {
			continue
		}
		if !sc.rightBoundary(e) {
			continue
		}
		if !long && !sc.leftBoundary(i) {
			continue
		}
		// a path component, not a label: /etc/passwd, secrets/token
		if i > 0 && (s[i-1] == '/' || s[i-1] == '\\') {
			continue
		}
		// the all-capitals PASS is a test result
		if kind == kindPassword && !long && e-i == 4 && s[i:e] == "PASS" {
			continue
		}
		vs, ve, level, ok := sc.value(e)
		if !ok {
			continue
		}
		// "pass" is a verb as well ("pass machine=x"): it counts as a label
		// only with a symbol behind it.
		if kind == kindPassword && !long && e-i == 4 && level < levelSymbol {
			continue
		}
		if !looksSecret(sc.decode(vs, ve), level, kind) {
			continue
		}
		hits = append(hits, hit{vs, ve, kind})
		i = ve - 1
	}
	return hits
}

func redactLabeled(s string, mode Mode, stats Stats) string {
	hits := scanLabeled(s, mode)
	if len(hits) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	prev := 0
	for _, h := range hits {
		b.WriteString(s[prev:h.start])
		b.WriteString(labeledMarker)
		prev = h.end
		if stats != nil {
			stats["labeled:"+kindName[h.kind]]++
		}
	}
	b.WriteString(s[prev:])
	return b.String()
}
