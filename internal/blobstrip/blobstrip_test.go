package blobstrip

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

// blob liefert n zufällige, aber reproduzierbare Bytes und deren base64.
func blob(n int, seed int64) (raw []byte, b64 string) {
	raw = make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(raw)
	return raw, base64.StdEncoding.EncodeToString(raw)
}

func want(raw []byte, mime, kind string) string {
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("[ghosttree: %s entfernt, %s, %d Bytes, sha256:%s]", kind, mime, len(raw), hex.EncodeToString(sum[:8]))
}

func mustValid(t *testing.T, s string) {
	t.Helper()
	if !json.Valid([]byte(s)) {
		t.Fatalf("result is not valid JSON: %.300s", s)
	}
}

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("invalid JSON: %v\n%.300s", err, s)
	}
	return v
}

func sameString(a, b string) bool {
	return len(a) == len(b) && unsafe.StringData(a) == unsafe.StringData(b)
}

func TestStripClaudeImageInToolResult(t *testing.T) {
	raw, b64 := blob(300_000, 1)
	line := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"shot"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]}]},"uuid":"u1"}`
	out, saved := Strip(line)
	mustValid(t, out)
	if saved != len(line)-len(out) || saved < 390_000 {
		t.Fatalf("saved = %d, line %d -> %d", saved, len(line), len(out))
	}
	exp := strings.Replace(line, b64, want(raw, "image/png", "Bild"), 1)
	if out != exp {
		t.Fatalf("got %.400s\nwant %.400s", out, exp)
	}
	if strings.Contains(out, b64[:64]) {
		t.Fatal("payload still in the line")
	}
}

func TestStripKeepsEverythingButThePayloadByteForByte(t *testing.T) {
	raw, b64 := blob(5000, 2)
	// Leerraum, Schlüsselreihenfolge, Maskierungen und Zahlen bleiben stehen.
	line := "{ \"z\" : 1.50e2 ,\n \"text\":\"a \\\"quoted\\\" \\u00e4 \\\\\" ,\"source\" : {\"data\":\"" + b64 + "\" , \"media_type\":\"image/jpeg\",\"type\":\"base64\"},\"n\":null}\n"
	out, saved := Strip(line)
	exp := strings.Replace(line, b64, want(raw, "image/jpeg", "Bild"), 1)
	if out != exp || saved != len(line)-len(out) || saved == 0 {
		t.Fatalf("got %q\nwant %q", out, exp)
	}
}

func TestStripDataURL(t *testing.T) {
	raw, b64 := blob(40_000, 3)
	line := `{"timestamp":"t","type":"response_item","payload":{"type":"custom_tool_call_output","output":[{"type":"input_image","image_url":"data:image/png;base64,` + b64 + `","detail":"high"}]}}`
	out, saved := Strip(line)
	mustValid(t, out)
	exp := strings.Replace(line, "data:image/png;base64,"+b64, want(raw, "image/png", "Bild"), 1)
	if out != exp || saved != len(line)-len(out) {
		t.Fatalf("got %.300s\nwant %.300s", out, exp)
	}
}

func TestStripDataURLWithParametersAndOtherKinds(t *testing.T) {
	raw, b64 := blob(3000, 4)
	for _, c := range []struct{ header, mime, kind string }{
		{"data:image/jpeg;charset=utf-8;base64,", "image/jpeg", "Bild"},
		{"data:audio/wav;base64,", "audio/wav", "Audio"},
		{"data:video/mp4;base64,", "video/mp4", "Video"},
		{"data:application/pdf;base64,", "application/pdf", "PDF"},
		{"data:application/zip;base64,", "application/zip", "Datei"},
		{"data:;base64,", "application/octet-stream", "Datei"},
	} {
		line := `{"u":"` + c.header + b64 + `"}`
		out, _ := Strip(line)
		if exp := `{"u":"` + want(raw, c.mime, c.kind) + `"}`; out != exp {
			t.Errorf("%s: got %.200s\nwant %.200s", c.header, out, exp)
		}
	}
}

func TestStripSeveralPayloadsInOneLine(t *testing.T) {
	r1, a := blob(20_000, 5)
	r2, b := blob(30_000, 6)
	r3, c := blob(10_000, 7)
	line := `{"message":{"content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + a + `"}},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"` + b + `"}}]},` +
		`"toolUseResult":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + a + `"}}],` +
		`"extra":{"u":"data:image/gif;base64,` + c + `"}}`
	out, saved := Strip(line)
	mustValid(t, out)
	for _, p := range []string{a, b, c} {
		if strings.Contains(out, p[:80]) {
			t.Fatal("a payload survived")
		}
	}
	if !strings.Contains(out, want(r1, "image/png", "Bild")) || !strings.Contains(out, want(r2, "image/jpeg", "Bild")) || !strings.Contains(out, want(r3, "image/gif", "Bild")) {
		t.Fatalf("placeholders missing: %.600s", out)
	}
	if strings.Count(out, "[ghosttree: ") != 4 || saved != len(line)-len(out) {
		t.Fatalf("count %d, saved %d vs %d", strings.Count(out, "[ghosttree: "), saved, len(line)-len(out))
	}
}

