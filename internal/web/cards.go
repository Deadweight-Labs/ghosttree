package web

import (
	"strings"
	"time"
	"unicode/utf8"
)

// cmdLine ist ein Pfad oder Befehl für die Mulde mit Kopieren (Vorlage gcmd).
// Icon ist der Schlüssel der Vorlage "icon".
type cmdLine struct {
	Icon, Text string
}

// leadSentence ist der erste Satz eines Textes, einzeilig, höchstens limit
// Zeichen. Er ist die Vorschau im Einklapper: Titel plus erster Satz fordern
// zum Aufklappen auf, ein "mehr anzeigen" braucht es nicht.
func leadSentence(s string, limit int) string {
	s = oneLineText(s)
	for i, r := range s {
		if (r == '.' || r == '!' || r == '?') && (i+1 == len(s) || s[i+1] == ' ') {
			s = s[:i+1]
			break
		}
	}
	if utf8.RuneCountInString(s) > limit {
		s = strings.TrimRight(string([]rune(s)[:limit]), " ,;:") + "…"
	}
	return s
}

// oldAfter ist das Alter, ab dem eine Karte ihr Alter in Warnfarbe zeigt.
const (
	taskOldAfter   = 3 * 24 * time.Hour
	reviewOldAfter = 7 * 24 * time.Hour
)

func olderThan(created string, now time.Time, limit time.Duration) bool {
	t := parseTime(created)
	return !t.IsZero() && now.Sub(t) >= limit
}

// commandLines teilt einen mehrzeiligen Prüfhinweis: je Zeile eine Mulde, nie
// als Fließtext.
func commandLines(icon, s string) []cmdLine {
	var out []cmdLine
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, cmdLine{Icon: icon, Text: line})
		}
	}
	return out
}
