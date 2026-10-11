package store

import (
	"errors"
	"fmt"
	"strings"
)

// CreateThreadByRole opens a thread in a project the caller has a role in
// (member and up) without the caller's agent being in that project's room. An
// agent belongs to exactly one project room, so a thread filed for another
// project (REQ-458) can neither register the agent there nor rely on a
// membership it does not have. The write right is the project role, as for a
// person in the browser; the thread has its home in the project room, which is
// created when nobody has been in it yet.
func (s *Store) CreateThreadByRole(p Principal, t Thread) (int64, error) {
	if s.writer != nil {
		return queueValue(s, []any{p, t}, func(d *Store, q []any) (int64, error) {
			return d.CreateThreadByRole(q[0].(Principal), q[1].(Thread))
		})
	}
	if strings.TrimSpace(t.Title) == "" || strings.TrimSpace(t.Project) == "" {
		return 0, fmt.Errorf("a thread needs a title and project")
	}
	switch err := s.Access(p).Check(t.Project, ResRoom, ActPost, Object{}); {
	case errors.Is(err, ErrAccessForbidden):
		return 0, ErrCoordForbidden
	case err != nil:
		return 0, ErrCoordNotFound
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := now()
	room := RoomKeyForProject(t.Project)
	if _, err := tx.Exec(`INSERT OR IGNORE INTO coord_rooms(room_key,kind,label,created_at) VALUES(?,?,?,?)`, room, RoomProject, "", ts); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`INSERT INTO threads(project,title,question,state,archived,person,author_principal_id,created_at,updated_at)
		VALUES(?,?,?,?,0,?,?,?,?)`, t.Project, t.Title, t.Question, ThreadOpen, p.Label, p.ID, ts, ts)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO thread_homes(thread_id,room_key,anchor_message_id,created_at) VALUES(?,?,NULL,?)`, id, room, ts); err != nil {
		return 0, err
	}
	if l := t.Link; l != nil && strings.TrimSpace(l.Kind) != "" && strings.TrimSpace(l.ID) != "" {
		if l.Kind == "request" {
			if err := validateRequestLinkTx(tx, room, l.ID); err != nil {
				return 0, err
			}
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO thread_links(thread_id,object_kind,object_id,object_revision,created_at) VALUES(?,?,?,?,?)`, id, l.Kind, l.ID, l.Revision, ts); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}
