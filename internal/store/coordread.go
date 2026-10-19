package store

import (
	"database/sql"
	"fmt"
	"sort"
)

type CoordReadState struct {
	PrincipalID      string
	DestinationKind  string
	DestinationID    string
	ReadThrough      int64
	ManualUnreadFrom int64
	UpdatedAt        string
}

type CoordRoomSummary struct {
	Room             CoordRoom
	LastSequence     int64
	ReadThrough      int64
	ManualUnreadFrom int64
	Unread           int64
	MentionUnread    int64
	// NeedsYou counts each message once that either mentions the viewer
	// unread or holds an open attention item for them.
	NeedsYou      int64
	Attention     int64
	LastMessageAt string
	lastMessageID int64
}

func (s *Store) CoordReadState(principal, kind, id string) (CoordReadState, error) {
	if s.reader != nil {
		return s.reader.CoordReadState(principal, kind, id)
	}
	state := CoordReadState{PrincipalID: principal, DestinationKind: kind, DestinationID: id}
	err := s.db.QueryRow(`SELECT read_through_sequence,
		COALESCE(manual_unread_from_sequence,0),updated_at
		FROM coord_read_state WHERE principal_id=? AND destination_kind=? AND destination_id=?`,
		principal, kind, id).Scan(&state.ReadThrough, &state.ManualUnreadFrom, &state.UpdatedAt)
	if err == nil || err == sql.ErrNoRows {
		return state, nil
	}
	return CoordReadState{}, err
}