func TestStripClaudeCodeReadFile(t *testing.T) {
	raw, b64 := blob(9000, 8)
	line := `{"toolUseResult":{"type":"image","file":{"base64":"` + b64 + `","type":"image/jpeg","originalSize":72381,"dimensions":{"originalWidth":600}}}}`
	out, _ := Strip(line)
	exp := strings.Replace(line, b64, want(raw, "image/jpeg", "Bild"), 1)
	if out != exp {
		t.Fatalf("got %.300s", out)
	}
}

func TestStripMCPImageAndBlobResource(t *testing.T) {
	raw, b64 := blob(9000, 9)
	line := `{"result":{"content":[{"type":"image","data":"` + b64 + `","mimeType":"image/png"},` +
		`{"type":"resource","resource":{"uri":"file:///a.pdf","blob":"` + b64 + `","mimeType":"application/pdf"}}]}}`
	out, _ := Strip(line)
	mustValid(t, out)
	if !strings.Contains(out, want(raw, "image/png", "Bild")) || !strings.Contains(out, want(raw, "application/pdf", "PDF")) {
		t.Fatalf("got %.400s", out)
	}
}

func TestStripMediaTypeAfterThePayload(t *testing.T) {
	raw, b64 := blob(2000, 10)
	line := `{"data":"` + b64 + `","mimeType":"image/webp"}`
	if out, _ := Strip(line); out != `{"data":"`+want(raw, "image/webp", "Bild")+`","mimeType":"image/webp"}` {
		t.Fatalf("got %.200s", out)
	}
}

func TestStripBase64Variants(t *testing.T) {
	raw := make([]byte, 3001) // 3001 % 3 == 1: Auffüllung mit ==
	rand.New(rand.NewSource(11)).Read(raw)
	std := base64.StdEncoding.EncodeToString(raw)
	for name, payload := range map[string]string{
		"std":       std,
		"unpadded":  strings.TrimRight(std, "="),
		"url":       base64.URLEncoding.EncodeToString(raw),
		"url raw":   base64.RawURLEncoding.EncodeToString(raw),
		"wrapped":   strings.Join(chunk(std, 76), `\n`),
		"slash esc": strings.ReplaceAll(std, "/", `\/`),
	} {
		line := `{"source":{"type":"base64","media_type":"image/png","data":"` + payload + `"}}`
		out, saved := Strip(line)
		if saved == 0 || !strings.Contains(out, want(raw, "image/png", "Bild")) {
			t.Errorf("%s: not replaced: %.200s", name, out)
		}
		mustValid(t, out)
	}
}

func chunk(s string, n int) []string {
	var out []string
	for len(s) > n {
		out, s = append(out, s[:n]), s[n:]
	}
	return append(out, s)
}

