package store

import (
	"errors"
	"fmt"
	"strings"
)

// Limits of a new thread's text, the same on every path that opens one: the
// agent's own room and the role path below.
const (
	MaxThreadTitle    = 500
	MaxThreadQuestion = 16 << 10
)

// ValidateThreadText refuses a title or question over the limits.
func ValidateThreadText(t Thread) error {
	if len(t.Title) > MaxThreadTitle {
		return fmt.Errorf("%w: a thread title is at most %d bytes", ErrInvalidInput, MaxThreadTitle)
	}
	if len(t.Question) > MaxThreadQuestion {
		return fmt.Errorf("%w: a thread question is at most %d bytes", ErrInvalidInput, MaxThreadQuestion)
	}
	return nil
}

// CreateThreadByRole opens a thread in a project the caller has a role in
// (member and up) without the caller's agent being in that project's room. An
// agent belongs to exactly one project room, so a thread filed for another
// project (REQ-458) can neither register the agent there nor rely on a
// membership it does not have. The write right is the project role, as for a
// person in the browser. This is a deliberate departure from spec 7.5, which
// reserves the role path (roleOpensProjectRoom) to web sessions: an agent or
// device token of a member may open a thread in any project where the account
// is at least a member, because filing a problem in project X while working in
// Y is the point (REQ-458, decision #2581). Agents still do not read or post in
// that room through this path; the thread has its home in the project room, which is
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
	if err := ValidateThreadText(t); err != nil {
		return 0, err
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
			if (CoordAccess{Store: s, Principal: p}).requestHeldTx(tx, l.ID) {
				return 0, errLinkedRequestNotFound
			}
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO thread_links(thread_id,object_kind,object_id,object_revision,created_at) VALUES(?,?,?,?,?)`, id, l.Kind, l.ID, l.Revision, ts); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}
