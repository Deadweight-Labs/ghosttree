package redact

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// looksSecret decides whether the text behind a label is a secret. It errs on
// the side of leaving prose and code alone: a label in front of an ordinary
// word is the common case, a real password the rare one.
//
// The value is one token of 6 to 200 characters that is not
//   - a placeholder: $VAR, ${VAR}, <password>, ***, xxxx, changeme, your-token, ...
//   - code: os.Getenv("X"), r.FormValue("pw"), request.form['pw'], {{ x }}
//   - a path or a URL: /etc/passwd, ./secret.txt, https://example.com/reset
//   - an identifier or number: process.env.DB_PASSWORD, user_token_v2, 10.0.0.1
//
// and it has at least two kinds of characters from lower case, upper case,
// digit and special character, one of them a digit or special character: a
// word with a capital letter (Sommer, getUserName) is not enough. After a
// bare "Passwort " or "Passwort ist " the value needs a digit or two special
// characters, because there the label may be an ordinary word of a sentence.
// A value of one kind only (correcthorsebatterystaple, 123456789012) counts
// from 12 characters, and only behind ":" or "->", in quotes, or in KEY=value
// without spaces; "Passwort ist zurückgesetzt" is prose and "x = longidentifier"
// is code.
//
// A PIN is the exception: only 4 to 8 digits, behind a symbol or "ist".
func looksSecret(v string, level int, kind labelKind) bool {
	if strings.HasPrefix(v, "[REDACTED") {
		return false
	}
	// What closes the sentence comes off first (t0), then what closes a
	// bracket or a quotation (t1): "{x.Secret}," is a placeholder in braces,
	// "[hidden]" loses its bracket to the second trimming.
	sentence := ".,;:"
	if level < levelSymbol {
		sentence += "!?"
	}
	t0 := strings.TrimRight(v, sentence)
	t1 := strings.TrimRight(v, sentence+")]}\"'»”’›")
	n := utf8.RuneCountInString(t1)
	if kind == kindPIN {
		return level >= levelWord && n >= 4 && n <= 8 && isDigits(t1)
	}
	if n < 6 || n > maxValueRunes {
		return false
	}
	for _, t := range [...]string{t0, t1} {
		if isPlaceholder(t) || isCode(t) || isPathOrURL(t) {
			return false
		}
	}
	return hasSecretShape(t1, level)
}

