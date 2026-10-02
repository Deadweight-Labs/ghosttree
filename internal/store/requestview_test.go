package store

import (
	"strconv"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestGuestRequestDetailCarriesNoSessionNumbers(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	shared := addSession(t, st, "shared", 4, roleProject, "box", VisGuests, userChunks("zebra"))
	hidden := addSession(t, st, "hidden", 3, roleProject, "box", "", userChunks("zebra"))
	created, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "feature", Title: "t", Scope: scope.Axes{Project: roleProject}, SessionRef: "session:" + strconv.FormatInt(hidden.ID, 10)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []Session{shared, hidden} {
		if _, _, err := st.StartRequestWork(created.Request.ID, s.ID, "primary", "rex"); err != nil {
			t.Fatal(err)
		}
	}
	d, err := st.RequestByID(created.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	d.Sightings = []requestdomain.Sighting{{SessionID: shared.ID, Quote: "a"}, {SessionID: hidden.ID, Quote: "secret"}}

	gus := viewer(st, "person:5", "gus")
	g := gus.RequestDetailView(d)
	if len(g.Work) != 1 || g.Work[0].SessionID != 0 || g.Work[0].SessionPublicID != shared.PublicID {
		t.Errorf("guest work = %+v, want only the shared session by public id", g.Work)
	}
	if len(g.Sightings) != 1 || g.Sightings[0].SessionID != 0 || g.Sightings[0].SessionPublicID != shared.PublicID {
		t.Errorf("guest sightings = %+v", g.Sightings)
	}
	if g.Request.SessionRef != "" {
		t.Errorf("session ref leaks: %q", g.Request.SessionRef)
	}
	for _, a := range g.Activity {
		for _, id := range []int64{shared.ID, hidden.ID} {
			if a.Data == "session:"+strconv.FormatInt(id, 10)+" role:primary" {
				t.Errorf("activity names a session number: %+v", a)
			}
		}
	}
	// A member still sees both numbers and gets the address of what she reads.
	mia := viewer(st, "person:3", "mia")
	m := mia.RequestDetailView(d)
	if len(m.Work) != 2 {
		t.Fatalf("member work = %+v", m.Work)
	}
	for _, w := range m.Work {
		if w.SessionID == 0 {
			t.Errorf("member lost the number: %+v", w)
		}
	}
	if hit := gus.RequestHitView(requestdomain.SearchHit{Request: created.Request, Sightings: 5}); hit.Sightings != 0 || hit.Request.SessionRef != "" {
		t.Errorf("guest hit = %+v", hit)
	}
}
