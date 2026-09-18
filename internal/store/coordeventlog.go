package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	CoordEventMessage    = "message"
	CoordEventMembership = "membership"
	CoordEventRead       = "read"
	CoordEventAttention  = "attention"
	CoordEventStanding   = "standing"
	CoordEventThread     = "thread"
	CoordEventDelivery   = "delivery"
	CoordEventVisibility = "visibility"

	coordEventRetentionLimit = 512
)

var ErrCoordEventCursor = errors.New("invalid coordination event cursor")

type CoordEvent struct {
	Sequence   int64  `json:"sequence"`
	Kind       string `json:"kind"`
	ObjectKind string `json:"object_kind"`
	ObjectID   string `json:"object_id"`
	CreatedAt  string `json:"created_at"`
}

type CoordEventReplay struct {
	Events         []CoordEvent
	Resync         bool
	Latest         int64
	ScannedThrough int64
}

func (s *Store) LatestCoordEventSequence() (int64, error) {
	if s.reader != nil {
		return s.reader.LatestCoordEventSequence()
	}
	var latest int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(sequence),0) FROM coord_events`).Scan(&latest)
	return latest, err
}

func (s *Store) OldestCoordEventSequence() (int64, error) {
	if s.reader != nil {
		return s.reader.OldestCoordEventSequence()
	}
	var oldest int64
	err := s.db.QueryRow(`SELECT COALESCE(MIN(sequence),0) FROM coord_events`).Scan(&oldest)
	return oldest, err
}

// CoordEventsAfter returns only invalidations the principal can still see.
// Authorization is evaluated during every replay, so retained events cannot
// restore access revoked after they were written.
func (s *Store) CoordEventsAfter(principal Principal, after int64, limit int) (CoordEventReplay, error) {
	if s.reader != nil {
		return s.reader.CoordEventsAfter(principal, after, limit)
	}
	if strings.TrimSpace(principal.ID) == "" {
		return CoordEventReplay{}, ErrCoordForbidden
	}
	if after < 0 {
		return CoordEventReplay{}, ErrCoordEventCursor
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	tx, err := s.db.Begin()
	if err != nil {
		return CoordEventReplay{}, err
	}
	defer tx.Rollback()
	var oldest, latest int64
	if err := tx.QueryRow(`SELECT COALESCE(MIN(sequence),0),COALESCE(MAX(sequence),0) FROM coord_events`).Scan(&oldest, &latest); err != nil {
		return CoordEventReplay{}, err
	}
	if after > latest {
		return CoordEventReplay{}, ErrCoordEventCursor
	}
	if oldest > 0 && after > 0 && after < oldest-1 {
		return CoordEventReplay{Resync: true, Latest: latest, ScannedThrough: latest}, nil
	}
	rows, err := tx.Query(`SELECT sequence,kind,object_kind,object_id,created_at
		FROM coord_events WHERE sequence>? ORDER BY sequence LIMIT ?`, after, limit)
	if err != nil {
		return CoordEventReplay{}, err
	}
	var candidates []CoordEvent
	for rows.Next() {
		var event CoordEvent
		if err := rows.Scan(&event.Sequence, &event.Kind, &event.ObjectKind, &event.ObjectID, &event.CreatedAt); err != nil {
			rows.Close()
			return CoordEventReplay{}, err
		}
		candidates = append(candidates, event)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return CoordEventReplay{}, err
	}
	if err := rows.Close(); err != nil {
		return CoordEventReplay{}, err
	}
	access := CoordAccess{Store: s, Principal: principal}
	actor, err := access.actorTx(tx)
	if err != nil {
		return CoordEventReplay{}, err
	}
	out := make([]CoordEvent, 0, len(candidates))
	scannedThrough := after
	for _, event := range candidates {
		scannedThrough = event.Sequence
		visible, err := coordEventVisibleTx(tx, access, actor, event)
		if err != nil {
			return CoordEventReplay{}, err
		}
		if visible {
			out = append(out, event)
		}
	}
	if err := tx.Commit(); err != nil {
		return CoordEventReplay{}, err
	}
	return CoordEventReplay{Events: out, Latest: latest, ScannedThrough: scannedThrough}, nil
}

func coordEventVisibleTx(tx *sql.Tx, access CoordAccess, actor string, event CoordEvent) (bool, error) {
	var err error
	switch event.ObjectKind {
	case DestinationRoom, DestinationDiscussion:
		err = access.canReadTx(tx, actor, event.ObjectKind, event.ObjectID)
	case "thread":
		threadID, parseErr := strconv.ParseInt(event.ObjectID, 10, 64)
		if parseErr != nil || threadID <= 0 {
			return false, nil
		}
		err = access.canReadThreadTx(tx, actor, threadID)
	case "attention":
		attentionID, parseErr := strconv.ParseInt(event.ObjectID, 10, 64)
		if parseErr != nil || attentionID <= 0 {
			return false, nil
		}
		var recipient, author, sender, kind, id string
		queryErr := tx.QueryRow(`SELECT a.recipient_principal_id,m.author_principal_id,m.sender_external_id,
			m.destination_kind,m.destination_id FROM coord_attention a
			JOIN coord_messages m ON m.id=a.message_id WHERE a.id=?`, attentionID).
			Scan(&recipient, &author, &sender, &kind, &id)
		if errors.Is(queryErr, sql.ErrNoRows) {
			return false, nil
		}
		if queryErr != nil {
			return false, queryErr
		}
		if actor != recipient && actor != sender && access.Principal.ID != author {
			return false, nil
		}
		err = access.canReadTx(tx, actor, kind, id)
	case "principal":
		if event.Kind != CoordEventVisibility {
			return false, nil
		}
		if event.ObjectID == access.Principal.ID {
			return true, nil
		}
		var owned bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM coord_agents
			WHERE external_id=? AND principal_id=?)`, event.ObjectID, access.Principal.ID).Scan(&owned); err != nil {
			return false, err
		}
		return owned, nil
	default:
		return false, nil
	}
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrCoordNotFound) || errors.Is(err, ErrCoordForbidden) {
		return false, nil
	}
	return false, fmt.Errorf("authorize coordination event: %w", err)
}