func TestStripLeavesWhatIsNotAMedium(t *testing.T) {
	_, b64 := blob(5000, 12)
	text := strings.Repeat("this is plain prose, not base64 at all. ", 100)
	for name, line := range map[string]string{
		"data without media type":   `{"data":"` + b64 + `","x":1}`,
		"signature":                 `{"type":"thinking","signature":"` + b64 + `","base64":"mentioned"}`,
		"payload is prose":          `{"source":{"type":"base64","media_type":"text/plain","data":"` + text + `"}}`,
		"text source":               `{"source":{"type":"text","media_type":"text/plain","data":"` + text + `"}}`,
		"base64 key without mime":   `{"base64":"` + b64 + `"}`,
		"data url inside text":      `{"text":"see data:image/png;base64,` + b64 + ` for the image"}`,
		"data url as key":           `{"data:image/png;base64,` + b64 + `":1}`,
		"data url without base64":   `{"u":"data:text/plain,` + strings.Repeat("a", 2000) + `"}`,
		"data url, payload is text": `{"u":"data:image/png;base64,` + text + `"}`,
		"blob without mime":         `{"blob":"` + b64 + `"}`,
		"mime but no payload key":   `{"mimeType":"image/png","other":"` + b64 + `"}`,
	} {
		out, saved := Strip(line)
		if saved != 0 || !sameString(out, line) {
			t.Errorf("%s: changed: %.200s", name, out)
		}
	}
}

func TestStripBelowTheThresholdStaysAsItIs(t *testing.T) {
	_, small := blob(MinBytes, 13)
	small = small[:MinBytes-4] // gültiges base64, knapp unter der Grenze
	line := `{"source":{"type":"base64","media_type":"image/png","data":"` + small + `"}}` + strings.Repeat(" ", MinBytes)
	if out, saved := Strip(line); saved != 0 || !sameString(out, line) {
		t.Fatalf("small payload was replaced: saved %d", saved)
	}
}

func TestStripLineWithoutPayloadIsNotReserialized(t *testing.T) {
	big := strings.Repeat("plain output line\n", 500)
	for name, line := range map[string]string{
		"no needle":      "{ \"a\" : \"" + strings.ReplaceAll(big, "\n", `\n`) + "\" ,\"b\":[1,2 , 3]}",
		"needle in text": "{ \"a\" : \"" + strings.ReplaceAll(big, "\n", `\n`) + " base64 \" ,\"b\":[1,2 , 3]}",
		"short":          `{"type":"base64"}`,
		"empty":          "",
		"not an object":  "[" + strings.Repeat(`"x",`, 500) + `"base64"]`,
	} {
		out, saved := Strip(line)
		if saved != 0 || !sameString(out, line) {
			t.Errorf("%s: changed", name)
		}
	}
}

func TestStripInvalidJSONComesBackUnchanged(t *testing.T) {
	_, b64 := blob(5000, 14)
	good := `{"source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}`
	for name, line := range map[string]string{
		"truncated":      good[:len(good)-3],
		"trailing comma": strings.Replace(good, `"}}`, `",}}`, 1),
		"two values":     good + good,
		"plain text":     "base64 " + strings.Repeat("x", 3000),
		"unterminated":   `{"a":"` + b64,
	} {
		out, saved := Strip(line)
		if saved != 0 || !sameString(out, line) {
			t.Errorf("%s: changed", name)
		}
	}
}

func TestStripIsIdempotent(t *testing.T) {
	_, a := blob(50_000, 15)
	_, b := blob(8_000, 16)
	for _, line := range []string{
		`{"message":{"content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + a + `"}}]}}`,
		`{"payload":{"output":[{"type":"input_image","image_url":"data:image/png;base64,` + a + `"}]}}`,
		`{"result":{"content":[{"type":"image","data":"` + b + `","mimeType":"image/png"}]},"toolUseResult":{"file":{"base64":"` + a + `","type":"image/png"}}}`,
	} {
		once, saved := Strip(line)
		if saved == 0 {
			t.Fatalf("nothing replaced in %.200s", line)
		}
		twice, saved2 := Strip(once)
		if saved2 != 0 || twice != once {
			t.Fatalf("second pass changed the line (saved %d)", saved2)
		}
	}
}

