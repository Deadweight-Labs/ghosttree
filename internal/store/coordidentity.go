package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
)

// MaxWorkNoteRunes begrenzt "woran ich arbeite".
const MaxWorkNoteRunes = 200

// BroadcastActiveWindow ist das Fenster, in dem ein Agent für @agents als aktiv
// gilt: Sichtung durch einen Hook der Session selbst.
const BroadcastActiveWindow = 15 * time.Minute

// BroadcastWindow ist der Mindestabstand zwischen zwei Broadcasts (@agents) eines
// Senders.
const BroadcastWindow = 5 * time.Minute

// ErrBroadcastLimited: der Sender hat gerade erst an alle Agenten gesendet.
var ErrBroadcastLimited = errors.New("coordination broadcast limited")

// BroadcastLimitedError nennt die Wartezeit.
type BroadcastLimitedError struct{ Wait time.Duration }

func (e *BroadcastLimitedError) Error() string {
	return fmt.Sprintf("%v: @agents was used a moment ago; wait %d minutes", ErrBroadcastLimited, int(e.Wait.Minutes())+1)
}

func (e *BroadcastLimitedError) Is(target error) bool { return target == ErrBroadcastLimited }

// BodyHasBroadcast sagt, ob ein Text das Broadcast-Wort enthält.
func BodyHasBroadcast(body string) bool {
	for _, t := range textMentionTokens(body) {
		if IsBroadcastToken(t) {
			return true
		}
	}
	return false
}

// checkBroadcastRateTx lässt höchstens einen Broadcast je Sender und
// BroadcastWindow zu. Gezählt werden die gespeicherten Nachrichten des Senders,
// deren Text das Broadcast-Wort enthält.
func checkBroadcastRateTx(tx *sql.Tx, sender string) error {
	cutoff := time.Now().UTC().Add(-BroadcastWindow)
	rows, err := tx.Query(`SELECT body, created_at FROM coord_messages WHERE sender_external_id=? AND created_at>=? ORDER BY id DESC LIMIT 50`,
		sender, cutoff.Format(time.RFC3339))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var body, created string
		if err := rows.Scan(&body, &created); err != nil {
			return err
		}
		if !BodyHasBroadcast(body) {
			continue
		}
		at, perr := time.Parse(time.RFC3339, created)
		if perr != nil {
			at = time.Now().UTC()
		}
		return &BroadcastLimitedError{Wait: at.Add(BroadcastWindow).Sub(time.Now().UTC())}
	}
	return rows.Err()
}

// ErrWorkNoteNotOwner: die Notiz gehört zu einer Session eines anderen Kontos.
var ErrWorkNoteNotOwner = errors.New("coordination agent belongs to another principal")

// CleanMachine macht aus einer selbstgemeldeten Maschine einen kurzen Namen aus
// Buchstaben, Ziffern und . _ -, klein geschrieben. Alles andere fällt weg, so
// kann der Name weder eine Kopfzeile brechen noch eine Mention-Kennung sprengen.
func CleanMachine(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		}
		if b.Len() >= 64 {
			break
		}
	}
	return strings.Trim(b.String(), ".-_")
}

// AgentLabel ist der Anzeigename eines Agenten: Person · Maschine · Branch.
// Der Request-Titel gehört bewusst nicht dazu: er ist rollenabhängig sichtbar
// und darf weder Name noch Mention-Kennung bestimmen. Leere Teile fallen weg;
// sind alle leer, ist es "".
func AgentLabel(person, machine, work string) string {
	var parts []string
	for _, p := range []string{person, machine, work} {
		if p = SafeLabel(p); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}

// SafeLabel macht aus einem selbstgemeldeten Namensteil (Branch, Maschine,
// Name) einen Text, der in einer einzeiligen Kopfzeile stehen darf: Buchstaben,
// Ziffern, Leerzeichen und . _ - / : # @ + bleiben, alles andere (Klammern,
// Komma, Gleichheitszeichen, Steuerzeichen) wird zu einem Leerzeichen. So kann
// ein Branch keine Rolle oder Autorität in die Zeile schreiben.
func SafeLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), strings.ContainsRune("._-/:#@+\u00b7", r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if utf8.RuneCountInString(out) > 120 {
		out = string([]rune(out)[:120])
	}
	return out
}

// neutralizeNote entfernt, was einen Attributwert oder ein Channel-Tag
// brechen könnte: spitze Klammern, Anführungszeichen, Backtick und Backslash.
func neutralizeNote(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '<', '>', '"', '\'', '`', '\\':
			return ' '
		}
		return r
	}, s)
}

// WorkNoteMaxAge ist das Alter, ab dem eine Arbeitsnotiz nicht mehr angezeigt
// wird: eine Session, die sie nicht löscht, soll nicht tagelang behaupten,
// woran sie arbeitet.
const WorkNoteMaxAge = 24 * time.Hour

// WorkNoteFresh sagt, ob eine zum Zeitpunkt at gesetzte Notiz zu ref noch
// gezeigt werden darf. Ein leerer oder unlesbarer Zeitpunkt zählt als alt.
func WorkNoteFresh(at string, ref time.Time) bool {
	t, err := time.Parse(time.RFC3339, at)
	return err == nil && ref.Sub(t) <= WorkNoteMaxAge
}

// SafeNote entschärft eine Notiz für die Ausgabe in Kopfzeilen und
// Attributwerten, auch wenn sie ein älterer Client gespeichert hat.
func SafeNote(note string) string {
	return strings.Join(strings.Fields(neutralizeNote(note)), " ")
}

