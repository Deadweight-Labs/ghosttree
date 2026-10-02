package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"strings"
	"time"
)

// Zielarten einer Nachricht. Dieselbe Primitive adressiert einen Raum und
// eine Diskussion; Räume und Diskussionen behalten trotzdem eigene Aggregate
// und Regeln. Eine gemeinsame Hülle ist kein Auftrag, alle Ghosttree-Objekte
// in eine Riesentabelle zu legen.
const (
	DestinationRoom       = "room"
	DestinationDiscussion = "discussion"
)

// Autorenarten. Mensch, Agent und Systemereignis sind verschiedene Dinge, und
// die Unterscheidung wird aus der authentifizierten Verbindung bestimmt, nie
// aus dem Rumpf einer Nachricht. Ein Agent, der "human" behauptet, erzeugt
// keine menschliche Freigabe.
const (
	AuthorAgent  = "agent"
	AuthorHuman  = "human"
	AuthorSystem = "system"
)

// CoordMessage ist ein dauerhafter Beitrag an einem Ziel.
//
// Zwei Schlüssel machen das Senden wiederholbar, und sie tun verschiedene
// Dinge: ClientID kommt vom Absender und fängt den eigenen Retry nach einem
// Timeout ab. OriginEventID benennt ein fremdes Ereignis, aus dem dieser
// Beitrag gespiegelt wurde, und verhindert, dass native Harness-Zustellung
// und Ghosttree-Zustellung dieselbe Nachricht zweimal ans Modell liefern.
type CoordMessage struct {
	ID              int64  `json:"id,omitempty"`
	DestinationKind string `json:"destination_kind"`
	DestinationID   string `json:"destination_id"`
	// Sequence zählt je Ziel und beginnt bei 1. Eine globale Zeilennummer
	// taugt dafür nicht: die Kontextkarte einer Diskussion grenzt sich mit
	// covers_through_sequence gegen genau ihr Ziel ab, und Zeitstempel
	// verschiedener Rechner sind keine verlässliche Kausalordnung.
	Sequence          int64      `json:"sequence,omitempty"`
	SenderExternalID  string     `json:"sender_external_id"`
	AuthorPrincipalID string     `json:"author_principal_id,omitempty"`
	AuthorKind        string     `json:"author_kind,omitempty"`
	ParentExternalID  string     `json:"parent_external_id,omitempty"`
	ClientID          string     `json:"client_id"`
	Kind              string     `json:"kind,omitempty"`
	Intent            string     `json:"intent,omitempty"`
	Priority          string     `json:"priority,omitempty"`
	Body              string     `json:"body"`
	ReplyTo           int64      `json:"reply_to,omitempty"`
	OriginEventID     string     `json:"origin_event_id,omitempty"`
	CausationID       string     `json:"causation_id,omitempty"`
	ExpiresAt         string     `json:"expires_at,omitempty"`
	ObservedAtClient  string     `json:"observed_at_client,omitempty"`
	CreatedAt         string     `json:"created_at,omitempty"`
	Mentions          []string   `json:"mentions,omitempty"`
	Refs              []CoordRef `json:"refs,omitempty"`
	// Expired ist abgeleitet und wird nicht gespeichert. Eine abgelaufene
	// Meldung bleibt Geschichte und verschwindet nicht; sie darf nur nicht
	// als gegenwärtig gelesen werden. "Die API ist 20 Sekunden weg" von
	// gestern ist kein Grund, heute zu warten.
	Expired bool `json:"expired,omitempty"`
	// SenderRole, RecipientRole und Authority sind abgeleitet, werden nie
	// gespeichert und nur in Antworten an einen angemeldeten Agenten gesetzt.
	// Was ein Client hier mitschickt, verwirft Send.
	SenderRole    string `json:"sender_role,omitempty"`
	RecipientRole string `json:"recipient_role,omitempty"`
	Authority     string `json:"authority,omitempty"`
	// SenderDisplayName ist der Kontoname des menschlichen Absenders, live
	// aus persons gelesen und nie gespeichert. Er fehlt für Agenten und für
	// Leser, die die Mitglieder des Projektraums nicht sehen (Gast). Der Wert
	// ist Nutzereingabe: Anzeigende Clients müssen ihn entschärfen.
	SenderDisplayName string `json:"sender_display_name,omitempty"`
}

