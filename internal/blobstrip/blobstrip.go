// Package blobstrip ersetzt base64-Nutzlasten in einer JSONL-Zeile eines
// Agenten-Transkripts durch einen kurzen Platzhalter.
//
// Screenshots und Dateianhänge stehen in den Rohzeilen als base64 und machen
// den größten Teil der Datenbank aus, werden aber weder durchsucht noch
// angezeigt (internal/transcript liest nur Text). Strip lässt die Zeile gültiges
// JSON mit derselben Struktur: nur der Wert der Nutzlast wird ersetzt, alle
// anderen Bytes bleiben stehen, auch Schlüsselreihenfolge und Leerraum.
package blobstrip

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
)

// MinBytes ist die Länge, ab der eine Nutzlast ersetzt wird. Darunter spart der
// Platzhalter nichts, und eine ersetzte Zeile bliebe beim zweiten Durchlauf
// nicht erkennbar unverändert.
const MinBytes = 1024

// Prefix beginnt jeden Platzhalter.
const Prefix = "[ghosttree: "

// Needles sind die Wörter, von denen eines in jeder Zeile stehen muss, die eine
// ersetzbare Nutzlast trägt. Wer Zeilen vorab filtern will (etwa in SQL), darf
// alle anderen auslassen.
var Needles = []string{"base64", "mimeType", "mime_type", "media_type"}

// Strip ersetzt jede base64-Nutzlast ab MinBytes in line durch einen
// Platzhalter und liefert die neue Zeile und die eingesparten Bytes.
//
// Erkannt werden
//   - Claude: ein Objekt {"type":"base64","media_type":…,"data":…} (Bild,
//     Dokument, Audio unter source, auch verschachtelt in tool_result,
//     message.content und toolUseResult),
//   - Claude Code Read: {"type":"image/png","base64":…,…} unter toolUseResult.file,
//   - MCP: {"type":"image","data":…,"mimeType":…} und blob-Ressourcen,
//   - jeder JSON-String der Form data:<mime>;base64,<daten> (Codex input_image).
//
// Eine Nutzlast zählt nur, wenn sie vollständig als base64 lesbar ist. Eine
// Zeile ohne Nutzlast, mit ungültigem JSON oder ohne einen der Needles kommt
// unverändert zurück (derselbe String, nicht neu serialisiert), mit 0.
//
// Nicht erfasst ist base64, das als Text in einem String steht: JSON im JSON
// (ein MCP-Ergebnis als Zeichenkette) oder der Inhalt einer .b64-Datei. Bei
// Stichproben echter Transkripte sind das unter zwei Prozent der Bilddaten.
func Strip(line string) (string, int) {
	if len(line) < MinBytes || !mayCarryPayload(line) {
		return line, 0
	}
	if !json.Valid([]byte(line)) {
		return line, 0
	}
	sc := scanner{src: line}
	if sc.value(0) < 0 || len(sc.edits) == 0 {
		return line, 0
	}
	sort.Slice(sc.edits, func(i, j int) bool { return sc.edits[i].start < sc.edits[j].start })
	var b strings.Builder
	b.Grow(len(line) - sc.saved)
	at := 0
	for _, e := range sc.edits {
		b.WriteString(line[at:e.start])
		b.WriteString(e.repl)
		at = e.end
	}
	b.WriteString(line[at:])
	return b.String(), sc.saved
}

func mayCarryPayload(line string) bool {
	for _, n := range Needles {
		if strings.Contains(line, n) {
			return true
		}
	}
	return false
}

type edit struct {
	start, end int // das Zeichenkettenliteral samt Anführungszeichen
	repl       string
}

type scanner struct {
	src   string
	edits []edit
	saved int
}

// value liest den Wert ab i und liefert das Ende dahinter, bei einem Fehler -1.
// Die Eingabe ist schon als JSON geprüft; die -1 sind nur Absicherung.
func (sc *scanner) value(i int) int {
	i = sc.skipSpace(i)
	if i >= len(sc.src) {
		return -1
	}
	switch sc.src[i] {
	case '"':
		end := sc.stringEnd(i)
		if end > 0 {
			sc.dataURL(i, end)
		}
		return end
	case '{':
		return sc.object(i)
	case '[':
		return sc.array(i)
	}
	for ; i < len(sc.src); i++ {
		switch sc.src[i] {
		case ',', ']', '}', ' ', '\t', '\r', '\n':
			return i
		}
	}
	return i
}