func (s *Store) MarkCoordRead(principal, kind, id string, through int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{principal, kind, id, through}, func(d *Store, p []any) error {
			return d.MarkCoordRead(p[0].(string), p[1].(string), p[2].(string), p[3].(int64))
		})
	}
	if err := validateReadTarget(principal, kind, id); err != nil {
		return err
	}
	if through < 0 {
		return fmt.Errorf("%w: read sequence must not be negative", ErrCoordInvalidSequence)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	max, err := destinationHighWater(tx, kind, id)
	if err != nil {
		return err
	}
	if through > max {
		return fmt.Errorf("%w: read sequence exceeds destination high-water", ErrCoordInvalidSequence)
	}
	at := now()
	_, err = tx.Exec(`INSERT INTO coord_read_state(
		principal_id,destination_kind,destination_id,read_through_sequence,manual_unread_from_sequence,updated_at)
		VALUES(?,?,?,?,NULL,?)
		ON CONFLICT(principal_id,destination_kind,destination_id) DO UPDATE SET
		read_through_sequence=MAX(coord_read_state.read_through_sequence,excluded.read_through_sequence),
		manual_unread_from_sequence=CASE
			WHEN coord_read_state.manual_unread_from_sequence<=excluded.read_through_sequence THEN NULL
			ELSE coord_read_state.manual_unread_from_sequence END,
		updated_at=excluded.updated_at`, principal, kind, id, through, at)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkCoordUnread(principal, kind, id string, from int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{principal, kind, id, from}, func(d *Store, p []any) error {
			return d.MarkCoordUnread(p[0].(string), p[1].(string), p[2].(string), p[3].(int64))
		})
	}
	if err := validateReadTarget(principal, kind, id); err != nil {
		return err
	}
	if from <= 0 {
		return fmt.Errorf("%w: unread sequence must be positive", ErrCoordInvalidSequence)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM coord_messages
		WHERE destination_kind=? AND destination_id=? AND sequence=?)`, kind, id, from).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: unread sequence does not exist in destination", ErrCoordInvalidSequence)
	}
	_, err = tx.Exec(`INSERT INTO coord_read_state(
		principal_id,destination_kind,destination_id,read_through_sequence,manual_unread_from_sequence,updated_at)
		VALUES(?,?,?,0,?,?)
		ON CONFLICT(principal_id,destination_kind,destination_id) DO UPDATE SET
		manual_unread_from_sequence=excluded.manual_unread_from_sequence,
		updated_at=excluded.updated_at`, principal, kind, id, from, now())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func validateReadTarget(principal, kind, id string) error {
	if principal == "" || id == "" {
		return fmt.Errorf("principal and destination are required")
	}
	if kind != DestinationRoom && kind != DestinationDiscussion {
		return fmt.Errorf("unknown destination kind %q", kind)
	}
	return nil
}

type queryRower interface {
	QueryRow(query string, args ...any) *sql.Row
}

func destinationHighWater(q queryRower, kind, id string) (int64, error) {
	var highWater int64
	err := q.QueryRow(`SELECT COALESCE((SELECT last_sequence FROM coord_destination_sequences
		WHERE destination_kind=? AND destination_id=?),0)`, kind, id).Scan(&highWater)
	return highWater, err
}

// homeVisibleToSQL is true for a thread without a visibility list or one that
// lists the viewer (its single placeholder), matching threadAccessTx. It needs
// the thread's home row in scope as "home".
const homeVisibleToSQL = `(NOT EXISTS(SELECT 1 FROM thread_visibility v WHERE v.thread_id=home.thread_id)
	OR EXISTS(SELECT 1 FROM thread_visibility v WHERE v.thread_id=home.thread_id AND v.member_external_id=?))`

func (s *Store) projectRoomSummaries(actor, principalID, agentID string, rooms []CoordRoom, byRole map[string]bool) ([]CoordRoomSummary, error) {
	if s.reader != nil {
		return s.reader.projectRoomSummaries(actor, principalID, agentID, rooms, byRole)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	out := make([]CoordRoomSummary, 0, len(rooms))
	for _, room := range rooms {
		visible, err := roomVisibleInSummarySnapshot(tx, room, actor, principalID, agentID, byRole[room.Key])
		if err != nil {
			return nil, err
		}
		if !visible {
			continue
		}
		summary := CoordRoomSummary{Room: room}
		var manual sql.NullInt64
		err = tx.QueryRow(`SELECT read_through_sequence,manual_unread_from_sequence
			FROM coord_read_state WHERE principal_id=? AND destination_kind='room' AND destination_id=?`, actor, room.Key).
			Scan(&summary.ReadThrough, &manual)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if manual.Valid {
			summary.ManualUnreadFrom = manual.Int64
		}
		err = tx.QueryRow(`SELECT id,sequence,created_at FROM coord_messages
			WHERE destination_kind='room' AND destination_id=?
			ORDER BY sequence DESC LIMIT 1`, room.Key).Scan(&summary.lastMessageID, &summary.LastSequence, &summary.LastMessageAt)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if err := tx.QueryRow(`SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN EXISTS(
				SELECT 1 FROM coord_message_mentions mm
				WHERE mm.message_id=m.id AND mm.mentioned_external_id=?
			) THEN 1 ELSE 0 END),0)
			FROM coord_messages m
			WHERE m.destination_kind='room' AND m.destination_id=?
			  AND m.sender_external_id<>?
			  AND (m.sequence>? OR (?>0 AND m.sequence>=?))`,
			actor, room.Key, actor,
			summary.ReadThrough, summary.ManualUnreadFrom, summary.ManualUnreadFrom).
			Scan(&summary.Unread, &summary.MentionUnread); err != nil {
			return nil, err
		}
		var threadMentions int64
		if err := tx.QueryRow(`SELECT COUNT(*) FROM coord_messages m
			JOIN thread_homes home ON CAST(home.thread_id AS TEXT)=m.destination_id
			JOIN coord_message_mentions mention ON mention.message_id=m.id
			LEFT JOIN coord_read_state read ON read.principal_id=?
			  AND read.destination_kind='discussion' AND read.destination_id=m.destination_id
			WHERE m.destination_kind='discussion' AND home.room_key=?
			  AND m.sender_external_id<>? AND mention.mentioned_external_id=?
			  AND (m.sequence>COALESCE(read.read_through_sequence,0)
			       OR (COALESCE(read.manual_unread_from_sequence,0)>0
			           AND m.sequence>=read.manual_unread_from_sequence))
			  AND `+homeVisibleToSQL+``,
			actor, room.Key, actor, actor, actor).Scan(&threadMentions); err != nil {
			return nil, err
		}
		summary.MentionUnread += threadMentions
		if err := tx.QueryRow(`SELECT COUNT(*) FROM coord_attention attention
			JOIN coord_messages m ON m.id=attention.message_id
			LEFT JOIN thread_homes home ON m.destination_kind='discussion'
			  AND CAST(home.thread_id AS TEXT)=m.destination_id
			WHERE attention.recipient_principal_id=? AND attention.state='open'
			  AND (m.expires_at IS NULL OR m.expires_at='' OR julianday(m.expires_at) IS NULL
			       OR julianday(m.expires_at)>=julianday(?))
			  AND ((m.destination_kind='room' AND m.destination_id=?)
			       OR (m.destination_kind='discussion' AND home.room_key=?))
			  AND (m.destination_kind='room' OR `+homeVisibleToSQL+`)`,
			actor, now(), room.Key, room.Key, actor).Scan(&summary.Attention); err != nil {
			return nil, err
		}
		if err := tx.QueryRow(`SELECT COUNT(*) FROM (
			SELECT m.id FROM coord_messages m
			JOIN coord_message_mentions mention ON mention.message_id=m.id
			WHERE m.destination_kind='room' AND m.destination_id=?
			  AND m.sender_external_id<>? AND mention.mentioned_external_id=?
			  AND (m.sequence>? OR (?>0 AND m.sequence>=?))
			UNION
			SELECT m.id FROM coord_messages m
			JOIN thread_homes home ON CAST(home.thread_id AS TEXT)=m.destination_id
			JOIN coord_message_mentions mention ON mention.message_id=m.id
			LEFT JOIN coord_read_state read ON read.principal_id=?
			  AND read.destination_kind='discussion' AND read.destination_id=m.destination_id
			WHERE m.destination_kind='discussion' AND home.room_key=?
			  AND m.sender_external_id<>? AND mention.mentioned_external_id=?
			  AND (m.sequence>COALESCE(read.read_through_sequence,0)
			       OR (COALESCE(read.manual_unread_from_sequence,0)>0
			           AND m.sequence>=read.manual_unread_from_sequence))
			  AND `+homeVisibleToSQL+`
			UNION
			SELECT m.id FROM coord_attention attention
			JOIN coord_messages m ON m.id=attention.message_id
			LEFT JOIN thread_homes home ON m.destination_kind='discussion'
			  AND CAST(home.thread_id AS TEXT)=m.destination_id
			WHERE attention.recipient_principal_id=? AND attention.state='open'
			  AND (m.expires_at IS NULL OR m.expires_at='' OR julianday(m.expires_at) IS NULL
			       OR julianday(m.expires_at)>=julianday(?))
			  AND ((m.destination_kind='room' AND m.destination_id=?)
			       OR (m.destination_kind='discussion' AND home.room_key=?))
			  AND (m.destination_kind='room' OR `+homeVisibleToSQL+`))`,
			room.Key, actor, actor, summary.ReadThrough, summary.ManualUnreadFrom, summary.ManualUnreadFrom,
			actor, room.Key, actor, actor, actor,
			actor, now(), room.Key, room.Key, actor).Scan(&summary.NeedsYou); err != nil {
			return nil, err
		}
		out = append(out, summary)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].lastMessageID != out[j].lastMessageID {
			return out[i].lastMessageID > out[j].lastMessageID
		}
		return out[i].Room.Key < out[j].Room.Key
	})
	return out, nil
}

func roomVisibleInSummarySnapshot(tx *sql.Tx, room CoordRoom, actor, principalID, agentID string, byRole bool) (bool, error) {
	var count int
	if byRole {
		return true, nil
	}
	switch room.Kind {
	case RoomDirect, RoomGroup:
		err := tx.QueryRow(`SELECT COUNT(*) FROM coord_room_memberships
			WHERE room_key=? AND principal_id=? AND left_at=''`, room.Key, actor).Scan(&count)
		return count > 0, err
	case RoomProject, RoomMachine:
		if agentID != "" {
			err := tx.QueryRow(`SELECT COUNT(*) FROM coord_room_memberships
				WHERE room_key=? AND principal_id=? AND left_at=''`, room.Key, actor).Scan(&count)
			return count > 0, err
		}
		err := tx.QueryRow(`SELECT COUNT(*) FROM coord_room_memberships m
			LEFT JOIN coord_agents a ON a.external_id=m.principal_id
			WHERE m.room_key=? AND m.left_at=''
			  AND (m.principal_id=? OR a.principal_id=?)`, room.Key, principalID, principalID).Scan(&count)
		return count > 0, err
	default:
		return false, nil
	}
}
