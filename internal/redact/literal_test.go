package redact

import (
	"errors"
	"math/rand"
	"strings"
	"testing"
)

// Every value in these tests is invented.
const (
	litFull = "QDzt#fakeWRD%7kTest!"
	litA    = "fakeWRD"
	litB    = "7kTest"
	// a value with characters that an encoder writes as escape sequences
	litEsc = "Pa\"ss&w0rd<Fake>"
	litUni = "Grüße😀x/y"
)

// uesc writes a \u escape sequence the way an encoder that escapes the
// character writes it.
func uesc(hex string) string { return `\` + "u" + hex }

func mustLiterals(t testing.TB, values ...string) *Literals {
	t.Helper()
	l, err := NewLiterals(values)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLiteralsReplaceTheGivenAndTheLowerCaseSpelling(t *testing.T) {
	l := mustLiterals(t, litFull, litA, litB)
	in := "sed 's/" + litFull + "/x/' f; grep " + strings.ToLower(litFull) + " out; fakewrd; 7ktest; FAKEWRD; Passwort: " + litFull
	want := "sed 's/" + LiteralMarker + "/x/' f; grep " + LiteralMarker + " out; " + LiteralMarker + "; " + LiteralMarker + "; FAKEWRD; Passwort: " + LiteralMarker
	stats := Stats{}
	got := l.Apply(in, Text, stats)
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if stats["literal"] != 5 {
		t.Fatalf("stats = %v", stats)
	}
	if again := l.Apply(got, Text, nil); again != got {
		t.Fatalf("not idempotent: %q", again)
	}
}

func TestLiteralsPreferTheLongerValue(t *testing.T) {
	l := mustLiterals(t, "abcd1234", "abcd1234xy")
	got := l.Apply("one abcd1234xy two abcd1234 three", Text, nil)
	if want := "one " + LiteralMarker + " two " + LiteralMarker + " three"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLiteralsMatchEscapedFormsInJSON(t *testing.T) {
	l := mustLiterals(t, litEsc, litUni)
	text := "a " + litEsc + " b " + litUni + " c"
	for _, escapeHTML := range []bool{true, false} {
		line := jsonLine(t, text, escapeHTML)
		if escapeHTML && !strings.Contains(line, uesc("0026")) {
			t.Fatalf("the encoder did not escape the value: %s", line)
		}
		got := l.Apply(line, JSON, nil)
		if content, want := contentOf(t, got), "a "+LiteralMarker+" b "+LiteralMarker+" c"; content != want {
			t.Fatalf("escapeHTML=%v: content %q, want %q\n%s", escapeHTML, content, want, got)
		}
	}
	// Other encoders write the same characters in other ways.
	for name, content := range map[string]string{
		"ensure_ascii": `"pw Gr` + uesc("00fc") + uesc("00df") + `e` + uesc("d83d") + uesc("de00") + `x\/y ok"`,
		"upper hex":    `"pw Gr` + uesc("00FC") + uesc("00DF") + `e` + uesc("D83D") + uesc("DE00") + `x/y ok"`,
		"mixed":        `"pw Gr` + uesc("00fc") + `ße😀x` + uesc("002f") + `y ok"`,
		"all escaped":  `"pw ` + uesc("0047") + `r` + uesc("00fc") + `ße😀x/y ok"`,
	} {
		line := `{"type":"user","message":{"role":"user","content":` + content + `}}`
		got := l.Apply(line, JSON, nil)
		if c, want := contentOf(t, got), "pw "+LiteralMarker+" ok"; c != want {
			t.Errorf("%s: content %q, want %q\n%s", name, c, want, got)
		}
	}
}

func TestLiteralsLeaveNumbersAndTheStructureOfJSONAlone(t *testing.T) {
	l := mustLiterals(t, "12345", `a":"b`)
	line := `{"n":12345,"s":"a12345b","t":"a\":\"b","u":{"a":"b"}}`
	got := l.Apply(line, JSON, nil)
	want := `{"n":12345,"s":"a` + LiteralMarker + `b","t":"` + LiteralMarker + `","u":{"a":"b"}}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestLiteralsNeverStartInsideAnEscapeSequence(t *testing.T) {
	l := mustLiterals(t, "u003c")
	line := jsonLine(t, "x<y", true)
	if !strings.Contains(line, uesc("003c")) {
		t.Fatalf("the encoder did not escape: %s", line)
	}
	if got := l.Apply(line, JSON, nil); got != line {
		t.Fatalf("the value was found inside an escape sequence: %s", got)
	}
	// In plain text the same characters are an ordinary value.
	if got, want := l.Apply("x"+uesc("003c")+"y", Text, nil), `x\`+LiteralMarker+"y"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLiteralsContains(t *testing.T) {
	l := mustLiterals(t, litA)
	for text, want := range map[string]bool{
		"a " + litA + " b":              true,
		"a " + strings.ToLower(litA):    true,
		"a " + strings.ToUpper(litA):    false,
		"nothing":                       false,
		"":                              false,
		"fake WRD":                      false,
		"fake" + LiteralMarker + "WRD ": false,
	} {
		if got := l.Contains(text); got != want {
			t.Errorf("Contains(%q) = %v, want %v", text, got, want)
		}
	}
	var none *Literals
	if none.Contains(litA) || none.Len() != 0 || none.Apply(litA, Text, nil) != litA {
		t.Error("a nil list must leave everything alone")
	}
}

var literalPieces = []string{
	litFull, litA, litB, litEsc, litUni, "k7k7k7k7m2m2m2m2",
	strings.ToLower(litFull), strings.ToLower(litEsc), "FAKEWRD",
	" ", " ", "\n", "\r\n", "\t", "\"", `\`, `\\`, "<", ">", "&", "/", "ü", "😀", "x", "7", "abc", "[", "]", ":", "=", "'", "\x01",
	"Passwort: ", "pw=", "grep ", "sed 's/",
}

func randomLiteralText(rng *rand.Rand) string {
	var sb strings.Builder
	for n := 1 + rng.Intn(14); n > 0; n-- {
		sb.WriteString(literalPieces[rng.Intn(len(literalPieces))])
	}
	return sb.String()
}

// The same text, plain and as a JSON line, comes to the same result; the line
// stays valid JSON; a second pass changes nothing; and none of the values is
// left.
func TestLiteralsAgreeInTextAndJSONOnRandomInput(t *testing.T) {
	values := []string{litFull, litA, litB, litEsc, litUni, "k7k7k7k7m2m2m2m2"}
	l := mustLiterals(t, values...)
	rng := rand.New(rand.NewSource(526))
	changed := 0
	for i := 0; i < 20000; i++ {
		text := randomLiteralText(rng)
		want := l.Apply(text, Text, nil)
		if want != text {
			changed++
		}
		line := jsonLine(t, text, i%2 == 0)
		got := l.Apply(line, JSON, nil)
		if content := contentOf(t, got); content != want {
			t.Fatalf("JSON and text disagree for %q\n json %q\n text %q", text, content, want)
		}
		if again := l.Apply(got, JSON, nil); again != got {
			t.Fatalf("JSON not idempotent for %q", text)
		}
		if again := l.Apply(want, Text, nil); again != want {
			t.Fatalf("text not idempotent for %q", text)
		}
		for _, v := range values {
			for _, form := range []string{v, strings.ToLower(v)} {
				if strings.Contains(want, form) {
					t.Fatalf("%q is left in the result for %q: %q", form, text, want)
				}
			}
		}
	}
	if changed < 5000 {
		t.Fatalf("only %d of 20000 texts held a value: the test checks too little", changed)
	}
}

func TestParseLiteralsReadsTheListFormat(t *testing.T) {
	file := "\xef\xbb\xbf# known values\r\n\r\n  " + litFull + "  \r\n" + litA + "\n" + `\#hash-start` + "\n#a comment\n\t\n" + litA + "\n"
	l, err := ParseLiterals(strings.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	if l.Len() != 3 {
		t.Fatalf("Len = %d, want 3 (the duplicate counts once, the comment not at all)", l.Len())
	}
	got := l.Apply("x #hash-start y "+litFull+" #a comment", Text, nil)
	if want := "x " + LiteralMarker + " y " + LiteralMarker + " #a comment"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseLiteralsRefusesWhatWouldDoHarm(t *testing.T) {
	for name, c := range map[string]struct{ file, want string }{
		"short":                  {"fine-value-1\nZq7\n", "line 2"},
		"short, multibyte":       {"fine-value-1\näöü\n", "line 2"},
		"invalid UTF-8":          {"\n\nab\xff\xfe\xfd\xfc-xyz\n", "line 3"},
		"control":                {"fine-value-1\nab\x01cd-efg\n", "line 2"},
		"marker word":            {"fine-value-1\nliteral\n", "marker"},
		"marker start":           {"fine-value-1\n[REDACTED:foo] bar\n", "marker"},
		"marker piece":           {"fine-value-1\nredacted\n", "marker"},
		"empty":                  {"# nothing here\n\n   \n", "no value"},
		"too short once trimmed": {"   abc   \n", "line 1"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseLiterals(strings.NewReader(c.file))
			if !errors.Is(err, ErrLiterals) {
				t.Fatalf("err = %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not say %q", err, c.want)
			}
			for _, secret := range []string{"fine-value-1", "Zq7", "äöü", "cd-efg", "-xyz", "foo", "abc"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the error holds a value: %q", err)
				}
			}
		})
	}
	// Four characters are enough, whatever their size in bytes.
	if _, err := ParseLiterals(strings.NewReader("äöüß\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseLiterals(strings.NewReader(strings.Repeat("x", maxLiteralsFile+10))); !errors.Is(err, ErrLiterals) {
		t.Fatalf("a huge list was read: %v", err)
	}
}

func BenchmarkLiteralsApplyRaw(b *testing.B) {
	l := mustLiterals(b, litFull, litA, litB, litEsc)
	var sb strings.Builder
	for sb.Len() < 4000 {
		sb.WriteString("Das war eine längere Zeile mit \"Anführungszeichen\"\nund Umbrüchen, Tabs\t und Pfaden /usr/local/bin/x sowie Zahlen 12345. ")
	}
	line := jsonLineB(b, sb.String())
	b.SetBytes(int64(len(line)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if l.Apply(line, JSON, nil) != line {
			b.Fatal("changed")
		}
	}
}
