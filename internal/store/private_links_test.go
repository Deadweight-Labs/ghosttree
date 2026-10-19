package store

import (
	"errors"
	"strconv"
	"testing"
)

// A thread cannot link a held request of another account's private session; the
// answer is the one for an unknown number (REQ-438 D, review 6). Also: what a
// private session works on stays out of the agent list (review 5).
func TestThreadLinkAndAgentWorkDoNotRevealHeldRequests(t *testing.T) {
	f := newVisFixture(t)
	res, err := f.st.db.Exec(`INSERT INTO requests(type,title,description,state,project,origin,person,session_ref,created_at,updated_at)
		VALUES('feature','held wish','d','open',?,'distilled','',?,?,?)`, roleProject, "session:"+strconv.FormatInt(f.b.ID, 10), tsz, tsz)
	if err != nil {
		t.Fatal(err)
	}
	rid, _ := res.LastInsertId()
	link := "REQ-" + strconv.FormatInt(rid, 10)

	room := RoomKeyForProject(roleProject)
	for _, c := range []struct {
		who, label, agent string
		wantHeld          bool
	}{{"person:4", "rex", "claude:rex-box:aaaa", true}, {"person:3", "mia", "claude:mia-box:bbbb", false}} {
		if _, err := f.st.RegisterCoordAgent(CoordAgent{ExternalID: c.agent, Provider: "claude", RoomKey: room, PrincipalID: c.who, DisplayName: c.agent}); err != nil {
			t.Fatal(err)
		}
		p := Principal{ID: c.who, Label: c.label}
		_, err := f.st.CoordinationFor(p, c.agent).CreateThread(Thread{Project: roleProject, Title: "t " + c.label, Link: &ThreadLink{Kind: "request", ID: link}})
		if c.wantHeld && !errors.Is(err, errLinkedRequestNotFound) {
			t.Errorf("%s linked a held request: %v", c.label, err)
		}
		if !c.wantHeld && err != nil {
			t.Errorf("%s cannot link her own held request: %v", c.label, err)
		}
	}

	// Agent list: the request a private session works on is its owner's business.
	if _, err := f.st.db.Exec(`INSERT INTO request_work(request_id,session_id,role,state,started_at) VALUES(?,?,'primary','active',?)`, rid, f.b.ID, tsz); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE requests SET person='robin' WHERE id=?`, rid); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE coord_agents SET session_id=? WHERE external_id='claude:mia-box:bbbb'`, f.b.ExternalID); err != nil {
		t.Fatal(err)
	}
	work, err := f.st.AgentWorkFor(viewer(f.st, "person:4", "rex"), []string{"claude:mia-box:bbbb"})
	if err != nil {
		t.Fatal(err)
	}
	if w := work["claude:mia-box:bbbb"]; w.RequestID != 0 || w.RequestTitle != "" {
		t.Errorf("rex sees what mia's private session works on: %+v", w)
	}
	work, _ = f.st.AgentWorkFor(viewer(f.st, "person:3", "mia"), []string{"claude:mia-box:bbbb"})
	if w := work["claude:mia-box:bbbb"]; w.RequestID != rid {
		t.Errorf("mia lost her own agent work: %+v", w)
	}
}
