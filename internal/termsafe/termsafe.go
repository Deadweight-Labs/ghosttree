// Package termsafe keeps text from a server (knowledge, comments, documents,
// error messages) from steering the terminal it is printed on. An escape
// sequence in it can set the window title, rewrite what is on screen, or on some
// terminals write the clipboard. Line breaks and tabs stay; every other control
// character goes, and so do the characters that reorder text on screen.
package termsafe

import (
	"io"
	"strings"
	"unicode/utf8"
)

// drop reports whether r must not reach a terminal.
func drop(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x061c, r == 0x200e, r == 0x200f:
		// Bidirectional controls: they make text read differently from what it is.
		return true
	case r == 0x2028 || r == 0x2029:
		return true
	}
	return false
}

// Strip removes the characters that could steer a terminal. A carriage return
// stays in a line ending (\r\n), so text read back from a file is as it was; a
// lone one is dropped, since it lets a line overwrite the one before it.
func Strip(s string) string {
	if !needsStrip(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n == 1 {
			// A lone byte is not text; as 0x80 to 0x9f it would be a C1 control
			// for a terminal that reads 8-bit.
			if s[i] >= 0x80 && s[i] <= 0x9f {
				i++
				continue
			}
			b.WriteByte(s[i])
			i++
			continue
		}
		if r == '\r' && i+1 < len(s) && s[i+1] == '\n' {
			b.WriteByte('\r')
		} else if !drop(r) {
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}

func needsStrip(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 && c != '\n' && c != '\t' || c >= 0x7f {
			return true
		}
	}
	return false
}

// Writer returns w with Strip applied to everything written through it. A
// character cut in two by a write boundary is held back until its rest comes.
func Writer(w io.Writer) io.Writer { return &writer{w: w} }

type writer struct {
	w     io.Writer
	carry []byte
}

func (s *writer) Write(p []byte) (int, error) {
	data := append(s.carry, p...)
	s.carry = nil
	// Hold back an incomplete UTF-8 sequence at the end.
	end := len(data)
	for k := 1; k <= utf8.UTFMax-1 && k <= len(data); k++ {
		if data[len(data)-k] < 0x80 {
			break
		}
		if utf8.RuneStart(data[len(data)-k]) {
			if !utf8.FullRune(data[len(data)-k:]) {
				end = len(data) - k
			}
			break
		}
	}
	// A \r at the end may belong to a \r\n that is cut by the boundary.
	if end > 0 && data[end-1] == '\r' {
		end--
	}
	s.carry = append([]byte(nil), data[end:]...)
	out := Strip(string(data[:end]))
	if _, err := io.WriteString(s.w, out); err != nil {
		return 0, err
	}
	return len(p), nil
}
