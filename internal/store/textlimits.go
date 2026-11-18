package store

import (
	"fmt"
	"strings"
	"unicode/utf8"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// Obergrenzen für Freitext. Sie sind großzügig gewählt: normale Arbeit stößt
// nie an, aber ein Konto kann den einzigen Schreiber nicht mit einem einzelnen
// Eintrag von mehreren MB aufhalten, den er auch noch in den Suchindex
// schreiben müsste.
const (
	// maxTitleRunes gilt für Titel, Namen und Etiketten.
	maxTitleRunes = 1000
	// maxNoteRunes gilt für Begründungen, Notizen, Herkunftsangaben und Verweise.
	maxNoteRunes = 8000
	// maxBodyBytes gilt für jeden anderen Text (Wissen, Beschreibungen,
	// Kommentare, Fassungen von Konzepten).
	maxBodyBytes = 1 << 20
)

// shortFields nennt die Felder, die eine Zeile sind, und ihre Grenze in
// Zeichen. Alles, was hier fehlt, ist ein langer Text (maxBodyBytes).
var shortFields = map[string]int{
	"title": maxTitleRunes, "name": maxTitleRunes, "label": maxTitleRunes,
	"reason": maxNoteRunes, "note": maxNoteRunes, "source": maxNoteRunes, "message": maxNoteRunes,
	"evidence_ref": maxNoteRunes, "external_ref": maxNoteRunes, "ref_id": maxNoteRunes,
	"trigger": maxNoteRunes, "supersede_reason": maxNoteRunes, "remaining": maxNoteRunes,
}

// checkTextLength lehnt einen Text über der Grenze seines Feldes ab. Der Name
// kann eine Listenstelle tragen (criteria[2]); es zählt das Feld davor.
func checkTextLength(field, text string) error {
	base, _, _ := strings.Cut(field, "[")
	if limit, short := shortFields[base]; short {
		if n := utf8.RuneCountInString(text); n > limit {
			return tooLong(field, fmt.Sprintf("%d characters", n), fmt.Sprintf("%d characters", limit))
		}
		return nil
	}
	if len(text) > maxBodyBytes {
		return tooLong(field, fmt.Sprintf("%d KiB", len(text)>>10), fmt.Sprintf("%d KiB", maxBodyBytes>>10))
	}
	return nil
}

func tooLong(field, got, max string) error {
	hint := "Shorten the text, or split it into several entries, and send it again."
	return requestdomain.NewRuleError("text_too_long",
		fmt.Sprintf("refused, nothing was stored: %s is %s, at most %s. %s", field, got, max, hint), hint, nil)
}
