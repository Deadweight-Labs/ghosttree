package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Die fünf Zustände einer Zustellung, nach Spec §A7. Sie sind absichtlich
// mehr als "gesendet/nicht gesendet": gespeichert heißt nicht abgeholt,
// abgeholt heißt nicht eingebracht, eingebracht heißt nicht gelesen, und
// gelesen heißt nicht berücksichtigt.
//
// Ein Adapter, der einen Schritt nicht belegen kann, lässt ihn aus. Der
// Zustand bleibt dann unknown und wird so gezeigt. Das ist der Kern der
// Zusage: Ghosttree behauptet keine Zustellung, die es nicht beobachtet hat.
const (
	DeliveryUnknown  = "unknown"
	DeliveryStored   = "stored"
	DeliveryFetched  = "fetched"
	DeliveryInjected = "injected"
	DeliveryAcked    = "acked"
)

// deliveryRank macht den Fortschritt vergleichbar. Verglichen wird die Zahl,
// nicht die Zeichenkette — deshalb kann ein verspätet gemeldeter früherer
// Schritt den Zustand nicht zurückziehen. Adapter melden nicht in
// garantierter Reihenfolge.
var deliveryRank = map[string]int{
	DeliveryUnknown:  0,
	DeliveryStored:   1,
	DeliveryFetched:  2,
	DeliveryInjected: 3,
	DeliveryAcked:    4,
}

// MarkCoordDelivery hält fest, wie weit eine Nachricht bei EINEM Empfänger
// gekommen ist. Je Empfänger, nicht je Nachricht: dass A gelesen hat, sagt
// nichts über B, und bei einem Raumbeitrag ist das der Normalfall.
func (s *Store) MarkCoordDelivery(messageID int64, recipient, state string) error {
	rank, ok := deliveryRank[state]
	if !ok {
		// Ein erfundener Zustand wird abgewiesen statt gespeichert. Sonst
		// steht irgendwann "sent" neben "stored" und niemand weiß mehr,
		// welches davon zugestellt heißt.
		return fmt.Errorf("unknown delivery state %q", state)
	}
	if s.writer != nil {
		return queueWrite(s, []any{messageID, recipient, state}, func(d *Store, p []any) error {
			return d.MarkCoordDelivery(p[0].(int64), p[1].(string), p[2].(string))
		})
	}
	_, err := s.db.Exec(`INSERT INTO coord_deliveries(message_id,recipient_external_id,state,rank,updated_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(message_id,recipient_external_id) DO UPDATE SET
			state=CASE WHEN excluded.rank > coord_deliveries.rank THEN excluded.state ELSE coord_deliveries.state END,
			rank=CASE WHEN excluded.rank > coord_deliveries.rank THEN excluded.rank ELSE coord_deliveries.rank END,
			updated_at=excluded.updated_at`,
		messageID, recipient, state, rank, now())
	return err
}

// ClaimCoordDelivery hebt eine Zustellung genau einmal auf injected und
// meldet, ob dieser Aufrufer gewonnen hat. Wer verliert, ändert nichts: das
// Update greift nur, solange der Rang unter injected liegt, und SQLite
// serialisiert die Schreiber. Ein bereits injected oder acked Eintrag bleibt
// unberührt, damit ein zweiter Kanal dieselbe Nachricht nicht noch einmal
// einbringt.
func (s *Store) ClaimCoordDelivery(messageID int64, recipient string) (bool, error) {
	if s.writer != nil {
		return queueValue(s, []any{messageID, recipient}, func(d *Store, p []any) (bool, error) {
			return d.ClaimCoordDelivery(p[0].(int64), p[1].(string))
		})
	}
	rank := deliveryRank[DeliveryInjected]
	res, err := s.db.Exec(`INSERT INTO coord_deliveries(message_id,recipient_external_id,state,rank,updated_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(message_id,recipient_external_id) DO UPDATE SET
			state=excluded.state, rank=excluded.rank, updated_at=excluded.updated_at
		WHERE coord_deliveries.rank < excluded.rank`,
		messageID, recipient, DeliveryInjected, rank, now())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// CoordInjectedMessages liefert, welche der Nachrichten für diesen Empfänger
// schon eingebracht (injected oder weiter) sind. Der Inbox-Abgleich blendet
// sie aus, damit das Modell sie nicht ein zweites Mal bekommt.
func (s *Store) CoordInjectedMessages(recipient string, messageIDs []int64) ([]int64, error) {
	if s.reader != nil {
		return s.reader.CoordInjectedMessages(recipient, messageIDs)
	}
	if len(messageIDs) == 0 {
		return nil, nil
	}
	args := []any{recipient, deliveryRank[DeliveryInjected]}
	for _, id := range messageIDs {
		args = append(args, id)
	}
	rows, err := s.db.Query(`SELECT message_id FROM coord_deliveries
		WHERE recipient_external_id=? AND rank>=? AND message_id IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(messageIDs)), ",")+`) ORDER BY message_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CoordDeliveryState beantwortet, was über diese Zustellung bekannt ist.
// Kein Datensatz ist keine Auskunft, sondern die Abwesenheit einer — genau
// dafür existiert DeliveryUnknown. Ein echter Datenbankfehler wird dagegen
// zurückgegeben und nicht als "unbekannt" getarnt.
func (s *Store) CoordDeliveryState(messageID int64, recipient string) (string, error) {
	if s.reader != nil {
		return s.reader.CoordDeliveryState(messageID, recipient)
	}
	var state string
	err := s.db.QueryRow(`SELECT state FROM coord_deliveries
		WHERE message_id=? AND recipient_external_id=?`, messageID, recipient).Scan(&state)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return DeliveryUnknown, nil
	case err != nil:
		return DeliveryUnknown, err
	}
	return state, nil
}

// SetCoordCursor merkt, bis wohin ein Empfänger an einem Ziel gelesen hat.
// Der Cursor läuft monoton: ein verspätetes älteres Ack darf ihn nicht
// zurückziehen, sonst bekommt der Empfänger nach jedem Reconnect dieselben
// Beiträge erneut. Spec §A7.
func (s *Store) SetCoordCursor(agentExternalID, destinationKind, destinationID string, lastMessageID int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{agentExternalID, destinationKind, destinationID, lastMessageID},
			func(d *Store, p []any) error {
				return d.SetCoordCursor(p[0].(string), p[1].(string), p[2].(string), p[3].(int64))
			})
	}
	_, err := s.db.Exec(`INSERT INTO coord_cursors(agent_external_id,destination_kind,destination_id,last_message_id,updated_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(agent_external_id,destination_kind,destination_id) DO UPDATE SET
			last_message_id=MAX(coord_cursors.last_message_id,excluded.last_message_id),
			updated_at=excluded.updated_at`,
		agentExternalID, destinationKind, destinationID, lastMessageID, now())
	return err
}

// CoordCursor liefert den Lesestand. Ein unbekannter Cursor ist 0 und damit
// "von vorn", kein Fehler: eine neue Session hat noch nichts gelesen, und das
// ist ein gültiger Zustand.
func (s *Store) CoordCursor(agentExternalID, destinationKind, destinationID string) (int64, error) {
	if s.reader != nil {
		return s.reader.CoordCursor(agentExternalID, destinationKind, destinationID)
	}
	var last int64
	err := s.db.QueryRow(`SELECT last_message_id FROM coord_cursors
		WHERE agent_external_id=? AND destination_kind=? AND destination_id=?`,
		agentExternalID, destinationKind, destinationID).Scan(&last)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, err
	}
	return last, nil
}
