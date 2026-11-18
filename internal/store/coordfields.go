package store

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/termsafe"
)

// Grenzen für die Selbstauskunft eines Agenten bei der Anmeldung. Diese Felder
// stehen in Listen, die andere Agenten lesen (coord peers, Posteingang); ein
// Konto, das sich anmeldet, darf dort keinen Satz unterbringen, der wie eine
// Anweisung aussieht.
const (
	maxProviderLen     = 32
	maxAgentBranchLen  = 200
	maxAgentPathRunes  = 512
	maxCapabilitiesLen = 256
	maxRoomKeyLen      = 200
)

// ValidProvider sagt, ob ein Harness-Name zulässig ist: kurz, Buchstaben,
// Ziffern und . _ -, am Anfang kein Zeichen davon. Leer heißt "nicht angegeben".
func ValidProvider(s string) bool {
	if s == "" {
		return true
	}
	if len(s) > maxProviderLen {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case i > 0 && (r == '.' || r == '_' || r == '-'):
		default:
			return false
		}
	}
	return true
}

// ValidGitRef sagt, ob s ein Branch-Name nach den Regeln von git
// check-ref-format ist (ohne Leerraum, ~ ^ : ? * [ \, "..", "@{", ".lock" am
// Ende einer Komponente, führenden Punkt je Komponente) und zusätzlich
// einzeilig, druckbar und höchstens 200 Byte lang. Leer ist gültig: ein
// losgelöster Kopf hat keinen Branch.
func ValidGitRef(s string) bool {
	if s == "" {
		return true
	}
	if len(s) > maxAgentBranchLen || !utf8.ValidString(s) || s == "@" {
		return false
	}
	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".") {
		return false
	}
	if strings.Contains(s, "..") || strings.Contains(s, "@{") || strings.Contains(s, "//") {
		return false
	}
	for _, r := range s {
		switch {
		case r == ' ', r == '~', r == '^', r == ':', r == '?', r == '*', r == '[', r == '\\', r == 0x7f:
			return false
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.IsSpace(r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r):
			return false
		}
	}
	for _, part := range strings.Split(s, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// validOneLine: einzeilig, druckbar, höchstens max Zeichen.
func validOneLine(s string, max int) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) || r == utf8.RuneError {
			return false
		}
	}
	return true
}

// ValidOneLine sagt, ob s einzeilig und druckbar ist und höchstens max Zeichen
// hat (leer ist gültig).
func ValidOneLine(s string, max int) bool { return validOneLine(s, max) }

// validCapabilities: eine Liste kurzer Namen (Buchstaben, Ziffern, . _ - : /),
// durch Komma oder Leerzeichen getrennt.
func validCapabilities(s string) bool {
	if len(s) > maxCapabilitiesLen {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-', r == ':', r == '/', r == ',', r == ' ':
		default:
			return false
		}
	}
	return true
}

// SafeLine macht aus Fremdtext eine einzelne Zeile ohne Steuer- und
// Formatzeichen und ohne Zeilenumbruch, höchstens max Zeichen. Für Felder, die
// ein Altbestand roh gespeichert haben kann.
func SafeLine(s string, max int) string { return cleanLine(termsafe.Strip(s), max) }

// normalizeForRegistration prüft die Selbstauskunft einer Anmeldung und glättet
// den Anzeigenamen. Was eine feste Form hat (Harness, Branch, Eltern-ID,
// Pfade, Fähigkeiten, Raumschlüssel), wird bei einer Abweichung abgelehnt und
// nicht umgeschrieben: ein umgeschriebener Wert wäre ein anderer, als der
// Agent meldet.
func (a *CoordAgent) normalizeForRegistration() error {
	if !ValidProvider(a.Provider) {
		return fmt.Errorf("%w: provider must be a short name of letters, digits, . _ - (at most %d characters)", ErrInvalidInput, maxProviderLen)
	}
	if !ValidGitRef(a.Branch) {
		return fmt.Errorf("%w: branch is not a valid git branch name", ErrInvalidInput)
	}
	if a.ParentExternalID != "" && !ValidExternalID(a.ParentExternalID) {
		return fmt.Errorf("%w: parent_external_id has invalid characters", ErrInvalidInput)
	}
	if !validOneLine(a.Cwd, maxAgentPathRunes) || !validOneLine(a.Worktree, maxAgentPathRunes) {
		return fmt.Errorf("%w: cwd and worktree must be one printable line of at most %d characters", ErrInvalidInput, maxAgentPathRunes)
	}
	if !validCapabilities(a.Capabilities) {
		return fmt.Errorf("%w: capabilities must be a short list of names", ErrInvalidInput)
	}
	if len(a.RoomKey) > maxRoomKeyLen || !validOneLine(a.RoomKey, maxRoomKeyLen) {
		return fmt.Errorf("%w: room_key must be one printable line of at most %d bytes", ErrInvalidInput, maxRoomKeyLen)
	}
	a.DisplayName = SafeLabel(a.DisplayName)
	return nil
}

// forPeers ist die Form eines Agenten, wie andere ihn lesen: jedes Feld der
// Selbstauskunft, das nicht die erlaubte Form hat (Altbestand, ein älterer
// Server), fällt weg oder wird auf eine Zeile gekürzt. Es schadet nicht, wenn
// der Wert schon sauber ist.
func (a CoordAgent) forPeers() CoordAgent {
	if !ValidProvider(a.Provider) {
		a.Provider = "unknown"
	}
	if !ValidGitRef(a.Branch) {
		a.Branch = ""
	}
	if !ValidExternalID(a.ParentExternalID) {
		a.ParentExternalID = ""
	}
	if !validOneLine(a.Cwd, maxAgentPathRunes) {
		a.Cwd = ""
	}
	if !validOneLine(a.Worktree, maxAgentPathRunes) {
		a.Worktree = ""
	}
	if !validCapabilities(a.Capabilities) {
		a.Capabilities = ""
	}
	a.DisplayName = SafeLabel(a.DisplayName)
	a.Machine = CleanMachine(a.Machine)
	a.WorkNote = SafeNote(SafeLine(a.WorkNote, MaxWorkNoteRunes))
	return a
}

// SafeForPeers wendet forPeers an; Clients rufen es vor dem Drucken auf, damit
// auch die Antwort eines älteren oder feindlichen Servers nichts anrichtet.
func SafeForPeers(a CoordAgent) CoordAgent { return a.forPeers() }
