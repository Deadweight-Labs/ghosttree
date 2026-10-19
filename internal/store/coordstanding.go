package store

import (
	"fmt"
	"strconv"
	"strings"
)

// IntentStanding markiert eine Nachricht als gezielte fachliche Vorgabe statt
// als Mitteilung. Spec §A4 verlangt die Unterscheidung: "Der lokale Postgres
// ist wieder da" ist Information, "für diesen Release keine Breaking Changes"
// ist eine neue Einschränkung für bestimmte Beteiligte.
const IntentStanding = "standing"

// StandingInstruction ist eine menschliche Vorgabe, die weiter gilt.
//
// Der Grund für einen eigenen Datensatz statt einer besonders wichtigen
// Nachricht steht in Spec §A4 und §11: eine weiter geltende Einschränkung
// darf nicht durch Chat-Retention oder eine Zusammenfassung unbemerkt
// verschwinden. Eine Nachricht rutscht nach fünfzig weiteren aus dem
// Blickfeld; eine Vorgabe soll das nicht. Sie endet mit ihrer Aufgabe oder
// durch eine ausdrückliche Geste — nicht dadurch, dass genug Verkehr darüber
// hinweggelaufen ist.
type StandingInstruction struct {
	RoomKey   string   `json:"room_key"`
	MessageID string   `json:"message_id"`
	Person    string   `json:"person"`
	Body      string   `json:"body"`
	Targets   []string `json:"targets,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
	EndedAt   string   `json:"ended_at,omitempty"`
	EndedBy   string   `json:"ended_by,omitempty"`
}

func (s *Store) PutStandingInstruction(in StandingInstruction) error {
	if in.RoomKey == "" || strings.TrimSpace(in.Body) == "" {
		return fmt.Errorf("a standing instruction needs a room and a body")
	}
	if in.Person == "" {
		// Eine Vorgabe ohne benannten Menschen wäre eine Regel ohne Urheber.
		// Genau das soll dieser Typ verhindern.
		return fmt.Errorf("a standing instruction needs the person who gave it")
	}
	if s.writer != nil {
		return queueWrite(s, []any{in}, func(d *Store, p []any) error {
			return d.PutStandingInstruction(p[0].(StandingInstruction))
		})
	}
	_, err := s.db.Exec(`INSERT INTO coord_standing(room_key,message_id,person,body,targets,created_at)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(room_key,message_id) DO UPDATE SET body=excluded.body, targets=excluded.targets`,
		in.RoomKey, in.MessageID, in.Person, in.Body, strings.Join(in.Targets, " "), now())
	return err
}

// StandingInstructions liefert die Vorgaben, die in diesem Raum noch gelten.
// Beendete bleiben in der Tabelle und werden hier nicht mitgeliefert: was
// einmal galt, ist Geschichte und keine Anweisung.
func (s *Store) StandingInstructions(roomKey string) ([]StandingInstruction, error) {
	if s.reader != nil {
		return s.reader.StandingInstructions(roomKey)
	}
	rows, err := s.db.Query(`SELECT room_key,message_id,person,body,targets,created_at
		FROM coord_standing WHERE room_key=? AND ended_at IS NULL
		ORDER BY created_at`, roomKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StandingInstruction
	for rows.Next() {
		var in StandingInstruction
		var targets string
		if err := rows.Scan(&in.RoomKey, &in.MessageID, &in.Person, &in.Body,
			&targets, &in.CreatedAt); err != nil {
			return nil, err
		}
		if targets != "" {
			in.Targets = strings.Fields(targets)
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// EndStandingInstruction beendet eine Vorgabe ausdrücklich und hält fest, wer
// das war. Beendet heißt nicht gelöscht: der Datensatz bleibt, damit später
// nachvollziehbar ist, was wie lange galt.
func (s *Store) EndStandingInstruction(roomKey, messageID, person string) error {
	if person == "" {
		return fmt.Errorf("ending an instruction needs the person who ended it")
	}
	if s.writer != nil {
		return queueWrite(s, []any{roomKey, messageID, person}, func(d *Store, p []any) error {
			return d.EndStandingInstruction(p[0].(string), p[1].(string), p[2].(string))
		})
	}
	res, err := s.db.Exec(`UPDATE coord_standing SET ended_at=?, ended_by=?
		WHERE room_key=? AND message_id=? AND ended_at IS NULL`,
		now(), person, roomKey, messageID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no active standing instruction %s in %s", messageID, roomKey)
	}
	return nil
}

// FormatMessageID macht aus der numerischen Nachrichten-ID den Schlüssel, den
// eine Vorgabe trägt. Als Text, weil eine Vorgabe später auch an etwas
// anderem als einer Nachricht hängen könnte.
func FormatMessageID(id int64) string { return strconv.FormatInt(id, 10) }