func TestStripPlaceholderIsSafeEvenForAHostileMediaType(t *testing.T) {
	_, b64 := blob(3000, 17)
	for _, mime := range []string{`image/png","x":"y`, `a\\"b/c`, "image/ä", "no-slash", "a/b/c", strings.Repeat("a", 200) + "/b", "IMAGE/PNG; charset=x", ""} {
		enc, _ := json.Marshal(mime)
		line := `{"source":{"type":"base64","media_type":` + string(enc) + `,"data":"` + b64 + `"}}`
		out, saved := Strip(line)
		mustValid(t, out)
		if saved == 0 {
			t.Fatalf("%q: not replaced", mime)
		}
		var v struct {
			Source struct {
				Data string `json:"data"`
			} `json:"source"`
		}
		if err := json.Unmarshal([]byte(out), &v); err != nil || !strings.HasPrefix(v.Source.Data, Prefix) {
			t.Fatalf("%q: %v %q", mime, err, v.Source.Data)
		}
	}
	out, _ := Strip(`{"source":{"type":"base64","media_type":"IMAGE/PNG; charset=x","data":"` + b64 + `"}}`)
	if !strings.Contains(out, "Bild entfernt, image/png, ") {
		t.Errorf("media type not normalised: %.200s", out)
	}
}

func TestStripPlaceholderShape(t *testing.T) {
	raw, b64 := blob(1234, 18)
	out, _ := Strip(`{"source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}`)
	sum := sha256.Sum256(raw)
	exp := `{"source":{"type":"base64","media_type":"image/png","data":"[ghosttree: Bild entfernt, image/png, 1234 Bytes, sha256:` + hex.EncodeToString(sum[:8]) + `]"}}`
	if out != exp {
		t.Fatalf("got  %s\nwant %s", out, exp)
	}
}

func TestStripNestedStringsWithQuotesAndBackslashesBeforeThePayload(t *testing.T) {
	raw, b64 := blob(4000, 19)
	line := `{"a":"x\\","b":"\\\"","c":"\"}{","d":{"e":"\\\\\"base64\\"},"source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}`
	out, _ := Strip(line)
	if out != strings.Replace(line, b64, want(raw, "image/png", "Bild"), 1) {
		t.Fatalf("got %.300s", out)
	}
}