func (sc *scanner) skipSpace(i int) int {
	for i < len(sc.src) {
		switch sc.src[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}

// stringEnd liefert das Ende des Strings, der bei i mit " beginnt. Es springt
// von Anführungszeichen zu Anführungszeichen und zählt davor die Backslashes,
// damit ein langer Text nicht Byte für Byte gelesen wird.
func (sc *scanner) stringEnd(i int) int {
	from := i + 1
	for {
		k := strings.IndexByte(sc.src[from:], '"')
		if k < 0 {
			return -1
		}
		q := from + k
		slashes := 0
		for p := q - 1; p > i && sc.src[p] == '\\'; p-- {
			slashes++
		}
		if slashes%2 == 0 {
			return q + 1
		}
		from = q + 1
	}
}

func (sc *scanner) array(i int) int {
	i = sc.skipSpace(i + 1)
	if i < len(sc.src) && sc.src[i] == ']' {
		return i + 1
	}
	for {
		i = sc.value(i)
		if i < 0 {
			return -1
		}
		i = sc.skipSpace(i)
		if i >= len(sc.src) {
			return -1
		}
		switch sc.src[i] {
		case ',':
			i++
		case ']':
			return i + 1
		default:
			return -1
		}
	}
}

// member ist ein Zeichenkettenwert, der für die Entscheidung über das Objekt
// zählt.
type member struct {
	key        string
	start, end int
}

// object liest ein Objekt. Welche Mitglieder Nutzlast sind, steht erst am Ende
// fest, weil "type" und der Medientyp auch hinter den Daten stehen können.
func (sc *scanner) object(i int) int {
	var typ, mime string
	var payloads []member
	i = sc.skipSpace(i + 1)
	if i < len(sc.src) && sc.src[i] == '}' {
		return i + 1
	}
	for {
		i = sc.skipSpace(i)
		if i >= len(sc.src) || sc.src[i] != '"' {
			return -1
		}
		keyEnd := sc.stringEnd(i)
		if keyEnd < 0 {
			return -1
		}
		key := sc.src[i+1 : keyEnd-1]
		j := sc.skipSpace(keyEnd)
		if j >= len(sc.src) || sc.src[j] != ':' {
			return -1
		}
		start := sc.skipSpace(j + 1)
		end := sc.value(start)
		if end < 0 {
			return -1
		}
		if start < len(sc.src) && sc.src[start] == '"' {
			switch sc.name(key) {
			case "type":
				typ = sc.short(start, end)
			case "mimeType", "media_type", "mime_type":
				if v := sc.short(start, end); mime == "" && strings.Contains(v, "/") {
					mime = v
				}
			case "data", "base64", "blob":
				if end-start-2 >= MinBytes {
					payloads = append(payloads, member{sc.name(key), start, end})
				}
			}
		}
		i = sc.skipSpace(end)
		if i >= len(sc.src) {
			return -1
		}
		switch sc.src[i] {
		case ',':
			i++
		case '}':
			for _, p := range payloads {
				sc.objectPayload(p, typ, mime)
			}
			return i + 1
		default:
			return -1
		}
	}
}

// name löst Escapes in einem Schlüssel auf; die Namen, auf die es ankommt,
// stehen praktisch immer unmaskiert da.
func (sc *scanner) name(key string) string {
	if strings.IndexByte(key, '\\') < 0 {
		return key
	}
	var s string
	if json.Unmarshal([]byte(`"`+key+`"`), &s) != nil {
		return ""
	}
	return s
}

// short liefert den Wert eines kurzen Strings ohne Anführungszeichen, bei
// einem langen oder ungültigen "".
func (sc *scanner) short(start, end int) string {
	lit := sc.src[start:end]
	if len(lit) > 256 {
		return ""
	}
	if strings.IndexByte(lit, '\\') < 0 {
		return lit[1 : len(lit)-1]
	}
	var s string
	if json.Unmarshal([]byte(lit), &s) != nil {
		return ""
	}
	return s
}

// objectPayload ersetzt data, base64 oder blob, wenn das Objekt sich als
// Medium ausweist: "type":"base64" oder ein Medientyp daneben. Ein Feld "data"
// in einem beliebigen anderen Objekt bleibt stehen.
func (sc *scanner) objectPayload(p member, typ, mime string) {
	switch p.key {
	case "data":
		if typ != "base64" && mime == "" {
			return
		}
	case "base64":
		if mime == "" && strings.Contains(typ, "/") {
			mime = typ
		}
		if mime == "" {
			return
		}
	case "blob":
		if mime == "" {
			return
		}
	}
	payload, ok := sc.literal(p.start, p.end)
	if !ok {
		return
	}
	sc.replace(p.start, p.end, mime, payload)
}

// dataURL ersetzt einen String, der ganz aus data:<mime>;base64,<daten> besteht.
func (sc *scanner) dataURL(start, end int) {
	if end-start-2 < MinBytes || !strings.HasPrefix(sc.src[start:], `"data:`) {
		return
	}
	text, ok := sc.literal(start, end)
	if !ok {
		return
	}
	comma := strings.IndexByte(text, ',')
	if comma < 0 || comma > 256 {
		return
	}
	header := text[len("data:"):comma]
	if !strings.HasSuffix(header, ";base64") {
		return
	}
	mime, _, _ := strings.Cut(header, ";")
	sc.replace(start, end, mime, text[comma+1:])
}

// literal liefert den Inhalt des Strings src[start:end] ohne Anführungszeichen,
// mit aufgelösten Escapes.
func (sc *scanner) literal(start, end int) (string, bool) {
	lit := sc.src[start:end]
	if strings.IndexByte(lit, '\\') < 0 {
		return lit[1 : len(lit)-1], true
	}
	var s string
	if json.Unmarshal([]byte(lit), &s) != nil {
		return "", false
	}
	return s, true
}

func (sc *scanner) replace(start, end int, mime, payload string) {
	if len(payload) < MinBytes {
		return
	}
	size, sum, ok := digest(payload)
	if !ok {
		return
	}
	repl := `"` + placeholder(mime, size, sum) + `"`
	if len(repl) >= end-start {
		return
	}
	sc.edits = append(sc.edits, edit{start, end, repl})
	sc.saved += end - start - len(repl)
}

// digest liest payload als base64 und liefert die Größe der Daten und die
// ersten 16 Hex-Stellen ihres SHA-256, also den Anfang von sha256sum der
// Originaldatei. Mit Auffüllung oder ohne, normal oder URL-sicher.
func digest(payload string) (int, string, bool) {
	padded := strings.HasSuffix(payload, "=")
	urlSafe := strings.ContainsAny(payload[:min(len(payload), 4096)], "-_")
	encodings := [...]*base64.Encoding{
		pick(padded, urlSafe), pick(padded, !urlSafe), pick(!padded, urlSafe), pick(!padded, !urlSafe),
	}
	for _, enc := range encodings {
		h := sha256.New()
		n, err := io.Copy(h, base64.NewDecoder(enc, strings.NewReader(payload)))
		if err == nil {
			return int(n), hex.EncodeToString(h.Sum(nil)[:8]), true
		}
	}
	return 0, "", false
}

func pick(padded, urlSafe bool) *base64.Encoding {
	switch {
	case padded && urlSafe:
		return base64.URLEncoding
	case padded:
		return base64.StdEncoding
	case urlSafe:
		return base64.RawURLEncoding
	}
	return base64.RawStdEncoding
}

// placeholder baut den Ersatztext. Er enthält nur Zeichen, die in einem
// JSON-String nicht maskiert werden müssen.
func placeholder(mime string, size int, sum string) string {
	mime = cleanMime(mime)
	kind := "Datei"
	switch {
	case strings.HasPrefix(mime, "image/"):
		kind = "Bild"
	case strings.HasPrefix(mime, "audio/"):
		kind = "Audio"
	case strings.HasPrefix(mime, "video/"):
		kind = "Video"
	case mime == "application/pdf":
		kind = "PDF"
	}
	return Prefix + kind + " entfernt, " + mime + ", " + strconv.Itoa(size) + " Bytes, sha256:" + sum + "]"
}

// cleanMime kürzt einen Medientyp auf type/subtype in Kleinschreibung. Er
// stammt aus dem Transkript und gelangt unmaskiert in die Zeile.
func cleanMime(m string) string {
	const unknown = "application/octet-stream"
	m, _, _ = strings.Cut(m, ";")
	m = strings.ToLower(strings.TrimSpace(m))
	if len(m) == 0 || len(m) > 100 || strings.Count(m, "/") != 1 {
		return unknown
	}
	for i := 0; i < len(m); i++ {
		c := m[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '/', c == '.', c == '+', c == '-', c == '_':
		default:
			return unknown
		}
	}
	return m
}
