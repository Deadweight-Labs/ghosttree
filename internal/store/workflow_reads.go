package store

import "strconv"

// HeldFromMembers says whether an entry is kept from the members of its
// project: it waits in held_entries for a reviewer, or it is a distilled
// request or a distilled, quarantined knowledge entry from a private session
// nobody released. Its author or the owner of that session still reads it. A
// workflow run reads none of these (REQ-698): its inputs and tool calls stand
// on a page every member reads, and the account it acts for may be the one
// person who sees the entry.
func (s *Store) HeldFromMembers(kind string, id int64) bool {
	if s.reader != nil {
		return s.reader.HeldFromMembers(kind, id)
	}
	if _, ok := s.heldIndex()[kind][id]; ok {
		return true
	}
	switch kind {
	case HeldRequest:
		ref, err := s.RequestRef(RefRequest, id)
		if err != nil {
			return true
		}
		return ref.HeldBy != "" && s.privateOrigin(ref.HeldBy)
	case HeldKnowledge:
		k, err := s.KnowledgeByID(id)
		if err != nil {
			return true
		}
		ref := knowledgeHeldRef(k)
		return ref != "" && s.privateOrigin(ref)
	}
	return false
}

// privateOrigin says whether a session reference names a session its owner
// did not open to the members; a reference of another form, or to a session
// that cannot be read, counts as private.
func (s *Store) privateOrigin(ref string) bool {
	m := originSessionPattern.FindStringSubmatch(ref)
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
