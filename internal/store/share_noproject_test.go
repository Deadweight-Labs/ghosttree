package store

import (
	"errors"
	"testing"
)

// Eine Session ohne Projekt erreicht niemanden, den "mit Mitgliedern" meinte:
// Teilen wird abgelehnt statt still Erfolg zu melden (Abnahme 2026-10-11, Befund 2).
func TestSessionWithoutProjectCannotBeShared(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	sess := addSession(t, st, "no-project", 3, "", "box", "", userChunks("zebra"))
	mia := viewer(st, "person:3", "mia")
	for _, level := range []string{VisProject, VisGuests} {
		if err := st.SetSessionVisibility(sess.ID, mia, level); !errors.Is(err, ErrShareNeedsProject) {
			t.Errorf("level %s: %v, want ErrShareNeedsProject", level, err)
		}
	}
	if got, _ := st.SessionByID(sess.ID); got.Shared || got.Visibility == VisProject || got.Visibility == VisGuests {
		t.Errorf("the refused share changed the session: %+v", got)
	}
	if err := st.SetSessionVisibility(sess.ID, mia, VisPrivate); err != nil {
		t.Errorf("staying private: %v", err)
	}
	// With a project it still works.
	with := addSession(t, st, "with-project", 3, roleProject, "box", "", userChunks("zebra"))
	if err := st.SetSessionVisibility(with.ID, mia, VisProject); err != nil {
		t.Errorf("share with project: %v", err)
	}
}
