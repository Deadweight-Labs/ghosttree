package store

import (
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// ActiveRequestWork is one session working on a request right now, as far as
// the viewer may read it: its page address and title.
type ActiveRequestWork struct {
	SessionPublicID string
	Title           string
}

// ActiveWorkOnRequests maps request IDs to the sessions that work on them at
// the moment, one query for a whole page instead of one lookup per request. A
// session shows only where the viewer may read the transcript and only by its
// public address, the same rule RequestDetailView applies.
func (s *Store) ActiveWorkOnRequests(pa *ProjectAccess, ids []int64) (map[int64][]ActiveRequestWork, error) {
	if s.reader != nil {
		return s.reader.ActiveWorkOnRequests(pa, ids)
	}
	out := map[int64][]ActiveRequestWork{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	owner := instanceOwnerID(s.db)
	rows, err := s.db.Query(`SELECT `+prefixCols("s.", sessionCols)+`, w.request_id
		FROM request_work w JOIN sessions s ON s.id=w.session_id
		WHERE w.state='active' AND w.request_id IN (`+placeholders(len(ids))+`) ORDER BY w.id`, args...)
	if err != nil {
		return nil, err
	}
	type hit struct {
		request int64
		sess    Session
	}
	var hits []hit
	for rows.Next() {
		var h hit
		if err := scanSession(rows, &h.sess, &h.request); err != nil {
			rows.Close()
			return nil, err
		}
		h.sess.AccountID = effectiveOwner(h.sess.AccountID, owner)
		hits = append(hits, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Rows are read first, then checked: the role check asks the database itself.
	for _, h := range hits {
		if h.sess.PublicID == "" || !pa.CanSeeTranscript(h.sess) {
			continue
		}
		out[h.request] = append(out[h.request], ActiveRequestWork{SessionPublicID: h.sess.PublicID, Title: strings.TrimSpace(h.sess.Title)})
	}
	return out, nil
}

// RequestPriorities lists the distinct priorities among the requests the
// filter's scope and visibility cover, whatever their state, so a filter menu
// does not change from one page of results to the next. Only requests the
// viewer may read count: the list says nothing about hidden ones.
func (s *Store) RequestPriorities(filter requestdomain.SearchFilter) ([]string, error) {
	if s.reader != nil {
		return s.reader.RequestPriorities(filter)
	}
	where, args := requestScopeWhere(filter)
	where = append(where, `r.priority<>''`)
	rows, err := s.db.Query(`SELECT DISTINCT r.priority FROM requests r WHERE `+strings.Join(where, ` AND `)+` ORDER BY r.priority LIMIT 50`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