func isDigits(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isSepRune(r rune) bool { return r == '-' || r == '_' || r == '.' }

type shape struct {
	lower, upper, digit, sep bool
	specials                 int // characters that are none of the others
}

func shapeOf(v string) (sh shape) {
	for _, r := range v {
		switch {
		case unicode.IsUpper(r):
			sh.upper = true
		case unicode.IsLetter(r):
			sh.lower = true
		case unicode.IsDigit(r):
			sh.digit = true
		case isSepRune(r):
			sh.sep = true
		default:
			sh.specials++
		}
	}
	return sh
}

func (sh shape) classes() (n int) {
	for _, b := range [...]bool{sh.lower, sh.upper, sh.digit, sh.specials > 0} {
		if b {
			n++
		}
	}
	return n
}

func hasSecretShape(v string, level int) bool {
	sh := shapeOf(v)
	if sh.sep && ordinaryParts(v) || constRe.MatchString(v) {
		return false
	}
	// User's, l'ordre: a word with an apostrophe
	if strings.Count(v, "'")+strings.Count(v, "’") == 1 && wordLike(strings.NewReplacer("'", "", "’", "").Replace(v)) {
		return false
	}
	// @agents, #general, $HOME: a mention, a tag or a variable, not a password
	if rest := strings.TrimLeft(v, "@#$%&~^+"); len(v)-len(rest) == 1 && wordLike(rest) {
		return false
	}
	switch classes := sh.classes(); {
	case classes >= 2:
		// After a bare "Passwort " a single special character is not enough:
		// "rotieren?" and "summary:hover" are prose and style sheets.
		if level >= levelSymbol {
			return sh.digit || sh.specials > 0
		}
		return sh.digit || sh.specials >= 2
	case classes == 1 && !sh.sep:
		return level == levelColon && utf8.RuneCountInString(v) >= 12 && (sh.digit || sh.lower)
	}
	return false
}

// wordLike is a word in one of the three usual spellings: lower case, Title
// or UPPER.
func wordLike(p string) bool {
	rs := []rune(p)
	if len(rs) == 0 {
		return false
	}
	upper := 0
	for _, r := range rs {
		if !unicode.IsLetter(r) {
			return false
		}
		if unicode.IsUpper(r) {
			upper++
		}
	}
	return upper == 0 || upper == len(rs) || upper == 1 && unicode.IsUpper(rs[0])
}

// wordDigitsRe is a version or a numbered word: v2, 2fa, sha256, user1, 2026.
// Two letters and a digit (kx9) are not: random blocks look like that.
var wordDigitsRe = regexp.MustCompile(`^(?:[vV][0-9]{1,3}|[A-Za-z]{3,}[0-9]{1,4}|[0-9]{1,4}[A-Za-z]{2,}|[0-9]{1,4})$`)

// ordinaryParts is true when v, split at - _ and ., consists of words,
// numbers up to four digits and words with such a number (v2, 2fa): an
// identifier, a version, a date or an address, not a password.
func ordinaryParts(v string) bool {
	parts := strings.FieldsFunc(v, isSepRune)
	if len(parts) == 0 {
		return false
	}
	for _, p := range parts {
		if !wordLike(p) && !wordDigitsRe.MatchString(p) && !camelRe.MatchString(p) {
			return false
		}
	}
	return true
}

var (
	shellVarRe = regexp.MustCompile(`^(?:\$[A-Za-z_][A-Za-z0-9_]*|%[A-Za-z_][A-Za-z0-9_]*%|\$\d+)$`)
	callRe     = regexp.MustCompile(`^[&*]?[A-Za-z_$@][A-Za-z0-9_$.]*[(\[{]`)
	// uint16, *int32, []byte: a type name
	typeRe = regexp.MustCompile(`^(?:\[\]|[*&])*(?:u?int(?:8|16|32|64)?|uintptr|float(?:32|64)|complex(?:64|128)|byte|rune|string|bool)$`)
	// 0x7f879679, -0x0080: a number literal
	hexRe   = regexp.MustCompile(`^-?0[xX][0-9a-fA-F]+$`)
	driveRe = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
	// --agent, -u: a command line option
	optionRe = regexp.MustCompile(`^-{1,2}[A-Za-z]`)
	// machine=<name>, refresh=true: a setting, not a password
	assignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*=`)
	// *token, &cfg.Token: a pointer or an address of a name
	addressRe = regexp.MustCompile(`^[*&](?:[A-Za-z_][A-Za-z_]*|[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z0-9_]+)+|(?:[A-Z][a-z0-9]+){2,})$`)
	// json:"token,omitempty": a struct tag
	tagRe = regexp.MustCompile("^`?[a-z][a-z0-9_]*:\"")
	// localhost:8080/get, 10.0.0.1:5432: an address with a port
	hostPortRe = regexp.MustCompile(`^[A-Za-z0-9.-]+:[0-9]{2,5}(?:/|$)`)
	// NL80211_ATTR_SAE_PASSWORD, C.SSL_ERROR: a constant
	constRe = regexp.MustCompile(`^[A-Z][A-Z0-9]*(?:[_.][A-Z0-9]+)+$`)
	// getUserName, RawValue: an identifier in camel case, every hump of three letters or more
	camelRe = regexp.MustCompile(`^[A-Z]?[a-z]{2,}(?:[A-Z][a-z]{2,})+$|^(?:[A-Z][a-z]{2,}){2,}$`)
	// +token, +c.cfg.Key: the rest of a string concatenation
	plusRe = regexp.MustCompile(`^\+[A-Za-z_]`)
)

var placeholderWords = map[string]bool{
	"password": true, "passwort": true, "passwd": true, "pass": true, "pwd": true,
	"secret": true, "token": true, "apikey": true, "username": true, "user": true,
	"changeme": true, "changeit": true, "replaceme": true, "example": true,
	"sample": true, "dummy": true, "redacted": true, "hidden": true, "masked": true,
	"placeholder": true, "none": true, "null": true, "nil": true, "undefined": true,
	"empty": true, "unset": true, "required": true, "optional": true,
}

