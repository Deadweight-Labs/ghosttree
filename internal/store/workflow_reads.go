package store

import "strconv"

// HeldFromMembers says whether an entry is kept from the members of its
// project: it waits in held_entries for a reviewer, or it is a distilled
// request from a private session nobody released. Its author or the owner of
// that session still reads it. A workflow run reads none of these (REQ-698):
// its tool calls stand on a page every member reads, and the account it acts
// for may be the one person who sees the entry.
func (s *Store) HeldFromMembers(kind string, id int64) bool {
	if s.reader != nil {
		return s.reader.HeldFromMembers(kind, id)
	}
	if _, ok := s.heldIndex()[kind][id]; ok {
		return true
	}
	if kind != HeldRequest {
		return false
	}
	ref, err := s.RequestRef(RefRequest, id)
	if err != nil {
		return true
	}
	if ref.HeldBy == "" {
		return false
	}
	m := originSessionPattern.FindStringSubmatch(ref.HeldBy)
	if m == nil {
		return true
	}
	sid, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return true
	}
	sess, err := s.SessionByID(sid)
	return err != nil || !(sess.Shared || sess.Visibility == VisGuests)
}
