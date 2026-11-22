package web

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// legacyGuestSend stores a message as a guest wrote it before a guest lost the
// right to write in a project room (room.post is member and up): such rows stay
// in the database, and what a guest reads of them has to keep its rules (no
// oracle for who is in the room, only the typed mentions). The store refuses a
// new guest post, so the row is written below the gate, with the mentions the
// guest typed (typed) kept as the guest path kept them.
func legacyGuestSend(t *testing.T, st *store.Store, principal, agent string, m store.CoordMessage, typed ...string) int64 {
	t.Helper()
	m.SenderExternalID, m.AuthorPrincipalID = agent, principal
	if m.AuthorKind == "" {
		m.AuthorKind = store.AuthorAgent
	}
	id, err := st.AppendCoordMessage(m)
	if err != nil {
		t.Fatal(err)
	}
	// The role of the sender as it was recorded when the message was written.
	if _, err := st.DB().Exec(`INSERT OR IGNORE INTO coord_message_sender_roles(message_id, role) VALUES(?,?)`, id, "guest"); err != nil {
		t.Fatal(err)
	}
	for _, mention := range typed {
		if _, err := st.DB().Exec(`INSERT OR IGNORE INTO coord_message_raw_mentions(message_id,mentioned_external_id) VALUES(?,?)`, id, mention); err != nil {
			t.Fatal(err)
		}
	}
	return id
}