// NormalizeWorkNote prüft und glättet die Arbeitsnotiz: Zeilenumbrüche und
// Steuerzeichen werden zu Leerzeichen, höchstens 200 Zeichen, und nichts, was
// nach einem Geheimnis aussieht (derselbe Filter wie bei Aufgaben für
// Menschen). Leer bedeutet löschen.
func NormalizeWorkNote(note string) (string, error) {
	var b strings.Builder
	for _, r := range note {
		if unicode.IsControl(r) || unicode.IsSpace(r) || r == ' ' || r == ' ' {
			r = ' '
		}
		b.WriteRune(r)
	}
	out := strings.Join(strings.Fields(neutralizeNote(b.String())), " ")
	if n := utf8.RuneCountInString(out); n > MaxWorkNoteRunes {
		return "", fmt.Errorf("%w: work note has %d characters, at most %d", ErrInvalidInput, n, MaxWorkNoteRunes)
	}
	if out == "" {
		return "", nil
	}
	if rule := humantask.CheckText("work note", out); rule != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidInput, rule.Message)
	}
	return out, nil
}

// SetCoordWorkNote setzt oder löscht (leere Notiz) die Selbstauskunft einer
// Session. Nur das Konto, dem die Session gehört, darf das.
func (s *Store) SetCoordWorkNote(externalID, principalID, note string) (string, error) {
	if s.writer != nil {
		return queueValue(s, []any{externalID, principalID, note}, func(d *Store, p []any) (string, error) {
			return d.SetCoordWorkNote(p[0].(string), p[1].(string), p[2].(string))
		})
	}
	clean, err := NormalizeWorkNote(note)
	if err != nil {
		return "", err
	}
	var owner string
	err = s.db.QueryRow(`SELECT COALESCE(principal_id,'') FROM coord_agents WHERE external_id=?`, externalID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrCoordForbidden
	}
	if err != nil {
		return "", err
	}
	if owner == "" || owner != principalID {
		return "", ErrWorkNoteNotOwner
	}
	at := ""
	if clean != "" {
		at = now()
	}
	if _, err := s.db.Exec(`UPDATE coord_agents SET work_note=?, work_note_at=? WHERE external_id=?`, clean, at, externalID); err != nil {
		return "", err
	}
	return clean, nil
}

// IsBroadcastToken sagt, ob ein normalisierter @-Name alle aktiven Agenten
// meint. Das Wort gehört dem Store und wird nie einem Teilnehmer als Name
// gegeben.
func IsBroadcastToken(token string) bool { return token == "agents" || token == "agenten" }

// broadcastIDsTx sind die Kandidaten, die Agenten sind und deren Session in den
// letzten BroadcastActiveWindow einen Hook ausgelöst hat.
func broadcastIDsTx(tx *sql.Tx, candidates []mentionCandidate) ([]string, error) {
	cutoff := time.Now().UTC().Add(-BroadcastActiveWindow)
	var out []string
	for _, c := range candidates {
		if strings.HasPrefix(c.id, "person:") {
			continue
		}
		var hook string
		err := tx.QueryRow(`SELECT last_hook_at FROM coord_agents WHERE external_id=?`, strings.TrimPrefix(c.id, "agent:")).Scan(&hook)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if at, perr := time.Parse(time.RFC3339, hook); perr == nil && !at.Before(cutoff) {
			out = append(out, c.id)
		}
	}
	return out, nil
}

// fillAgentSendersTx setzt bei Nachrichten von Agenten den lesbaren Namen und
// die Arbeitsnotiz des Absenders, live gelesen. Wie der Kontoname eines
// Menschen fehlt beides in der Gastsicht: wer die Mitglieder nicht sieht,
// erfährt auch ihre Rechner nicht.
func fillAgentSendersTx(tx *sql.Tx, a CoordAccess, kind, id string, msgs []CoordMessage) error {
	type who struct{ label, note string }
	cache := map[string]who{}
	guest, guestKnown := false, false
	for i := range msgs {
		m := &msgs[i]
		if m.AuthorKind == AuthorHuman || m.AuthorKind == AuthorSystem || m.SenderExternalID == "" {
			continue
		}
		if !guestKnown {
			guest, guestKnown = a.guestViewForMessageTx(tx, kind, id), true
		}
		if guest {
			return nil
		}
		w, ok := cache[m.SenderExternalID]
		if !ok {
			var machine, branch, note, noteAt, principal string
			err := tx.QueryRow(`SELECT machine, COALESCE(branch,''), work_note, work_note_at, COALESCE(principal_id,'') FROM coord_agents WHERE external_id=?`,
				m.SenderExternalID).Scan(&machine, &branch, &note, &noteAt, &principal)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil && !WorkNoteFresh(noteAt, time.Now().UTC()) {
				note = ""
			}
			if err == nil {
				w = who{label: AgentLabel(NormalizeAccountName(personNameTx(tx, principal)), machine, branch), note: note}
			}
			cache[m.SenderExternalID] = w
		}
		m.SenderLabel, m.SenderWorkNote = w.label, w.note
		if at, perr := time.Parse(time.RFC3339, m.CreatedAt); perr == nil && time.Since(at) > WorkNoteMaxAge {
			// Die Notiz ist der Stand von heute; neben einer alten Nachricht
			// würde sie etwas anderes behaupten, als damals galt.
			m.SenderWorkNote = ""
		}
	}
	return nil
}
