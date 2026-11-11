package store

import (
	"sync"
)

// actorSessions remembers, for one request, which sessions behind actor
// stamps the viewer may see as metadata (CanSeeSessionMeta). A list or a
// statistic asks for the same sessions many times.
type actorSessions struct {
	mu   sync.Mutex
	seen map[int64]*Session // nil: not visible or unknown
}

// SessionsByIDs reads sessions by row id; unknown ids are missing.
func (s *Store) SessionsByIDs(ids []int64) (map[int64]Session, error) {
	if s.reader != nil {
		return s.reader.SessionsByIDs(ids)
	}
	out := map[int64]Session{}
	err := chunked(dedupe(ids), func(part []int64) error {
		rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions WHERE id IN (`+placeholders(len(part))+`)`, factIDArgs(part)...)
		if err != nil {
			return err
		}
		found, err := scanSessions(rows)
		if err != nil {
			return err
		}
		for _, sess := range found {
			out[sess.ID] = sess
		}
		return nil
	})
	return out, err
}

// loadActorSessions decides visibility for the sessions not asked about yet.
func (a *ProjectAccess) loadActorSessions(ids []int64) {
	a.actorSess.mu.Lock()
	if a.actorSess.seen == nil {
		a.actorSess.seen = map[int64]*Session{}
	}
	var missing []int64
	for _, id := range ids {
		if _, ok := a.actorSess.seen[id]; !ok && id > 0 {
			missing = append(missing, id)
		}
	}
	a.actorSess.mu.Unlock()
	if len(missing) == 0 {
		return
	}
	found, err := a.st.SessionsByIDs(missing)
	if err != nil {
		found = nil
	}
	a.actorSess.mu.Lock()
	defer a.actorSess.mu.Unlock()
	for _, id := range missing {
		sess, ok := found[id]
		if ok && a.CanSeeSessionMeta(sess) {
			s := sess
			a.actorSess.seen[id] = &s
		} else {
			a.actorSess.seen[id] = nil
		}
	}
}

// actorSession is the session behind a stamp if the viewer may see its
// metadata.
func (a *ProjectAccess) actorSession(id int64) (Session, bool) {
	if id <= 0 {
		return Session{}, false
	}
	a.loadActorSessions([]int64{id})
	a.actorSess.mu.Lock()
	defer a.actorSess.mu.Unlock()
	if s := a.actorSess.seen[id]; s != nil {
		return *s, true
	}
	return Session{}, false
}

// ActorView leaves of a stamp what this viewer may see (REQ-620). Harness and
// kind are what the page shows anyway. Model and session only when the viewer
// may see the session's metadata (CanSeeSessionMeta: a private session only
// its owner); the machine then as the viewer's view of that session shows it
// (no machine for a guest). A machine without session (from a device token)
// only for the account that wrote it. ok is true when the session stays, and
// sess is that session for further checks (transcript link).
func (a *ProjectAccess) ActorView(ea EntryActor) (view EntryActor, sess Session, ok bool) {
	view = ea
	view.Model, view.Machine, view.SessionID, view.SessionRef = "", "", 0, ""
	if ea.SessionID > 0 {
		if s, visible := a.actorSession(ea.SessionID); visible {
			view.SessionID, view.Model = ea.SessionID, ea.Model
			view.Machine = a.SessionView(s).Scope.Machine
			return view, s, true
		}
		return view, Session{}, false
	}
	if a.valid && ea.AccountID == a.account {
		view.Machine = ea.Machine
	}
	return view, Session{}, false
}

// actorModelVisible says whether the model of a stamp from this session may
// count for the viewer (search model:, statistics).
func (a *ProjectAccess) actorModelVisible(sessionID int64) bool {
	_, ok := a.actorSession(sessionID)
	return ok
}
