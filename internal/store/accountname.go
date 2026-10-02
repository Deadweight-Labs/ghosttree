package store

import (
	"errors"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// MaxDisplayNameRunes begrenzt Kontonamen in Anzeigen (Zeichen).
const MaxDisplayNameRunes = 64

// ErrAccountNameTaken: der Name kollidiert nach NFKC und Kleinschreibung mit
// einem vorhandenen Konto.
var ErrAccountNameTaken = errors.New("account name is already in use (names are compared ignoring case and look-alike forms)")

// isInvisibleName: Default_Ignorable-Zeichen (Cf, Other_Default_Ignorable,
// Variation Selectors) und die Hangul-Füller, die sichtbar leer sind.
func isInvisibleName(r rune) bool {
	switch r {
	case 0x115F, 0x1160, 0x3164, 0xFFA0:
		return true
	}
	return unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) ||
		unicode.Is(unicode.Variation_Selector, r)
}

// NormalizeAccountName macht einen Kontonamen anzeigesicher und vergleichbar:
// NFKC, unsichtbare Zeichen und Hangul-Füller raus, nur Buchstaben, Ziffern,
// Leerzeichen und . _ - ' bleiben (alles andere wird _), Kombinationszeichen
// (Mn/Mc) nur hinter einem Buchstaben, Leerraum zusammengezogen, höchstens
// MaxDisplayNameRunes Zeichen. Der Name ist Nutzereingabe: der Rest des
// Systems zeigt nur diese Form, und die Identität bleibt person:N.
func NormalizeAccountName(name string) string {
	var out []rune
	lastLetter := false
	for _, r := range norm.NFKC.String(name) {
		switch {
		case isInvisibleName(r):
			continue
		case unicode.IsLetter(r):
			out, lastLetter = append(out, r), true
		case unicode.In(r, unicode.Mn, unicode.Mc):
			if lastLetter {
				out = append(out, r)
			}
		case unicode.IsDigit(r):
			out, lastLetter = append(out, r), false
		case r == '.', r == '_', r == '-', r == '\'', r == ' ':
			out, lastLetter = append(out, r), false
		default:
			out, lastLetter = append(out, '_'), false
		}
	}
	s := strings.Join(strings.Fields(string(out)), " ")
	if r := []rune(s); len(r) > MaxDisplayNameRunes {
		s = strings.TrimSpace(string(r[:MaxDisplayNameRunes]))
	}
	return s
}

var nameFold = cases.Fold()

// accountNameKey ist die Vergleichsform: normalisiert, NFKC, casefold.
func accountNameKey(name string) string {
	return nameFold.String(norm.NFKC.String(NormalizeAccountName(name)))
}

// accountNameTakenTx sagt, ob ein vorhandenes Konto denselben Vergleichsschlüssel hat.
func accountNameTakenTx(q queryer, name string) (bool, error) {
	key := accountNameKey(name)
	rows, err := q.Query(`SELECT name FROM persons`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var existing string
		if err := rows.Scan(&existing); err != nil {
			return false, err
		}
		if accountNameKey(existing) == key {
			return true, nil
		}
	}
	return false, rows.Err()
}
