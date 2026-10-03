package store

import (
	"strconv"
	"testing"
)

// Globales Wissen hat kein Projekt; ob der Betrachter die Nummer kennen darf,
// richtet sich nach dem Projekt der Session, auf die es verweist.
func TestKnowledgeViewOfGlobalEntryFollowsSessionProject(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	shared := addSession(t, st, "shared", 4, roleProject, "box", VisGuests, userChunks("zebra"))
	hidden := addSession(t, st, "hidden", 3, roleProject, "box", "", userChunks("zebra"))
	ref := func(s Session) string { return "session:" + strconv.FormatInt(s.ID, 10) + "#2" }

	for _, who := range []struct{ id, label string }{{"person:3", "mia"}, {"person:1", "robin"}} {
		pa := viewer(st, who.id, who.label)
		for _, s := range []Session{shared, hidden} {
			if got := pa.KnowledgeView(Knowledge{SessionRef: ref(s)}).SessionRef; got != ref(s) {
				t.Errorf("%s lost the number of session %d: %q", who.label, s.ID, got)
			}
		}
	}
	gus := viewer(st, "person:5", "gus")
	if got := gus.KnowledgeView(Knowledge{SessionRef: ref(shared)}).SessionRef; got != "session:"+shared.PublicID+"#2" {
		t.Errorf("guest, readable session: %q", got)
	}
	if got := gus.KnowledgeView(Knowledge{SessionRef: ref(hidden)}).SessionRef; got != "" {
		t.Errorf("guest, hidden session: %q", got)
	}
	if got := gus.KnowledgeView(Knowledge{SessionRef: "session:99999#2"}).SessionRef; got != "" {
		t.Errorf("guest, missing session: %q", got)
	}
}
