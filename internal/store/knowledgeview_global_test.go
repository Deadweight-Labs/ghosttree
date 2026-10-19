package store

import (
	"strconv"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
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

// Ein Admin ohne Rolle im Projekt der Session sieht die Nummer in globalem
// Wissen nicht: Admin ist keine Leseberechtigung für verweisende Einträge, und
// beim Anlegen wird die Nummer wie bei jedem anderen ohne Nummernsicht verworfen.
func TestAdminWithoutRoleGetsNoNumbersInGlobalKnowledge(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	foreign := addSession(t, st, "foreign", 3, "github.com/dw/unclaimed", "box", "", userChunks("zebra"))
	ref := "session:" + strconv.FormatInt(foreign.ID, 10) + "#2"
	robin := viewer(st, "person:1", "robin")
	if robin.SeesSessionNumbers("github.com/dw/unclaimed") {
		t.Fatal("fixture: admin has a role in the foreign project")
	}
	if got := robin.KnowledgeView(Knowledge{SessionRef: ref}).SessionRef; got == ref {
		t.Errorf("admin without role sees the number: %q", got)
	}
	k := Knowledge{SessionRef: ref, Scope: scope.Axes{Project: "github.com/dw/unclaimed"}}
	robin.DropWrittenSessionNumber(&k)
	if k.SessionRef != "" {
		t.Errorf("admin without role may write %q", k.SessionRef)
	}
	// Without enforcement nothing is hidden.
	st.SetAccessMode(AccessMode{})
	if got := viewer(st, "person:1", "robin").KnowledgeView(Knowledge{SessionRef: ref}).SessionRef; got != ref {
		t.Errorf("no enforcement: %q", got)
	}
}

// bm25 hängt vom ganzen Index ab; unter Durchsetzung ordnet auch der Admin nur
// aus der lesbaren Menge.
func TestAdminDoesNotReadAllUnderEnforcement(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	if viewer(st, "person:1", "robin").ReadsAll() {
		t.Error("admin reads all under enforcement")
	}
	st.SetAccessMode(AccessMode{})
	if !viewer(st, "person:1", "robin").ReadsAll() {
		t.Error("nobody is restricted without enforcement")
	}
}
