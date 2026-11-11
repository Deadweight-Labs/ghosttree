// Package gitmsg bildet aus einer Commit-Nachricht die Schlüssel, mit denen ein
// Server eine Nachricht aus einem Transkript gegen die Nachricht eines Commits bei
// GitHub vergleicht, ohne eine der beiden zu speichern.
//
// Beide Seiten sehen dieselbe Nachricht in leicht anderer Form: das Transkript
// hat den Text des Befehls (git cleanup, Absätze aus mehreren -m), GitHub die
// Nachricht des Commits (Titel und Rumpf getrennt, Co-authored-by-Zeilen vom Server
// entfernt, Zeilenlänge und Gesamtlänge gekappt). Der Schlüssel macht daraus Buchstaben
// und Ziffern in Kleinschreibung und kappt sie früh: Leerraum, Satzzeichen,
// Steuerzeichen und die Kappung der Gegenseite ändern ihn nicht.
//
// Nur der Hash verlässt die Maschine, nie der Text (IDEA-31).
package gitmsg

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
)

const (
	// maxTitleRunes kappt die Titelzeile wie der Server (cleanLine, 200 Zeichen).
	maxTitleRunes = 200
	// maxTitleKey und maxFullKey kappen den normalisierten Text, bevor er gehasht wird.
	// Eine Gegenseite, die den Rumpf früher kappt (6000 Zeichen), hat bis dahin
	// weit mehr als maxFullKey Buchstaben.
	maxTitleKey = 160
	maxFullKey  = 3000
	// keyHexLen: so viele Hexzeichen eines SHA-256 bleiben (128 Bit).
	keyHexLen = 32
)

// Keys sind die beiden Schlüssel einer Nachricht. Title gilt der ersten Zeile; Full
// gilt Titel und Rumpf und ist leer, wenn die Nachricht nur aus dem Titel besteht
// (dann sagt Title schon alles). Beide sind leer, wenn die Nachricht nichts enthält.
type Keys struct {
	Title string
	Full  string
}

// Of liest die Schlüssel einer Nachricht. Zeilen "Co-authored-by:" zählen nicht mit
// (der Server entfernt sie aus dem Rumpf und speichert sie getrennt).
func Of(message string) Keys {
	title, body := Split(message)
	return OfParts(title, body)
}

// OfParts liest die Schlüssel aus Titel und Rumpf, wie GitHub sie liefert.
func OfParts(title, body string) Keys {
	t := normalize(truncRunes(title, maxTitleRunes), maxTitleKey)
	if t == "" {
		return Keys{}
	}
	k := Keys{Title: digest(t)}
	b := normalize(strippedTrailers(body), maxFullKey)
	if b != "" {
		k.Full = digest(normalize(truncRunes(title, maxTitleRunes), maxFullKey) + "|" + b)
	}
	return k
}

// Split trennt eine Nachricht in die erste nicht leere Zeile und den Rest.
func Split(message string) (title, body string) {
	message = strings.ReplaceAll(strings.ReplaceAll(message, "\r\n", "\n"), "\r", "\n")
	message = strings.Trim(message, "\n \t")
	if i := strings.IndexByte(message, '\n'); i >= 0 {
		return message[:i], message[i+1:]
	}
	return message, ""
}

// strippedTrailers lässt die Zeilen "Co-authored-by:" weg.
func strippedTrailers(body string) string {
	if !strings.Contains(strings.ToLower(body), "co-authored-by:") {
		return body
	}
	var keep []string
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if len(t) >= 15 && strings.EqualFold(t[:15], "co-authored-by:") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}

func truncRunes(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}

// normalize behält Buchstaben und Ziffern in Kleinschreibung, höchstens max.
func normalize(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
			if n++; n >= max {
				break
			}
		}
	}
	return b.String()
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:keyHexLen]
}

// Valid sagt, ob s die Form eines Schlüssels hat (so, wie ihn Of liefert).
func Valid(s string) bool {
	if len(s) != keyHexLen {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
