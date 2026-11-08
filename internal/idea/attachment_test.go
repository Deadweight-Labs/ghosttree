package idea

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

func pngBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegBytes(t testing.TB) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 3, 2)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// webpLossless baut den Kopf eines verlustfreien WebP mit den Maßen w x h.
func webpLossless(w, h int) []byte {
	payload := make([]byte, 10)
	payload[0] = 0x2f
	binary.LittleEndian.PutUint32(payload[1:5], uint32(w-1)|uint32(h-1)<<14)
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(4+8+len(payload)))
	b.WriteString("WEBPVP8L")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(payload)))
	b.Write(payload)
	return b.Bytes()
}

// webpExtended baut den Kopf eines WebP im erweiterten Format (24 Bit je Seite).
func webpExtended(w, h int) []byte {
	payload := make([]byte, 10)
	for i, v := range []int{w - 1, h - 1} {
		payload[4+3*i], payload[5+3*i], payload[6+3*i] = byte(v), byte(v>>8), byte(v>>16)
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(4+8+len(payload)))
	b.WriteString("WEBPVP8X")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(payload)))
	b.Write(payload)
	return b.Bytes()
}

func code(err error) string { return requestdomain.Code(err) }

func TestCheckImageAcceptsRasterTypesByTheirBytes(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		mime string
		w, h int
	}{
		{"png", pngBytes(t, 4, 3), "image/png", 4, 3},
		{"jpeg", jpegBytes(t), "image/jpeg", 3, 2},
		{"webp", webpLossless(640, 480), "image/webp", 640, 480},
	} {
		img, err := CheckImage(tc.data, "")
		if err != nil || img.Mime != tc.mime || img.Width != tc.w || img.Height != tc.h {
			t.Errorf("%s: %+v %v", tc.name, img, err)
		}
		// Der angegebene Typ muss stimmen, oder fehlen.
		if _, err := CheckImage(tc.data, tc.mime); err != nil {
			t.Errorf("%s with its own type: %v", tc.name, err)
		}
		if _, err := CheckImage(tc.data, "image/gif"); code(err) != "attachment_type_mismatch" {
			t.Errorf("%s declared as gif: %v", tc.name, err)
		}
	}
}

func TestCheckImageRefusesWhatIsNotARasterImage(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	html := []byte("<html><script>alert(1)</script></html>")
	// Ein PNG-Kopf mit Markup dahinter ist ein PNG mit Anhang, kein HTML; der
	// Typ kommt aus den Bytes, ausgeliefert wird mit nosniff.
	fakePNG := append([]byte("\x89PNG\r\n\x1a\n"), html...)
	for name, data := range map[string][]byte{
		"svg":                    svg,
		"html":                   html,
		"empty":                  nil,
		"gif":                    []byte("GIF89a\x01\x00\x01\x00"),
		"pdf":                    []byte("%PDF-1.7"),
		"png header with markup": fakePNG,
		"jpeg header only":       {0xFF, 0xD8, 0xFF, 0xE0},
		"webp too short":         []byte("RIFF\x04\x00\x00\x00WEBP"),
	} {
		if _, err := CheckImage(data, "image/png"); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := CheckImage(svg, ""); code(err) != "attachment_type" {
		t.Fatalf("svg: %v", err)
	}
}

func TestCheckImageLimitsSizeAndDimensions(t *testing.T) {
	big := append(pngBytes(t, 2, 2), bytes.Repeat([]byte{0}, MaxAttachmentBytes)...)
	if _, err := CheckImage(big, ""); code(err) != "attachment_too_large" {
		t.Fatalf("5 MB limit: %v", err)
	}
	if _, err := CheckImage(webpExtended(MaxSide+1, 10), ""); code(err) != "attachment_dimensions" {
		t.Fatalf("one side: %v", err)
	}
	if _, err := CheckImage(webpExtended(10000, 10000), ""); code(err) != "attachment_dimensions" {
		t.Fatalf("pixel budget: %v", err)
	}
	if _, err := CheckImage(webpExtended(MaxSide, 100), ""); err != nil {
		t.Fatalf("at the limit: %v", err)
	}
}

func TestOneLineRefusesControlAndFormatCharacters(t *testing.T) {
	for _, bad := range []string{"a\nb", "a\x00b", "a\u202eb", "a\u200bb", "a\u2028b", "a\ufeffb", strings.Repeat("x", 11)} {
		if err := OneLine(bad, 10); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if err := OneLine("Überlegung für Größe", 40); err != nil {
		t.Fatalf("umlauts: %v", err)
	}
}
