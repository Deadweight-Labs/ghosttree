package store

import (
	"database/sql"
	"errors"
	"fmt"
)

const (
	messageWindowLatest = "latest"
	messageWindowBefore = "before"
	messageWindowAfter  = "after"
)

var ErrCoordInvalidSequence = errors.New("invalid coordination sequence")

type MessageWindow struct {
	Mode     string
	Sequence int64
	Limit    int
}

type MessagePage struct {
	Messages  []CoordMessage
	HighWater int64
	HasOlder  bool
	HasNewer  bool
}

func LatestWindow(limit int) MessageWindow {
	return MessageWindow{Mode: messageWindowLatest, Limit: limit}
}

func BeforeWindow(sequence int64, limit int) MessageWindow {
	return MessageWindow{Mode: messageWindowBefore, Sequence: sequence, Limit: limit}
}

func AfterWindow(sequence int64, limit int) MessageWindow {
	return MessageWindow{Mode: messageWindowAfter, Sequence: sequence, Limit: limit}
}

func (s *Store) CoordMessageWindow(destinationKind, destinationID string, window MessageWindow) (MessagePage, error) {
	if s.reader != nil {
		return s.reader.CoordMessageWindow(destinationKind, destinationID, window)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return MessagePage{}, err
	}
	defer tx.Rollback()
	page, err := coordMessageWindowTx(tx, destinationKind, destinationID, window)
	if err != nil {
		return MessagePage{}, err
	}
	if err := tx.Commit(); err != nil {
		return MessagePage{}, err
	}
	return page, nil
}

func coordMessageWindowTx(tx *sql.Tx, destinationKind, destinationID string, window MessageWindow) (MessagePage, error) {
	limit := window.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	switch window.Mode {
	case messageWindowLatest:
	case messageWindowBefore:
		if window.Sequence <= 0 {
			return MessagePage{}, fmt.Errorf("%w: before must be positive", ErrCoordInvalidSequence)
		}
	case messageWindowAfter:
		if window.Sequence < 0 {
			return MessagePage{}, fmt.Errorf("%w: after must not be negative", ErrCoordInvalidSequence)
		}
	default:
		return MessagePage{}, fmt.Errorf("%w: unknown window mode", ErrCoordInvalidSequence)
	}

	page := MessagePage{}
	if err := tx.QueryRow(`SELECT COALESCE((SELECT last_sequence FROM coord_destination_sequences
		WHERE destination_kind=? AND destination_id=?),0)`, destinationKind, destinationID).Scan(&page.HighWater); err != nil {
		return MessagePage{}, err
	}
	if window.Mode != messageWindowLatest && window.Sequence > 0 {
		if window.Sequence > page.HighWater {
			return MessagePage{}, fmt.Errorf("%w: anchor exceeds destination high-water", ErrCoordInvalidSequence)
		}
	}

	const columns = `id,destination_kind,destination_id,sequence,
		sender_external_id,author_principal_id,author_kind,
		COALESCE(parent_external_id,''),client_id,kind,intent,priority,body,
		COALESCE(reply_to,0),COALESCE(origin_event_id,''),
		COALESCE(causation_id,''),COALESCE(expires_at,''),
		COALESCE(observed_at_client,''),created_at`
	var query string
	var args []any
	switch window.Mode {
	case messageWindowLatest:
		query = `SELECT ` + columns + ` FROM (
			SELECT * FROM coord_messages
			WHERE destination_kind=? AND destination_id=? AND sequence<=?
			ORDER BY sequence DESC LIMIT ?)
			ORDER BY sequence`
		args = []any{destinationKind, destinationID, page.HighWater, limit}
	case messageWindowBefore:
		query = `SELECT ` + columns + ` FROM (
			SELECT * FROM coord_messages
			WHERE destination_kind=? AND destination_id=? AND sequence<? AND sequence<=?
			ORDER BY sequence DESC LIMIT ?)
			ORDER BY sequence`
		args = []any{destinationKind, destinationID, window.Sequence, page.HighWater, limit}
	case messageWindowAfter:
		query = `SELECT ` + columns + ` FROM coord_messages
			WHERE destination_kind=? AND destination_id=? AND sequence>? AND sequence<=?
			ORDER BY sequence LIMIT ?`
		args = []any{destinationKind, destinationID, window.Sequence, page.HighWater, limit}
	}
	rows, err := tx.Query(query, args...)
	if err != nil {
		return MessagePage{}, err
	}
	nowTS := now()
	for rows.Next() {
		var m CoordMessage
		if err := rows.Scan(&m.ID, &m.DestinationKind, &m.DestinationID, &m.Sequence,
			&m.SenderExternalID, &m.AuthorPrincipalID, &m.AuthorKind,
			&m.ParentExternalID, &m.ClientID, &m.Kind, &m.Intent, &m.Priority,
			&m.Body, &m.ReplyTo, &m.OriginEventID, &m.CausationID, &m.ExpiresAt,
			&m.ObservedAtClient, &m.CreatedAt); err != nil {
			rows.Close()
			return MessagePage{}, err
		}
		m.Expired = expiredAt(m.ExpiresAt, nowTS)
		page.Messages = append(page.Messages, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return MessagePage{}, err
	}
	if err := rows.Close(); err != nil {
		return MessagePage{}, err
	}
	if len(page.Messages) > 0 {
		first := page.Messages[0].Sequence
		last := page.Messages[len(page.Messages)-1].Sequence
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM coord_messages
			WHERE destination_kind=? AND destination_id=? AND sequence<?)`, destinationKind, destinationID, first).Scan(&page.HasOlder); err != nil {
			return MessagePage{}, err
		}
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM coord_messages
			WHERE destination_kind=? AND destination_id=? AND sequence>? AND sequence<=?)`, destinationKind, destinationID, last, page.HighWater).Scan(&page.HasNewer); err != nil {
			return MessagePage{}, err
		}
		return page, nil
	}
	if window.Mode == messageWindowBefore {
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM coord_messages
			WHERE destination_kind=? AND destination_id=? AND sequence<?)`, destinationKind, destinationID, window.Sequence).Scan(&page.HasOlder); err != nil {
			return MessagePage{}, err
		}
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM coord_messages
			WHERE destination_kind=? AND destination_id=? AND sequence>=? AND sequence<=?)`, destinationKind, destinationID, window.Sequence, page.HighWater).Scan(&page.HasNewer); err != nil {
			return MessagePage{}, err
		}
	} else if window.Mode == messageWindowAfter {
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM coord_messages
			WHERE destination_kind=? AND destination_id=? AND sequence<=?)`, destinationKind, destinationID, window.Sequence).Scan(&page.HasOlder); err != nil {
			return MessagePage{}, err
		}
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM coord_messages
			WHERE destination_kind=? AND destination_id=? AND sequence>? AND sequence<=?)`, destinationKind, destinationID, window.Sequence, page.HighWater).Scan(&page.HasNewer); err != nil {
			return MessagePage{}, err
		}
	}
	return page, nil
}
