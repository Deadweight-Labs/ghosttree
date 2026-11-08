package idea

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
)

// Bild-Anhänge an Idee, Antwort und Variante (REQ-611). Erlaubt sind PNG, JPEG
// und WebP. SVG ist abgelehnt: ein SVG ist ausführbares Markup, und „bereinigt“
// wäre eine Zusage, die sich kaum halten lässt; ein Rasterbild ist ein Rasterbild.
// Der Typ kommt aus den ersten Bytes, nie aus dem Dateinamen oder einer
// Angabe des Absenders.

// Maße.
const (
	MaxAttachmentBytes      = 5 << 20
	MaxAttachmentPerSubject = 40
	MaxPixels               = 40_000_000
	MaxSide                 = 16384
	MaxAlt                  = 200
)

// AttachmentMeta beschreibt einen Anhang ohne seine Bytes.
type AttachmentMeta struct {
	ID        int64  `json:"id"`
	Subject   string `json:"subject"`
	SubjectID int64  `json:"subject_id"`
	// Owner ist idea, reply oder variant; OwnerRef die Nummer der Variante,
	// bzw. der Diskussion bei einer Antwort.
	Owner     string `json:"owner"`
	OwnerRef  string `json:"owner_ref,omitempty"`
	Mime      string `json:"mime"`
	Bytes     int    `json:"bytes"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	SHA256    string `json:"sha256"`
	Alt       string `json:"alt,omitempty"`
	Person    string `json:"person,omitempty"`
	CreatedAt string `json:"created_at"`
	// URL ist der relative Pfad der Auslieferung.
	URL string `json:"url"`
}

// Eigentümer eines Anhangs.
const (
	OwnerIdea    = "idea"
	OwnerReply   = "reply"
	OwnerVariant = "variant"
)

// Image ist ein geprüftes Bild.
type Image struct {
	Mime   string
	Ext    string
	Width  int
	Height int
}

func attachRule(code, msg, res string) error { return ideaRule(code, msg, res) }

// CheckImage prüft Bytes als Bild und liefert Typ und Maße. declared ist ein
// vom Absender angegebener Typ (leer oder gleich dem erkannten, sonst abgelehnt).
func CheckImage(data []byte, declared string) (Image, error) {
	if len(data) == 0 {
		return Image{}, attachRule("attachment_empty", "the attachment is empty", "attach a PNG, JPEG or WebP image")
	}
	if len(data) > MaxAttachmentBytes {
		return Image{}, attachRule("attachment_too_large", fmt.Sprintf("the attachment is larger than %d bytes", MaxAttachmentBytes), "scale the image down")
	}
	var img Image
	var err error
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		img, err = checkRaster(data, "image/png", "png", func(b []byte) (image.Config, error) { return png.DecodeConfig(bytes.NewReader(b)) })
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		img, err = checkRaster(data, "image/jpeg", "jpg", func(b []byte) (image.Config, error) { return jpeg.DecodeConfig(bytes.NewReader(b)) })
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		img, err = checkWebP(data)
	default:
		return Image{}, attachRule("attachment_type", "only PNG, JPEG and WebP images can be attached (SVG is not accepted)", "convert the image to PNG, JPEG or WebP")
	}
	if err != nil {
		return Image{}, err
	}
	if d := strings.ToLower(strings.TrimSpace(strings.SplitN(declared, ";", 2)[0])); d != "" && d != "application/octet-stream" && d != img.Mime {
		return Image{}, attachRule("attachment_type_mismatch", fmt.Sprintf("the content is %s, not %s", img.Mime, d), "send the image with its real type")
	}
	return img, nil
}

func checkRaster(data []byte, mime, ext string, cfg func([]byte) (image.Config, error)) (Image, error) {
	c, err := cfg(data)
	if err != nil {
		return Image{}, attachRule("attachment_invalid", "the image cannot be read: "+err.Error(), "export it again")
	}
	return sized(Image{Mime: mime, Ext: ext, Width: c.Width, Height: c.Height})
}

func sized(i Image) (Image, error) {
	if i.Width <= 0 || i.Height <= 0 || i.Width > MaxSide || i.Height > MaxSide || int64(i.Width)*int64(i.Height) > MaxPixels {
		return Image{}, attachRule("attachment_dimensions", fmt.Sprintf("the image is %dx%d; at most %d pixels per side and %d in all", i.Width, i.Height, MaxSide, MaxPixels), "scale the image down")
	}
	return i, nil
}

// checkWebP liest Typ und Maße aus dem Kopf (VP8, VP8L oder VP8X) und prüft,
// dass die RIFF-Länge zu den Bytes passt.
func checkWebP(data []byte) (Image, error) {
	bad := func(why string) (Image, error) {
		return Image{}, attachRule("attachment_invalid", "the WebP image cannot be read: "+why, "export it again")
	}
	if int64(binary.LittleEndian.Uint32(data[4:8]))+8 > int64(len(data)) {
		return bad("truncated")
	}
	if len(data) < 30 {
		return bad("too short")
	}
	var w, h int
	switch string(data[12:16]) {
	case "VP8 ":
		if data[23] != 0x9d || data[24] != 0x01 || data[25] != 0x2a {
			return bad("no VP8 frame")
		}
		w, h = int(binary.LittleEndian.Uint16(data[26:28])&0x3fff), int(binary.LittleEndian.Uint16(data[28:30])&0x3fff)
	case "VP8L":
		if data[20] != 0x2f {
			return bad("no VP8L frame")
		}
		bits := binary.LittleEndian.Uint32(data[21:25])
		w, h = int(bits&0x3fff)+1, int((bits>>14)&0x3fff)+1
	case "VP8X":
		w = 1 + (int(data[24]) | int(data[25])<<8 | int(data[26])<<16)
		h = 1 + (int(data[27]) | int(data[28])<<8 | int(data[29])<<16)
	default:
		return bad("unknown chunk")
	}
	return sized(Image{Mime: "image/webp", Ext: "webp", Width: w, Height: h})
}
