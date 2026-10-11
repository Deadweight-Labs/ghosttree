package store

import (
	"strconv"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Distilled, not yet adopted knowledge and requests from a private session
// belong to the owner of that session until the owner adopts them (REQ-438 D).
func TestDistilledFindingsFromPrivateSessionsAreHeldForTheSessionOwner(t *testing.T) {
	f := newVisFixture(t) // f.b: private session of mia (person:3)
	ref := "session:" + strconv.FormatInt(f.b.ID, 10) + "#1"
	insertKnowledge := func(title, confidence, origin string) int64 {
		res, err := f.st.db.Exec(`INSERT INTO knowledge(type,title,body,project,confidence,status,origin,session_ref,created_at,updated_at)
			VALUES('note',?,'b',?,?,'active',?,?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, title, roleProject, confidence, origin, ref)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	kid := insertKnowledge("held finding", "quarantined", "distilled")
	aid := insertKnowledge("deliberate note", "staged", "agent")
	rres, err := f.st.db.Exec(`INSERT INTO requests(type,title,description,state,project,origin,person,session_ref,created_at,updated_at)
		VALUES('feature','held wish','d','open',?,'distilled','',?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, roleProject, "session:"+strconv.FormatInt(f.b.ID, 10))
	if err != nil {
		t.Fatal(err)
	}
	rid, _ := rres.LastInsertId()

	know := func(id int64) Knowledge {
		k, err := f.st.KnowledgeByID(id)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	reqs := func(who, label string) int {
		page, err := f.st.SearchRequests(viewer(f.st, who, label).RequestFilter(requestdomain.SearchFilter{Scope: scope.Axes{Project: roleProject}, Limit: 50}))
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, h := range page.Results {
			if h.Request.ID == rid {
				n++
			}
		}
		return n
	}
	others := []struct{ who, label string }{{"person:1", "robin"}, {"person:2", "lena"}, {"person:4", "rex"}}
	for _, c := range others {
		pa := viewer(f.st, c.who, c.label)
		if pa.CanSeeKnowledge(know(kid)) {
			t.Errorf("%s sees held knowledge of a private session of another account", c.label)
		}
		if err := pa.CheckKnowledge(know(kid), ActVerify); err == nil {
			t.Errorf("%s may act on held knowledge", c.label)
		}
		if !pa.CanSeeKnowledge(know(aid)) {
			t.Errorf("%s lost a deliberate agent note", c.label)
		}
		if reqs(c.who, c.label) != 0 {
			t.Errorf("%s finds the held request", c.label)
		}
	}
	mia := viewer(f.st, "person:3", "mia")
	if !mia.CanSeeKnowledge(know(kid)) || reqs("person:3", "mia") != 1 {
		t.Fatal("the owner of the session does not see her findings")
	}
	// The owner releases the finding to the reviewers; verifying is not hers.
	if err := mia.CheckKnowledge(know(kid), ActVerify); err == nil {
		t.Fatal("the owner of the session may verify her own finding")
	}
	if !mia.CanReleaseKnowledge(know(kid)) {
		t.Fatal("the owner cannot release her finding")
	}
	for _, c := range others {
		if viewer(f.st, c.who, c.label).CanReleaseKnowledge(know(kid)) {
			t.Errorf("%s may release a finding of another account", c.label)
		}
	}
	if err := f.st.UpdateKnowledgeBy(kid, map[string]string{"confidence": "staged", "confirmed_by": ""}, "mia"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.ClaimDistilledRequest(rid, RefRequest, "mia"); err != nil {
		t.Fatal(err)
	}
	for _, c := range others {
		if !viewer(f.st, c.who, c.label).CanSeeKnowledge(know(kid)) || reqs(c.who, c.label) != 1 {
			t.Errorf("%s does not see what mia adopted", c.label)
		}
	}
	// A later share lifts the hold as well.
	sid := insertKnowledge("shared later", "quarantined", "distilled")
	rex := viewer(f.st, "person:4", "rex")
	if rex.CanSeeKnowledge(know(sid)) {
		t.Fatal("held before sharing")
	}
	if err := f.st.SetSessionVisibility(f.b.ID, mia, VisProject); err != nil {
		t.Fatal(err)
	}
	if !rex.CanSeeKnowledge(know(sid)) {
		t.Error("sharing the session does not lift the hold")
	}
}