// CoordRef verbindet eine Nachricht mit einem bestehenden Ghosttree-Objekt.
// Ohne das ist eine Abstimmung nur Text; damit ist sie der Weg zurück zu der
// Entscheidung, dem Auftrag oder dem Beleg, um den es ging.
//
// Revision unterscheidet zwei Arten von Verweis, und die Unterscheidung ist
// nicht kosmetisch: eine Referenz auf einen Wissenseintrag zeigt auf einen
// Head, der sich morgen ändert; eine auf Dokument 360 rev 1 nicht. Wer beides
// gleich behandelt, zitiert später einen Text, den nie jemand geschrieben hat.
// Leere Revision heißt veränderlicher Head — und das wird ausgewiesen, statt
// den Verweis als Beleg des damaligen Wortlauts auszugeben.
type CoordRef struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Revision string `json:"revision,omitempty"`
	// MutableHead ist abgeleitet und wird nicht gespeichert.
	MutableHead bool `json:"mutable_head,omitempty"`
}

// AppendCoordMessage speichert einen Beitrag, bevor irgendjemand seinen
// Empfang bestätigt. Zwei Wiederholungsfälle werden abgefangen, und beide
// geben die vorhandene ID zurück, ohne den gespeicherten Inhalt anzutasten:
// derselbe Absender mit derselben ClientID, und dasselbe fremde Ereignis mit
// derselben OriginEventID.
func (s *Store) AppendCoordMessage(m CoordMessage) (int64, error) {
	if s.writer != nil {
		return queueValue(s, []any{m}, func(d *Store, p []any) (int64, error) {
			return d.AppendCoordMessage(p[0].(CoordMessage))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := appendCoordMessageTx(tx, m)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// ErrCoordInvalidExpiry: expires_at must be RFC 3339, otherwise the message
// would silently never expire.
var ErrCoordInvalidExpiry = errors.New("coordination expiry is not an RFC 3339 timestamp")

func appendCoordMessageTx(tx *sql.Tx, m CoordMessage) (int64, error) {
	m = m.withDefaults()
	if m.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339, m.ExpiresAt); err != nil {
			return 0, ErrCoordInvalidExpiry
		}
	}
	// Systemmeldungen gehen nie durch die Wiederholungserkennung: sie kommen nur
	// aus dem Store, und eine vorab gesendete ClientID darf sie nicht
	// unterdrücken.
	if m.AuthorKind != AuthorSystem {
		if id, found, err := existingCoordMessage(tx, m); err != nil {
			return 0, err
		} else if found {
			return id, nil
		}
	}

	// The high-water row survives retention, so an emptied destination never
	// reuses a sequence that an existing read marker may already cover.
	var seq int64
	if _, err := tx.Exec(`INSERT OR IGNORE INTO coord_destination_sequences(
		destination_kind,destination_id,last_sequence) VALUES(?,?,0)`,
		m.DestinationKind, m.DestinationID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE coord_destination_sequences SET last_sequence=last_sequence+1
		WHERE destination_kind=? AND destination_id=?`, m.DestinationKind, m.DestinationID); err != nil {
		return 0, err
	}
	if err := tx.QueryRow(`SELECT last_sequence FROM coord_destination_sequences
		WHERE destination_kind=? AND destination_id=?`, m.DestinationKind, m.DestinationID).Scan(&seq); err != nil {
		return 0, err
	}

	res, err := tx.Exec(`INSERT INTO coord_messages(
			destination_kind,destination_id,sequence,sender_external_id,
			author_principal_id,author_kind,parent_external_id,client_id,kind,
			intent,priority,body,reply_to,origin_event_id,causation_id,
			expires_at,observed_at_client,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.DestinationKind, m.DestinationID, seq, m.SenderExternalID,
		m.AuthorPrincipalID, m.AuthorKind, nullableText(m.ParentExternalID),
		m.ClientID, m.Kind, m.Intent, m.Priority, m.Body,
		nullableCoordID(m.ReplyTo), nullableText(m.OriginEventID),
		nullableText(m.CausationID), nullableText(m.ExpiresAt),
		nullableText(m.ObservedAtClient), m.CreatedAt)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, r := range m.Refs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO coord_message_refs(message_id,ref_kind,ref_id,ref_revision)
			VALUES(?,?,?,?)`, id, r.Kind, r.ID, r.Revision); err != nil {
			return 0, err
		}
	}
	for _, mention := range m.Mentions {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO coord_message_mentions(message_id,mentioned_external_id)
			VALUES(?,?)`, id, mention); err != nil {
			return 0, err
		}
	}
	if reason, ok := attentionReasonForIntent(m.Intent); ok {
		state := AttentionOpen
		if expiredAt(m.ExpiresAt, now()) {
			state = AttentionExpired
		}
		roomKey := attentionRoomKeyTx(tx, m.DestinationKind, m.DestinationID)
		for _, recipient := range normalizeMembers(m.Mentions) {
			if err := insertAttentionTx(tx, recipient, id, reason, state, m.CreatedAt, roomKey); err != nil {
				return 0, err
			}
		}
	}
	return id, nil
}

// withDefaults füllt, was der Store selbst verantwortet. AuthorKind fällt
// bewusst auf agent zurück und nie auf human: wer keinen belegten
// menschlichen Ursprung mitbringt, bekommt auch keinen.
func (m CoordMessage) withDefaults() CoordMessage {
	if m.CreatedAt == "" {
		m.CreatedAt = now()
	}
	if m.Kind == "" {
		m.Kind = "message"
	}
	if m.AuthorKind == "" {
		m.AuthorKind = AuthorAgent
	}
	if m.Priority == "" {
		m.Priority = "normal"
	}
	if m.DestinationKind == "" {
		m.DestinationKind = DestinationRoom
	}
	return m
}

// existingCoordMessage beantwortet beide Wiederholungsfragen in einer Runde.
// Kein UPDATE: wer denselben Schlüssel mit anderem Inhalt sendet, bekommt den
// alten Inhalt zurück, statt ihn dem Empfänger unter der Hand auszutauschen.
func existingCoordMessage(tx *sql.Tx, m CoordMessage) (int64, bool, error) {
	var id int64
	err := tx.QueryRow(`SELECT id FROM coord_messages
		WHERE sender_external_id=? AND client_id=?`,
		m.SenderExternalID, m.ClientID).Scan(&id)
	switch {
	case err == nil:
		return id, true, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, false, err
	}
	if m.OriginEventID == "" {
		return 0, false, nil
	}
	err = tx.QueryRow(`SELECT id FROM coord_messages WHERE origin_event_id=?`,
		m.OriginEventID).Scan(&id)
	switch {
	case err == nil:
		return id, true, nil
	case errors.Is(err, sql.ErrNoRows):
		return 0, false, nil
	default:
		return 0, false, err
	}
}

func nullableCoordID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// CoordMessagesSince liefert das Fenster nach afterID; afterID=0 ist der
// Anfang. Das Limit ist gedeckelt, damit ein einzelner Aufruf nicht ein
// ganzes Ziel in einen Modellkontext kippt.
//
// Abgelaufene Nachrichten werden ausgeliefert und als abgelaufen markiert,
// nicht weggefiltert: sie bleiben Geschichte, dürfen aber nicht als
// gegenwärtige Lage gelesen werden.
func (s *Store) CoordMessagesSince(destinationKind, destinationID string, afterID int64, limit int) ([]CoordMessage, error) {
	if s.reader != nil {
		return s.reader.CoordMessagesSince(destinationKind, destinationID, afterID, limit)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id,destination_kind,destination_id,sequence,
			sender_external_id,author_principal_id,author_kind,
			COALESCE(parent_external_id,''),client_id,kind,intent,priority,body,
			COALESCE(reply_to,0),COALESCE(origin_event_id,''),
			COALESCE(causation_id,''),COALESCE(expires_at,''),
			COALESCE(observed_at_client,''),created_at
		FROM coord_messages
		WHERE destination_kind=? AND destination_id=? AND id>?
		ORDER BY id LIMIT ?`,
		destinationKind, destinationID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nowTS := now()
	var out []CoordMessage
	for rows.Next() {
		var m CoordMessage
		if err := rows.Scan(&m.ID, &m.DestinationKind, &m.DestinationID, &m.Sequence,
			&m.SenderExternalID, &m.AuthorPrincipalID, &m.AuthorKind,
			&m.ParentExternalID, &m.ClientID, &m.Kind, &m.Intent, &m.Priority,
			&m.Body, &m.ReplyTo, &m.OriginEventID, &m.CausationID, &m.ExpiresAt,
			&m.ObservedAtClient, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.Expired = expiredAt(m.ExpiresAt, nowTS)
		out = append(out, m)
	}
	return out, rows.Err()
}

// expiredAt vergleicht zwei RFC-3339-Zeitpunkte. Ein unlesbarer Zeitpunkt
// gilt NICHT als abgelaufen: eine kaputte Angabe darf eine Meldung nicht
// stillschweigend entwerten.
func expiredAt(expires, reference string) bool {
	if expires == "" {
		return false
	}
	exp, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		return false
	}
	ref, err := time.Parse(time.RFC3339, reference)
	if err != nil {
		ref = time.Now().UTC()
	}
	return exp.Before(ref)
}

// CoordMessageRefs liest die Objektbezüge einer Nachricht. Getrennt vom
// Fenster oben, weil eine Inbox die Bezüge selten braucht und ein Join sie
// jedem Aufruf aufladen würde.
func (s *Store) CoordMessageRefs(messageID int64) ([]CoordRef, error) {
	if s.reader != nil {
		return s.reader.CoordMessageRefs(messageID)
	}
	rows, err := s.db.Query(`SELECT ref_kind,ref_id,ref_revision FROM coord_message_refs
		WHERE message_id=? ORDER BY ref_kind,ref_id,ref_revision`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CoordRef
	for rows.Next() {
		var r CoordRef
		if err := rows.Scan(&r.Kind, &r.ID, &r.Revision); err != nil {
			return nil, err
		}
		r.MutableHead = r.Revision == ""
		out = append(out, r)
	}
	return out, rows.Err()
}

// CoordMessageMentions liest die ausdrücklich erwähnten Empfänger. Sie sind
// die Grundlage der Zustellregeln: eine Erwähnung wird zeitnah geliefert, ein
// gewöhnlicher Raumbeitrag darf gebündelt werden. Dafür braucht es kein
// dauerhaft mitlesendes Modell.
func (s *Store) CoordMessageMentions(messageID int64) ([]string, error) {
	if s.reader != nil {
		return s.reader.CoordMessageMentions(messageID)
	}
	rows, err := s.db.Query(`SELECT mentioned_external_id FROM coord_message_mentions
		WHERE message_id=? ORDER BY mentioned_external_id`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// attentionIDSource liefert Kandidaten für neue Attention-Ids. Variable, damit
// ein Test eine Kollision erzwingen kann.
var attentionIDSource = randomAttentionID

// randomAttentionID: positive 63-Bit-Zahl aus crypto/rand. Ein Zähler wäre ein
// Seitenkanal: Eine Erwähnung eines Nicht-Mitglieds legt keine Zeile an, die
// eines Mitglieds schon, und die Lücke in den eigenen Ids verriete es einem
// Gast. Nichts darf von monotonen Ids abhängen; sortiert wird nach created_at
// und message_id.
func randomAttentionID() int64 {
	var b [8]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			panic("coordination attention id: " + err.Error())
		}
		if id := int64(binary.BigEndian.Uint64(b[:]) >> 1); id > 0 {
			return id
		}
	}
}

// insertAttentionTx legt einen Eintrag mit zufälliger Id an. Ein Duplikat
// (derselbe Empfänger, dieselbe Nachricht und Art) ist wie bisher ein No-op;
// eine Kollision der Id wird mit einer neuen Zufallszahl wiederholt.
func insertAttentionTx(tx *sql.Tx, recipient string, messageID int64, reason, state, createdAt, roomKey string) error {
	for attempt := 0; attempt < 16; attempt++ {
		id := attentionIDSource()
		var taken int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM coord_attention WHERE id=?`, id).Scan(&taken); err != nil {
			return err
		}
		if taken > 0 {
			continue
		}
		_, err := tx.Exec(`INSERT INTO coord_attention(id,recipient_principal_id,message_id,reason,state,created_at,room_key)
			VALUES(?,?,?,?,?,?,?) ON CONFLICT(recipient_principal_id,message_id,reason) DO NOTHING`,
			id, recipient, messageID, reason, state, createdAt, roomKey)
		if err == nil {
			return nil
		}
		if !strings.Contains(err.Error(), "coord_attention.id") {
			return err
		}
	}
	return errors.New("coordination attention id: no free id after 16 attempts")
}
