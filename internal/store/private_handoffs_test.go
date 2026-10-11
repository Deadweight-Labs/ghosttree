package store

import (
	"strconv"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const tsz = "2026-01-01T00:00:00Z"

// Request views: what hangs on a private session of another account (quote,
// handoff, evidence, activity, counts) is as private as the transcript, for
// members, leads and the project owner alike (REQ-438 D, review 2, 4, 5, 6).
func TestRequestViewsHideWhatHangsOnForeignPrivateSessions(t *testing.T) {
	f := newVisFixture(t) // f.b private (mia), f.c shared (rex)
	db := f.st.db
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO requests(id,type,title,description,state,project,origin,person,created_at,updated_at)
		VALUES(40,'feature','shared request','d','open',?,'agent','robin',?,?)`, roleProject, tsz, tsz)
	mustExec(`INSERT INTO request_work(request_id,session_id,role,state,started_at,ended_at,summary) VALUES
		(40,?,'primary','paused',?,'2026-01-02T00:00:00Z','REX-HANDOFF'),(40,?,'related','paused',?,'2026-01-03T00:00:00Z','MIA-HANDOFF')`, f.c.ID, tsz, f.b.ID, tsz)
	mustExec(`INSERT INTO request_sightings(request_id,session_id,chunk_seq,quote) VALUES(40,?,1,'REX-QUOTE'),(40,?,2,'MIA-QUOTE')`, f.c.ID, f.b.ID)
	mustExec(`INSERT INTO request_criteria(id,request_id,number,description,state,created_at,updated_at) VALUES(41,40,1,'AC','met',?,?)`, tsz, tsz)
	mustExec(`INSERT INTO request_evidence(request_id,criterion_id,kind,ref,person,created_at) VALUES(40,41,'session',?,'mia',?)`, "session:"+strconv.FormatInt(f.b.ID, 10)+"#4", tsz)
	mustExec(`INSERT INTO request_activity(request_id,kind,person,data,created_at,session_id) VALUES(40,'work.finished','mia','MIA-HANDOFF',?,?)`, tsz, f.b.ID)

	// Legacy rows without a session id (0 or -1) still carry a session reference.
	mustExec(`INSERT INTO request_activity(request_id,kind,person,data,created_at,session_id) VALUES
		(40,'work.finished','mia','LEGACY-HANDOFF',?,-1),(40,'work.started','mia',?,?,0)`, tsz, "session:"+strconv.FormatInt(f.b.ID, 10)+" role:related", tsz)

	detail, err := f.st.RequestByID(40)
	if err != nil {
		t.Fatal(err)
	}
	dump := func(d requestdomain.Detail) string {
		var b strings.Builder
		for _, w := range d.Work {
			b.WriteString(w.Summary + "|")
		}
		for _, s := range d.Sightings {
			b.WriteString(s.Quote + "|")
		}
		for _, c := range d.Criteria {
			for _, e := range c.Evidence {
				b.WriteString(e.Ref + "|")
			}
		}
		for _, a := range d.Activity {
			b.WriteString(a.Data + "|")
		}
		return b.String()
	}
	for _, c := range []struct{ who, label string }{{"person:1", "robin"}, {"person:2", "lena"}, {"person:4", "rex"}} {
		got := dump(viewer(f.st, c.who, c.label).RequestDetailView(detail))
		if strings.Contains(got, "MIA-") || strings.Contains(got, "LEGACY-") || strings.Contains(got, "session:"+strconv.FormatInt(f.b.ID, 10)) {
			t.Errorf("%s sees what hangs on mia's private session: %s", c.label, got)
		}
		if c.label != "rex" && !strings.Contains(got, "REX-HANDOFF") {
			t.Errorf("%s lost the handoff of the shared session: %s", c.label, got)
		}
	}
	if got := dump(viewer(f.st, "person:3", "mia").RequestDetailView(detail)); !strings.Contains(got, "MIA-HANDOFF") || !strings.Contains(got, "MIA-QUOTE") {
		t.Errorf("the owner of the session lost her own handoff and quote: %s", got)
	}

	page, err := f.st.SearchRequests(requestdomain.SearchFilter{Scope: scope.Axes{Project: roleProject}, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ who, label string }{{"person:1", "robin"}, {"person:2", "lena"}, {"person:4", "rex"}} {
		for _, h := range page.Results {
			if h.Request.ID != 40 {
				continue
			}
			v := viewer(f.st, c.who, c.label).RequestHitView(h)
			if strings.Contains(v.LatestHandoff, "MIA-") {
				t.Errorf("%s sees the handoff of a private session in the hit: %q", c.label, v.LatestHandoff)
			}
		}
	}

	// Interrupted work in the bootstrap follows the same rule.
	threads, err := f.st.InterruptedWorkFor(viewer(f.st, "person:4", "rex").Viewer(), scope.Axes{Project: roleProject}, "2026-12-01T00:00:00Z", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range threads {
		if strings.Contains(th.Handoff, "MIA-") {
			t.Errorf("interrupted work shows a private handoff: %q", th.Handoff)
		}
	}
	own, err := f.st.InterruptedWorkFor(viewer(f.st, "person:3", "mia").Viewer(), scope.Axes{Project: roleProject}, "2026-12-01T00:00:00Z", "", 5)
	if err != nil || len(own) == 0 {
		t.Errorf("the owner does not get her own interrupted work: %v %v", own, err)
	}
}

func TestSightingCountOfAHitCountsOnlyReadableSessions(t *testing.T) {
	f := newVisFixture(t)
	for _, q := range []string{
		`INSERT INTO requests(id,type,title,description,state,project,origin,person,created_at,updated_at) VALUES(50,'feature','wish','d','open','` + roleProject + `','distilled','robin','` + tsz + `','` + tsz + `')`,
	} {
		if _, err := f.st.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.st.db.Exec(`INSERT INTO request_sightings(request_id,session_id,chunk_seq,quote) VALUES(50,?,1,'a'),(50,?,1,'b')`, f.b.ID, f.c.ID); err != nil {
		t.Fatal(err)
	}
	h := requestdomain.SearchHit{Request: requestdomain.Request{ID: 50, Scope: scope.Axes{Project: roleProject}}, Sightings: 2}
	if got := viewer(f.st, "person:4", "rex").RequestHitView(h).Sightings; got != 1 {
		t.Errorf("sightings = %d, want only the readable session", got)
	}
}

func TestOpenRequestCountSkipsHeldRequestsOfOthers(t *testing.T) {
	f := newVisFixture(t)
	if _, err := f.st.db.Exec(`INSERT INTO requests(type,title,description,state,project,origin,person,session_ref,created_at,updated_at)
		VALUES('feature','held','d','open',?,'distilled','',?,?,?)`, roleProject, "session:"+strconv.FormatInt(f.b.ID, 10), tsz, tsz); err != nil {
		t.Fatal(err)
	}
	ax := scope.Axes{Project: roleProject}
	if n, _ := f.st.CountOpenRequestsFor(viewer(f.st, "person:4", "rex").Viewer(), ax); n != 0 {
		t.Errorf("rex counts %d, want the held request left out", n)
	}
	if n, _ := f.st.CountOpenRequestsFor(viewer(f.st, "person:3", "mia").Viewer(), ax); n != 1 {
		t.Errorf("mia counts %d, want her own held request", n)
	}
}

func TestAdminSearchIsNotRankedOverTheWholeIndex(t *testing.T) {
	f := newVisFixture(t)
	if pre := viewer(f.st, "person:1", "robin").TranscriptPrefilter(); pre.Unrestricted {
		t.Error("an instance admin must not get the unrestricted (bm25 over everything) search under enforcement")
	}
}

// Losing the role does not release anything: only the owner of the session can
// (REQ-438 D). Nobody else's action makes a held finding visible.
func TestLosingTheRoleKeepsHeldFindingsHeldUntilTheOwnerReturns(t *testing.T) {
	f := newVisFixture(t)
	ref := "session:" + strconv.FormatInt(f.b.ID, 10) + "#1"
	res, err := f.st.db.Exec(`INSERT INTO knowledge(type,title,body,project,confidence,status,origin,session_ref,created_at,updated_at)
		VALUES('note','orphan','b',?,'quarantined','active','distilled',?,?,?)`, roleProject, ref, tsz, tsz)
	if err != nil {
		t.Fatal(err)
	}
	kid, _ := res.LastInsertId()
	rres, err := f.st.db.Exec(`INSERT INTO requests(type,title,description,state,project,origin,person,session_ref,created_at,updated_at)
		VALUES('feature','orphan wish','d','open',?,'distilled','',?,?,?)`, roleProject, "session:"+strconv.FormatInt(f.b.ID, 10), tsz, tsz)
	if err != nil {
		t.Fatal(err)
	}
	rid, _ := rres.LastInsertId()
	if err := f.st.RemoveProjectRole("person:1", roleProject, "person:3", RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	k, _ := f.st.KnowledgeByID(kid)
	if k.Confidence != "quarantined" {
		t.Errorf("confidence %q after the owner lost the role, want it untouched", k.Confidence)
	}
	for _, c := range []struct{ who, label string }{{"person:1", "robin"}, {"person:2", "lena"}, {"person:4", "rex"}} {
		if viewer(f.st, c.who, c.label).CanSeeKnowledge(k) {
			t.Errorf("%s sees the held finding after the owner lost the role", c.label)
		}
	}
	var person string
	_ = f.st.db.QueryRow(`SELECT person FROM requests WHERE id=?`, rid).Scan(&person)
	if person != "" {
		t.Errorf("held request was rewritten to %q", person)
	}
	// Back in the project, the owner sees it and can release it.
	if err := f.st.SetProjectRole("person:1", roleProject, "person:3", RoleMember, false, RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	mia := viewer(f.st, "person:3", "mia")
	if !mia.CanSeeKnowledge(k) || !mia.CanReleaseKnowledge(k) {
		t.Error("the returning owner cannot see or release her finding")
	}
}
