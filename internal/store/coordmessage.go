package store

import (
	"database/sql"
	"errors"
)

// CoordMessage ist ein dauerhafter Beitrag in einem Raum. ClientID kommt vom
// Absender und macht das Senden wiederholbar: derselbe Versuch nach einem
// Timeout ist dieselbe Nachricht, nicht die zweite. Bei einem Agenten heißt
// eine doppelt gelesene Bitte zweimal handeln, deshalb ist das keine Kosmetik.
type CoordMessage struct {
	ID               int64      `json:"id,omitempty"`
	RoomKey          string     `json:"room_key"`
	SenderExternalID string     `json:"sender_external_id"`
	SenderKind       string     `json:"sender_kind"`
	ClientID         string     `json:"client_id"`
	Kind             string     `json:"kind,omitempty"`
	Body             string     `json:"body"`
	ReplyTo          int64      `json:"reply_to,omitempty"`
	CreatedAt        string     `json:"created_at,omitempty"`
	Refs             []CoordRef `json:"refs,omitempty"`
}

// CoordRef verbindet eine Nachricht mit einem bestehenden Ghosttree-Objekt.
// Ohne das ist eine Abstimmung nur Text; damit ist sie der Weg zurück zu der
// Entscheidung, dem Auftrag oder dem Beleg, um den es ging. Das ist der
// Unterschied zwischen "das Auth-Ding ist fertig" und einer Nachricht, die in
// sechs Monaten noch etwas wert ist.
type CoordRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// AppendCoordMessage speichert einen Beitrag, bevor irgendjemand seinen
// Empfang bestätigt. Ein Wiederholungsversuch mit derselben ClientID liefert
// die vorhandene ID zurück und lässt den gespeicherten Inhalt unangetastet:
// eine Nachricht ändert sich nicht, weil jemand sie erneut sendet.
func (s *Store) AppendCoordMessage(m CoordMessage) (int64, error) {
	if s.writer != nil {
		return queueValue(s, []any{m}, func(d *Store, p []any) (int64, error) {
			return d.AppendCoordMessage(p[0].(CoordMessage))
		})
	}
	at := m.CreatedAt
	if at == "" {
		at = now()
	}
	kind := m.Kind
	if kind == "" {
		kind = "message"
	}
	senderKind := m.SenderKind
	if senderKind == "" {
		senderKind = "agent"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// Der Wiederholungsfall zuerst. Kein UPDATE: wer denselben Schlüssel mit
	// anderem Inhalt sendet, bekommt den alten Inhalt zurück, statt ihn dem
	// Empfänger unter der Hand auszutauschen.
	var existing int64
	err = tx.QueryRow(`SELECT id FROM coord_messages WHERE sender_external_id=? AND client_id=?`,
		m.SenderExternalID, m.ClientID).Scan(&existing)
	switch {
	case err == nil:
		return existing, tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return 0, err
	}

	res, err := tx.Exec(`INSERT INTO coord_messages(
			room_key,sender_external_id,sender_kind,client_id,kind,body,reply_to,created_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		m.RoomKey, m.SenderExternalID, senderKind, m.ClientID, kind, m.Body,
		nullableCoordID(m.ReplyTo), at)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, r := range m.Refs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO coord_message_refs(message_id,ref_kind,ref_id)
			VALUES(?,?,?)`, id, r.Kind, r.ID); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// nullableCoordID schreibt 0 als NULL. Eine Antwort auf Nachricht 0 gibt es
// nicht, und eine 0 in der Spalte sähe aus wie eine echte Referenz.
func nullableCoordID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// CoordMessagesSince liefert das Fenster nach afterID; afterID=0 ist der
// Anfang. Das Limit ist gedeckelt, damit ein einzelner Aufruf nicht einen
// ganzen Raum in einen Modellkontext kippt.
func (s *Store) CoordMessagesSince(roomKey string, afterID int64, limit int) ([]CoordMessage, error) {
	if s.reader != nil {
		return s.reader.CoordMessagesSince(roomKey, afterID, limit)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id,room_key,sender_external_id,sender_kind,client_id,
			kind,body,COALESCE(reply_to,0),created_at
		FROM coord_messages WHERE room_key=? AND id>? ORDER BY id LIMIT ?`,
		roomKey, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CoordMessage
	for rows.Next() {
		var m CoordMessage
		if err := rows.Scan(&m.ID, &m.RoomKey, &m.SenderExternalID, &m.SenderKind,
			&m.ClientID, &m.Kind, &m.Body, &m.ReplyTo, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CoordMessageRefs liest die Objektbezüge einer Nachricht. Getrennt vom
// Fenster oben, weil eine Inbox die Bezüge selten braucht und ein Join sie
// jedem Aufruf aufladen würde.
func (s *Store) CoordMessageRefs(messageID int64) ([]CoordRef, error) {
	if s.reader != nil {
		return s.reader.CoordMessageRefs(messageID)
	}
	rows, err := s.db.Query(`SELECT ref_kind,ref_id FROM coord_message_refs
		WHERE message_id=? ORDER BY ref_kind,ref_id`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CoordRef
	for rows.Next() {
		var r CoordRef
		if err := rows.Scan(&r.Kind, &r.ID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
