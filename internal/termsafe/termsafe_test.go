package termsafe

import (
	"bytes"
	"testing"
)

func TestStripRemovesWhatSteersATerminal(t *testing.T) {
	cases := map[string]string{
		"plain text\nwith\ttab":       "plain text\nwith\ttab",
		"umlaute äöü ß und 日本語 und 👍": "umlaute äöü ß und 日本語 und 👍",
		"\x1b]0;owned\x07title":       "]0;ownedtitle",
		"\x1b[2J\x1b[Hgone":           "[2J[Hgone",
		"\x1b]52;c;ZXZpbA==\x1b\\":    "]52;c;ZXZpbA==\\",
		"a\rb":                        "ab",
		"a\r\nb":                      "a\r\nb",
		"c1 \u009b31m x":              "c1 31m x",
		"raw \x9b31m":                 "raw 31m",
		"del\x7fete":                  "delete",
		"bidi \u202eevil\u202c":       "bidi evil",
		"nul\x00byte":                 "nulbyte",
		"ends with a lone cr\r":       "ends with a lone cr",
	}
	for in, want := range cases {
		if got := Strip(in); got != want {
			t.Errorf("Strip(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriterStripsAcrossWriteBoundaries(t *testing.T) {
	var buf bytes.Buffer
	w := Writer(&buf)
	// "ä" (c3 a4) and the C1 control U+009B (c2 9b) are each cut between writes,
	// and so is a \r\n.
	for _, chunk := range [][]byte{[]byte("x\xc3"), []byte("\xa4y\xc2"), []byte("\x9bz\x1b[31m!\r"), []byte("\nlone\r"), []byte("x")} {
		if n, err := w.Write(chunk); err != nil || n != len(chunk) {
			t.Fatalf("%d %v", n, err)
		}
	}
	if got, want := buf.String(), "xäyz[31m!\r\nlonex"; got != want {
		t.Fatalf("%q, want %q", got, want)
	}
}