var placeholderPrefixes = []string{
	"changeme", "changeit", "replaceme", "yourpass", "yourtoken", "yoursecret",
	"yourapi", "yourkey", "example", "placeholder", "dummy", "redacted",
}

// isPlaceholder is true for a value that stands for a secret instead of being
// one: a variable, a template, a mask or an example word. changeme is one: it
// is the default everybody knows, not a secret somebody chose.
func isPlaceholder(v string) bool {
	if v == "" || strings.HasPrefix(v, "[REDACTED") {
		return true
	}
	if shellVarRe.MatchString(v) || strings.Contains(v, "${") || strings.Contains(v, "{{") || strings.Contains(v, "$(") {
		return true
	}
	first, last := v[0], v[len(v)-1]
	if first == '<' && last == '>' || first == '{' && last == '}' || first == '[' && last == ']' || first == '(' && last == ')' {
		return true
	}
	if strings.HasPrefix(v, "%(") || strings.HasPrefix(v, "<%") || v == "%s" || v == "%v" || v == "%d" {
		return true
	}
	// ******, xxxxxx, 000000, ......, xxxx-xxxx
	core := strings.Map(func(r rune) rune {
		if isSepRune(r) || r == ' ' {
			return -1
		}
		return unicode.ToLower(r)
	}, v)
	if core == "" {
		return true
	}
	rs := []rune(core)
	same := true
	for _, r := range rs {
		if r != rs[0] {
			same = false
			break
		}
	}
	if same {
		return true
	}
	if placeholderWords[core] {
		return true
	}
	for _, p := range placeholderPrefixes {
		if strings.HasPrefix(core, p) {
			return true
		}
	}
	return false
}

// isCode is true for a value that is an expression: a call, an index, a
// template.
func isCode(v string) bool {
	switch v[0] {
	case '(', '[', '{', '<': // a parenthesis or a template, not a password
		return true
	}
	return callRe.MatchString(v) || optionRe.MatchString(v) || assignRe.MatchString(v) || addressRe.MatchString(v) ||
		typeRe.MatchString(v) || hexRe.MatchString(v) || tagRe.MatchString(v) || hostPortRe.MatchString(v) ||
		strings.Contains(v, "%s") || strings.Contains(v, "%d") || strings.Contains(v, "%v") || strings.Contains(v, "%q") || strings.Contains(v, "%T") ||
		strings.Contains(v, `\n`) || strings.Contains(v, `\t`) || strings.Contains(v, `\r`) ||
		strings.Contains(v, "=>") || strings.Contains(v, "()") || strings.Contains(v, "::") ||
		strings.Contains(v, "{}") || strings.Contains(v, "[]") ||
		strings.Contains(v, "(\"") || strings.Contains(v, "('") ||
		strings.Contains(v, "\"+") || strings.Contains(v, "+\"") || plusRe.MatchString(v) ||
		strings.Contains(v, "','") || strings.Contains(v, "\",\"") ||
		strings.Contains(v, "}}") || strings.Contains(v, "</") || strings.Contains(v, "/>") || strings.Contains(v, "><") ||
		strings.Contains(v, ".*") || strings.Contains(v, "[^") || strings.Contains(v, `\(`) || strings.Contains(v, `\d`) || strings.Contains(v, `\w`)
}

// isPathOrURL is true for a file path or a URL. A URL with credentials is
// handled by its own rule before this one.
func isPathOrURL(v string) bool {
	if strings.Contains(v, "://") {
		return true
	}
	switch {
	case strings.HasPrefix(v, "/"), strings.HasPrefix(v, "~/"), strings.HasPrefix(v, "./"),
		strings.HasPrefix(v, "../"), strings.HasPrefix(v, `\\`), strings.HasPrefix(v, "www."),
		driveRe.MatchString(v):
		return true
	}
	if !strings.Contains(v, "/") {
		return false
	}
	// Every segment is a name, or every segment after the first is: the first
	// may carry a prefix as in llm-key:/etc/ghosttree/llm-key. A secret with
	// slashes (base64) has segments that are no names.
	segs := strings.Split(v, "/")
	names, others := 0, 0
	for i, seg := range segs {
		switch {
		case seg == "":
		case ordinaryParts(seg):
			names++
		case i == 0:
			others++
		default:
			return false
		}
	}
	return others == 0 || names >= 2
}
