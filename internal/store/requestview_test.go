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

func TestRequestViewKeepsRealIDsForThoseWhoMayWork(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	mine1 := addSession(t, st, "mine1", 3, roleProject, "box", "", userChunks("zebra"))
	mine2 := addSession(t, st, "mine2", 3, roleProject, "box", "", userChunks("zebra"))
	others := []Session{addSession(t, st, "other1", 4, roleProject, "box", "", userChunks("zebra")), addSession(t, st, "other2", 4, roleProject, "box", "", userChunks("zebra"))}
	var works []requestdomain.Work
	for i, title := range []string{"first", "second"} {
		mine := []Session{mine1, mine2}[i]
		c, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: title}})
		if err != nil {
			t.Fatal(err)
		}
		// Another account's work comes first so the numbers have a gap.
		if _, _, err := st.StartRequestWork(c.Request.ID, others[i].ID, "primary", "rex"); err != nil {
			t.Fatal(err)
		}
		w, _, err := st.StartRequestWork(c.Request.ID, mine.ID, "primary", "mia")
		if err != nil {
			t.Fatal(err)
		}
		works = append(works, w)
	}
	last := works[1]
	d, err := st.RequestByID(last.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	mia := viewer(st, "person:3", "mia")
	got := mia.RequestDetailView(d)
	if len(got.Work) != 1 || got.Work[0].ID != last.ID {
		t.Fatalf("work = %+v, want the real id %d", got.Work, last.ID)
	}
	if got.Work[0].SessionID != 0 {
		t.Errorf("session number leaked: %+v", got.Work[0])
	}
	for _, a := range got.Activity {
		if a.ID <= 0 {
			t.Errorf("activity id %d", a.ID)
		}
	}
	if len(d.Activity) > 0 && got.Activity[0].ID != d.Activity[0].ID {
		t.Errorf("activity renumbered for a viewer who may work: %d != %d", got.Activity[0].ID, d.Activity[0].ID)
	}
	fin, err := st.FinishRequestWork(got.Work[0].ID, "completed", "done", "mia")
	if err != nil || fin.RequestID != last.RequestID || fin.SessionID != mine2.ID {
		t.Fatalf("finish hit %+v, %v", fin, err)
	}
}

func TestActivitySessionBackfillSkipsAmbiguousAndRunsOnce(t *testing.T) {
	st := accessFixture(t)
	a := addSession(t, st, "a", 3, roleProject, "box", "", nil)
	b := addSession(t, st, "b", 3, roleProject, "box", "", nil)
	c, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "t", Scope: scope.Axes{Project: roleProject}}})
	if err != nil {
		t.Fatal(err)
	}
	rid := c.Request.ID
	for _, sid := range []int64{a.ID, b.ID} {
		if _, err := st.db.Exec(`INSERT INTO request_work(request_id,session_id,role,state,started_at,ended_at,summary) VALUES(?,?,'primary','paused','t0','t1','same')`, rid, sid); err != nil {
			t.Fatal(err)
		}
	}
	solo, err := st.db.Exec(`INSERT INTO request_work(request_id,session_id,role,state,started_at,ended_at,summary) VALUES(?,?,'related','paused','t0','t2','solo')`, rid, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = solo
	for _, row := range [][2]string{{"t1", "same"}, {"t2", "solo"}} {
		if _, err := st.db.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at,session_id) VALUES(?,'work.finished','x',?,?,0)`, rid, row[1], row[0]); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureActivitySession(st.db); err != nil {
		t.Fatal(err)
	}
	read := func(data string) int64 {
		var sid int64
		if err := st.db.QueryRow(`SELECT session_id FROM request_activity WHERE request_id=? AND kind='work.finished' AND data=?`, rid, data).Scan(&sid); err != nil {
			t.Fatal(err)
		}
		return sid
	}
	if got := read("same"); got > 0 {
		t.Errorf("ambiguous finish attributed to session %d", got)
	}
	if got := read("solo"); got != a.ID {
		t.Errorf("unique finish = %d, want %d", got, a.ID)
	}
	var rest int
	if err := st.db.QueryRow(`SELECT count(*) FROM request_activity WHERE session_id=0 AND kind='work.finished'`).Scan(&rest); err != nil {
		t.Fatal(err)
	}
	if rest != 0 {
		t.Errorf("%d unresolved rows would be rewritten at every start", rest)
	}
}

func TestRestrictedViewDropsSessionEvidenceWithForeignRef(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	shared := addSession(t, st, "shared", 4, roleProject, "box", VisGuests, userChunks("zebra"))
	c, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "t", Scope: scope.Axes{Project: roleProject}}, Criteria: []string{"c"}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.RequestByID(c.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	d.Criteria[0].Evidence = []requestdomain.Evidence{
		{ID: 7, Kind: "session", Ref: "session:" + strconv.FormatInt(shared.ID, 10) + "#3"},
		{ID: 8, Kind: "session", Ref: "see session " + strconv.FormatInt(shared.ID, 10)},
		{ID: 9, Kind: "url", Ref: "https://example.com"},
	}
	got := viewer(st, "person:5", "gus").RequestDetailView(d).Criteria[0].Evidence
	if len(got) != 2 || got[0].Ref != "session:"+shared.PublicID+"#3" || got[1].Kind != "url" {
		t.Errorf("evidence = %+v", got)
	}
}
