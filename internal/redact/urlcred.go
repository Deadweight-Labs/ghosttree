package redact

import (
	"strings"
	"unicode"
)

const urlMarker = "[REDACTED:url]"

// maxURLCredential bounds how far after "://" the credentials are searched.
const maxURLCredential = 1024

// urlCredentialSpans returns the byte ranges of the passwords in URLs of the
// form scheme://user:password@host. A placeholder such as "password",
// "${DB_PASS}" or "<pw>" is documentation and stays. Neither the user nor the
// password may hold whitespace, a slash, ? # a quote, an angle bracket or a
// backslash: a match never reaches out of a JSON string, and a password with
// such a character is percent-encoded in a URL anyway.
func urlCredentialSpans(s string, mode Mode) [][]int {
	if !strings.Contains(s, "://") || strings.IndexByte(s, '@') < 0 {
		return nil
	}
	sc := labelScanner{s: s, json: mode == JSON}
	plain := s // the scheme is read from text in which no escape sequence looks like letters
	if mode == JSON {
		plain = jsonShadow(s)
	}
	var spans [][]int
	for from := 0; ; {
		p := strings.Index(s[from:], "://")
		if p < 0 {
			return spans
		}
		p += from
		from = p + 3
		if !hasScheme(plain, p) {
			continue
		}
		colon := -1
		for k, n := from, 0; k < len(s) && n < maxURLCredential; n++ {
			u := sc.unit(k)
			if u.r == ':' && colon < 0 {
				colon = k + u.n
			}
			if u.r == '@' {
				if colon >= 0 && colon < k && !isPlaceholder(sc.decode(colon, k)) {
					spans = append(spans, []int{colon, k})
					from = k + u.n
				}
				break
			}
			if unicode.IsSpace(u.r) || strings.ContainsRune(`/?#"'<>\`, u.r) {
				break
			}
			k += u.n
		}
	}
}

// hasScheme reports whether "://" at s[p] follows a scheme: a letter, then
// letters, digits and + . - for at least two characters in all.
func hasScheme(s string, p int) bool {
	start := p
	for start > 0 && p-start < 16 {
		c := s[start-1]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '.' || c == '-' {
			start--
			continue
		}
		break
	}
	for ; start < p; start++ {
		if c := s[start]; c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			return p-start >= 2
		}
	}
	return false
}

func redactURLCredentials(s string, mode Mode, stats Stats) string {
	spans := urlCredentialSpans(s, mode)
	if len(spans) == 0 {
		return s
	}
	if stats != nil {
		stats["url"] += int64(len(spans))
	}
	return replaceSpans(s, spans, urlMarker)
}
