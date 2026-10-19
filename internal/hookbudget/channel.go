package hookbudget

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Kanäle des automatischen Kontexts. Sie haben getrennte Budgets, und das ist
// eine ausdrückliche Korrektur: der erste Entwurf ließ Koordinationshinweise
// in dasselbe Lebenszeit-Budget zählen wie den Gedächtnis-Bootstrap. v2 §7
// verwirft das mit einer Begründung, die man einmal gelesen nicht mehr
// wegdiskutiert — "nach ausreichend langer Arbeit verstummt die
// Kommunikation".
//
// Ein erschöpftes Gedächtnisbudget darf den Chat nicht für den Rest der
// Session abschalten, und umgekehrt darf lebhafte Koordination nicht das
// Wissen verdrängen, das eine Session zum Arbeiten braucht.
const (
	ChannelMemory = "memory"
	ChannelCoord  = "coord"
)

// CoordLimit und CoordWindow sind Startwerte für Tests, keine gemessenen
// Optimalwerte. v2 §7 schlägt als Größenordnung "etwa 4.000 Tokens pro
// Empfänger und fünf Minuten" vor und nennt das ausdrücklich eine
// konfigurierbare Hypothese. Gerechnet wird hier in Zeichen, weil der Rest
// dieses Pakets in Zeichen rechnet und eine erfundene Tokenumrechnung
// genauer aussähe, als sie ist.
const (
	CoordLimit  = 12000
	CoordWindow = 5 * time.Minute
)

// ChannelLimit nennt das Budget eines Kanals.
func ChannelLimit(channel string) int {
	if channel == ChannelCoord {
		return CoordLimit
	}
	return Limit
}

// ChannelWindow nennt das Fenster, nach dem sich ein Kanal erholt. Der
// Gedächtniskanal hat keins: sein Budget gilt für die Lebensdauer der
// Session, weil Bootstrap-Wissen einmal ausgeliefert wird und nicht in
// Schüben nachkommt.
//
// Koordination ist das Gegenteil. Sie läuft über Stunden, und ein
// Lebenszeitbudget hieße: ab Nachmittag kommt nichts mehr an. Deshalb ein
// rollendes Fenster — voll heißt hier "gerade jetzt zu viel", nicht "für
// heute vorbei".
func ChannelWindow(channel string) time.Duration {
	if channel == ChannelCoord {
		return CoordWindow
	}
	return 0
}

// coordNotice sagt, was wirklich passiert ist. Nicht "zugestellt", nicht
// "gelöscht": verzögert, mit dem Weg zum Rest. Spec §7 verlangt genau diese
// drei Unterscheidungen.
const coordNotice = "\n\n[ghosttree: coordination delivery is rate-limited right now. " +
	"Nothing was dropped — read the rest with `coord_inbox`.]\n"

// ChannelUsage liest den Kontostand eines Kanals. Ohne diesen Weg wäre die
// Trennung der Budgets behauptet statt belegt: man sähe nur, dass etwas
// durchkommt, nicht auf wessen Rechnung.
func ChannelUsage(sessionID, channel string) (Receipt, error) {
	path, err := channelPath(sessionID, channel)
	if err != nil {
		return Receipt{}, err
	}
	r, err := readReceipt(path, receiptKey(sessionID, channel))
	if errors.Is(err, os.ErrNotExist) {
		return Receipt{}, nil
	}
	return r, err
}

func receiptKey(sessionID, channel string) string {
	digest := sha256.Sum256([]byte(channel + "\x00" + sessionID))
	return hex.EncodeToString(digest[:])
}

func channelPath(sessionID, channel string) (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	name := receiptKey(sessionID, channel) + ".json"
	if channel == ChannelCoord {
		name = receiptKey(sessionID, channel) + ".coord.json"
	}
	return filepath.Join(dir, name), nil
}

// ageCoordWindow datiert das Fenster zurück. Nur für Tests: fünf Minuten
// echt zu warten wäre der Unterschied zwischen einem Test, den man laufen
// lässt, und einem, den man abschaltet.
func ageCoordWindow(_ string, sessionID string, by time.Duration) error {
	path, err := channelPath(sessionID, ChannelCoord)
	if err != nil {
		return err
	}
	r, err := readReceipt(path, receiptKey(sessionID, ChannelCoord))
	if err != nil {
		return err
	}
	r.StartedAt = r.StartedAt.Add(-by)
	return saveReceipt(path, r)
}