// Zufällige Dokumente: nach Strip sind sie, bis auf die Nutzlasten, dieselben.
func TestStripRandomDocumentsKeepEverythingElse(t *testing.T) {
	rng := rand.New(rand.NewSource(20))
	nasty := []string{`"`, `\`, `\"`, `"}`, `base64`, `data:image/png;base64,AAAA`, "ä <>&", "\n\t", ""}
	var randomString func() string
	randomString = func() string {
		var b strings.Builder
		for i := rng.Intn(4); i >= 0; i-- {
			b.WriteString(nasty[rng.Intn(len(nasty))])
			b.WriteString(fmt.Sprint(rng.Intn(100)))
		}
		return b.String()
	}
	var gen func(depth int, media *int) (doc, expected any)
	gen = func(depth int, media *int) (any, any) {
		switch k := rng.Intn(8); {
		case depth > 4 || k == 0:
			return randomString(), nil
		case k == 1:
			return float64(rng.Intn(1000)) / 8, nil
		case k <= 3:
			n := rng.Intn(4)
			d, e := make([]any, n), make([]any, n)
			for i := range d {
				d[i], e[i] = gen(depth+1, media)
				if e[i] == nil {
					e[i] = d[i]
				}
			}
			return d, e
		case k == 4:
			raw, b64 := blob(MinBytes+rng.Intn(3000), int64(rng.Intn(1<<30)))
			*media++
			return map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": b64}},
				map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": want(raw, "image/png", "Bild")}}
		case k == 5:
			raw, b64 := blob(MinBytes+rng.Intn(3000), int64(rng.Intn(1<<30)))
			*media++
			return "data:image/jpeg;base64," + b64, want(raw, "image/jpeg", "Bild")
		default:
			n := rng.Intn(5)
			d, e := map[string]any{}, map[string]any{}
			for i := 0; i < n; i++ {
				key := randomString() + fmt.Sprint(i)
				d[key], e[key] = gen(depth+1, media)
				if e[key] == nil {
					e[key] = d[key]
				}
			}
			return d, e
		}
	}
	replaced := 0
	for i := 0; i < 300; i++ {
		var media int
		doc, exp := gen(0, &media)
		if exp == nil {
			exp = doc
		}
		raw, err := json.Marshal(map[string]any{"pad": strings.Repeat("p", MinBytes), "doc": doc})
		if err != nil {
			t.Fatal(err)
		}
		out, saved := Strip(string(raw))
		mustValid(t, out)
		if media == 0 {
			if saved != 0 || out != string(raw) {
				t.Fatalf("doc %d without media changed", i)
			}
			continue
		}
		replaced += media
		var got any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		if want := (map[string]any{"pad": strings.Repeat("p", MinBytes), "doc": exp}); !reflect.DeepEqual(got, decode(t, mustMarshal(t, want))) {
			t.Fatalf("doc %d differs after Strip:\n%.500s", i, out)
		}
		if saved != len(raw)-len(out) {
			t.Fatalf("doc %d: saved %d, real %d", i, saved, len(raw)-len(out))
		}
	}
	if replaced < 100 {
		t.Fatalf("only %d payloads generated", replaced)
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func FuzzStrip(f *testing.F) {
	_, b64 := blob(2000, 21)
	for _, s := range []string{
		`{"source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}`,
		`{"u":"data:image/png;base64,` + b64 + `"}`,
		`{"data":"` + b64 + `","mimeType":"image/png"}`,
		`{"a":"\\","base64":"` + b64 + `","type":"image/png"}`,
		`[{"blob":"` + b64 + `","mimeType":"a/b"}]`,
		`{"text":"base64 ` + strings.Repeat("x", 2000) + `"}`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		out, saved := Strip(line)
		if saved < 0 || saved != len(line)-len(out) {
			t.Fatalf("saved %d for %d -> %d bytes", saved, len(line), len(out))
		}
		if saved == 0 {
			if out != line {
				t.Fatal("unchanged line differs")
			}
			return
		}
		if !json.Valid([]byte(line)) || !json.Valid([]byte(out)) {
			t.Fatal("a changed line must come from valid JSON and stay valid JSON")
		}
		again, saved2 := Strip(out)
		if saved2 != 0 || again != out {
			t.Fatal("not idempotent")
		}
	})
}

func benchLine(n int) string {
	_, b64 := blob(n, 22)
	return `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]}]}}`
}

func BenchmarkStripImage3MB(b *testing.B) {
	line := benchLine(3 << 20)
	b.SetBytes(int64(len(line)))
	for i := 0; i < b.N; i++ {
		if _, saved := Strip(line); saved == 0 {
			b.Fatal("not replaced")
		}
	}
}

func BenchmarkStripImage100KB(b *testing.B) {
	line := benchLine(100 << 10)
	b.SetBytes(int64(len(line)))
	for i := 0; i < b.N; i++ {
		Strip(line)
	}
}

func BenchmarkStripTextMentioningBase64(b *testing.B) {
	line := `{"type":"user","message":{"content":"` + strings.Repeat("some output line with base64 in it\\n", 30_000) + `"}}`
	b.SetBytes(int64(len(line)))
	for i := 0; i < b.N; i++ {
		Strip(line)
	}
}

func BenchmarkStripTextWithoutNeedle(b *testing.B) {
	line := `{"type":"user","message":{"content":"` + strings.Repeat("some output line, nothing special\\n", 30_000) + `"}}`
	b.SetBytes(int64(len(line)))
	for i := 0; i < b.N; i++ {
		Strip(line)
	}
}
