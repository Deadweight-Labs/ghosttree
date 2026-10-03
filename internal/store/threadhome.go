package store

import (
	"database/sql"
	"errors"
)

var ErrAnchorAlreadyThreaded = errors.New("coordination message already has a task thread")

type ThreadHome struct {
	ThreadID        int64  `json:"thread_id"`
	RoomKey         string `json:"room_key"`
	AnchorMessageID int64  `json:"anchor_message_id"`
	CreatedAt       string `json:"created_at"`
}

type RoomThread struct {
	Thread         Thread     `json:"thread"`
	Home           ThreadHome `json:"home"`
	AnchorSequence int64      `json:"anchor_sequence"`
	RequestID      string     `json:"request_id,omitempty"`
	RequestTitle   string     `json:"request_title,omitempty"`
	RequestState   string     `json:"request_state,omitempty"`
}

func threadHomeTx(tx *sql.Tx, threadID int64) (ThreadHome, bool, error) {
	var home ThreadHome
	err := tx.QueryRow(`SELECT thread_id,room_key,COALESCE(anchor_message_id,0),created_at
		FROM thread_homes WHERE thread_id=?`, threadID).
		Scan(&home.ThreadID, &home.RoomKey, &home.AnchorMessageID, &home.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ThreadHome{}, false, nil
	}
	return home, err == nil, err
}

func roomThreadsTx(tx *sql.Tx, roomKey string) ([]RoomThread, error) {
	rows, err := tx.Query(`SELECT t.id,t.project,t.title,t.question,t.state,t.archived,t.person,
			t.author_principal_id,t.created_at,t.updated_at,COALESCE(t.resolved_at,''),
			h.room_key,COALESCE(h.anchor_message_id,0),h.created_at,COALESCE(a.sequence,0),
			CASE WHEN r.id IS NULL THEN '' ELSE l.object_id END,COALESCE(r.title,''),COALESCE(r.state,'')
		FROM thread_homes h
		JOIN threads t ON t.id=h.thread_id
		LEFT JOIN coord_messages a ON a.id=h.anchor_message_id AND a.destination_kind='room' AND a.destination_id=h.room_key
		LEFT JOIN thread_links l ON l.thread_id=t.id AND l.object_kind='request'
			AND l.rowid=(SELECT MIN(l2.rowid) FROM thread_links l2 WHERE l2.thread_id=t.id AND l2.object_kind='request')
		LEFT JOIN requests r ON l.object_id='REQ-' || r.id
			AND (TRIM(r.project)='' OR 'project:'||TRIM(r.project)=h.room_key)
		WHERE h.room_key=?
		ORDER BY t.archived,t.updated_at DESC,t.id DESC`, roomKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RoomThread
	for rows.Next() {
		var item RoomThread
		var archived int
		if err := rows.Scan(&item.Thread.ID, &item.Thread.Project, &item.Thread.Title,
			&item.Thread.Question, &item.Thread.State, &archived, &item.Thread.Person,
			&item.Thread.AuthorPrincipalID, &item.Thread.CreatedAt, &item.Thread.UpdatedAt,
			&item.Thread.ResolvedAt, &item.Home.RoomKey, &item.Home.AnchorMessageID,
			&item.Home.CreatedAt, &item.AnchorSequence, &item.RequestID, &item.RequestTitle, &item.RequestState); err != nil {
			return nil, err
		}
		item.Thread.Archived = archived != 0
		item.Thread.Dormant = threadIsDormant(item.Thread)
		item.Home.ThreadID = item.Thread.ID
		out = append(out, item)
	}
	return out, rows.Err()
}

// ensureThreadHomes gives a thread opened through the API (the MCP thread_open
// tool, before it wrote its own home) the project room as its home, so the room
// lists it. A thread whose room does not exist stays as it is. Idempotent.
func ensureThreadHomes(db *sql.DB) error {
	_, err := db.Exec(`INSERT INTO thread_homes(thread_id,room_key,anchor_message_id,created_at)
		SELECT t.id,'project:'||TRIM(t.project),NULL,t.created_at FROM threads t
		WHERE NOT EXISTS (SELECT 1 FROM thread_homes h WHERE h.thread_id=t.id)
		AND NOT EXISTS (SELECT 1 FROM thread_visibility v WHERE v.thread_id=t.id)
		AND EXISTS (SELECT 1 FROM coord_rooms r WHERE r.room_key='project:'||TRIM(t.project))`)
	return err
}
