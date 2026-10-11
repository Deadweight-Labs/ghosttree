package store

import (
	"strconv"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Authorship is the account id. A person who renames themselves to the label of
// someone else's legacy entries ("Claude", "admin", the author's old name) takes
// nothing over (security review of PR #101, finding 1).
func TestRenamingAnAccountDoesNotTakeOverGlobalEntries(t *testing.T) {
	f := newGlobalFixture(t)
	f.st.SetAccessMode(AccessMode{Enforce: true})
	for _, name := range []string{"Claude", "claude", "admin", "ROBIN", "robin-x"} {
		if _, err := f.st.db.Exec(`UPDATE persons SET name=? WHERE id=6`, name); err != nil {
			t.Fatal(err)
		}
		nora := viewer(f.st, "person:6", name)
		for _, k := range []Knowledge{f.robinK, f.legacyK, f.miaK} {
			if nora.CanSeeKnowledge(k) {
				t.Errorf("an account renamed to %q sees %q", name, k.Title)
			}
		}
		page, err := f.st.SearchRequests(nora.RequestFilter(requestdomain.SearchFilter{Limit: 25}))
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range page.Results {
			if h.Request.ID == f.robinR || h.Request.ID == f.legacyR || h.Request.ID == f.miaR {
				t.Errorf("an account renamed to %q lists request %d", name, h.Request.ID)
			}
		}
		for _, id := range []int64{f.robinR, f.legacyR} {
			ref, _ := f.st.RequestRef(RefRequest, id)
			obj, _ := nora.RequestObject(ref)
			if nora.Check(ref.Project, ResRequest, ActRead, obj) == nil {
				t.Errorf("an account renamed to %q reads request %d", name, id)
			}
		}
	}
}

// The one-time backfill gives legacy entries without an author row their account:
// the current name, then an earlier name (case-insensitive); an ambiguous name
// leaves the entry without an owner (only the instance owner sees it); an unknown
// or empty label and a distillate without an origin session belong to the instance
// owner; a distillate belongs to the owner of its session. It runs once.
func TestAuthorBackfillAssignsLegacyEntriesOnceByAccount(t *testing.T) {
	st := accessFixture(t) // robin 1, lena 2, mia 3, rex 4, gus 5, nora 6
	st.SetAccessMode(AccessMode{Enforce: true})
	if _, err := st.db.Exec(`INSERT INTO person_name_history(person_id, old_name, old_key, new_name, source, created_at) VALUES
		(2,'lenny','lenny','lena','test','2026-01-01T00:00:00Z'),
		(2,'dup','dup','lena','test','2026-01-01T00:00:00Z'),
		(3,'dup','dup','mia','test','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	sess := addSession(t, st, "orig", 4, roleProject, "box", "", userChunks("zebra"))
	ref := "session:" + strconv.FormatInt(sess.ID, 10) + "#1"
	legacy := func(person, origin, sessionRef string) (kid, rid int64) {
		t.Helper()
		res, err := st.db.Exec(`INSERT INTO knowledge(type,title,body,confidence,status,origin,person,session_ref,created_at,updated_at) VALUES('note',?,'b','trusted','active',?,?,?,'x','x')`, "k-"+person+origin, origin, person, sessionRef)
		if err != nil {
			t.Fatal(err)
		}
		kid, _ = res.LastInsertId()
		res, err = st.db.Exec(`INSERT INTO requests(type,title,description,origin,person,session_ref,created_at,updated_at) VALUES('feature',?,'d',?,?,?,'x','x')`, "r-"+person+origin, origin, person, sessionRef)
		if err != nil {
			t.Fatal(err)
		}
		rid, _ = res.LastInsertId()
		return kid, rid
	}
	cases := []struct {
		name                       string
		person, origin, sessionRef string
		want                       int64
	}{
		{"current name", "mia", "agent", "", 3},
		{"other case", "MIA", "agent", "", 3},
		{"earlier name", "Lenny", "agent", "", 2},
		{"ambiguous earlier name: nobody", "dup", "agent", "", 0},
		{"label of no account", "claude", "agent", "", 1},
		{"empty label", "", "agent", "", 1},
		{"distillate: the owner of its session", "", "distilled", ref, 4},
		{"distillate of a session that is gone", "", "distilled", "session:99999#1", 1},
	}
	type ids struct{ k, r int64 }
	made := make([]ids, len(cases))
	for i, c := range cases {
		made[i].k, made[i].r = legacy(c.person, c.origin, c.sessionRef)
		// Entries written by the running server carry a row; legacy ones do not.
		_, _ = st.db.Exec(`DELETE FROM knowledge_authors WHERE knowledge_id=?`, made[i].k)
		_, _ = st.db.Exec(`DELETE FROM request_authors WHERE request_id=?`, made[i].r)
	}
	if _, err := st.db.Exec(`DELETE FROM org_state WHERE key=?`, authorsBackfillKey); err != nil {
		t.Fatal(err)
	}
	if err := backfillAuthors(st.db); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		if got := st.knowledgeAuthorID(made[i].k); got != c.want {
			t.Errorf("knowledge, %s: account %d, want %d", c.name, got, c.want)
		}
		if got := st.requestAuthorID(made[i].r); got != c.want {
			t.Errorf("request, %s: account %d, want %d", c.name, got, c.want)
		}
	}
	// Ownerless: only the instance owner sees it, nobody else, the one with the
	// ambiguous name included.
	ownerless, _ := st.KnowledgeByID(made[3].k)
	for _, v := range globalViewers {
		if got, want := viewer(st, v.id, v.label).CanSeeKnowledge(ownerless), v.id == "person:1"; got != want {
			t.Errorf("%s sees the entry without owner: %v", v.label, got)
		}
	}
	// Once: a row that is missing afterwards stays missing, nothing is redone.
	if _, err := st.db.Exec(`DELETE FROM knowledge_authors WHERE knowledge_id=?`, made[0].k); err != nil {
		t.Fatal(err)
	}
	if err := backfillAuthors(st.db); err != nil {
		t.Fatal(err)
	}
	if got := st.knowledgeAuthorID(made[0].k); got != 0 {
		t.Errorf("the backfill ran twice: %d", got)
	}
	// A reopened database does not run it again either.
	var marker string
	if err := st.db.QueryRow(`SELECT value FROM org_state WHERE key=?`, authorsBackfillKey).Scan(&marker); err != nil || marker == "" {
		t.Errorf("no marker: %v", err)
	}
}

// Entries written now carry the writer's account from the start, whatever the
// writer is called later.
func TestNewEntriesCarryTheWritersAccount(t *testing.T) {
	st := accessFixture(t)
	id, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "mine", Body: "b", Person: "mia", Scope: scope.Axes{}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "mine", Person: "mia"}})
	if err != nil {
		t.Fatal(err)
	}
	if st.knowledgeAuthorID(id) != 3 || st.requestAuthorID(d.Request.ID) != 3 {
		t.Errorf("authors: %d %d, want 3", st.knowledgeAuthorID(id), st.requestAuthorID(d.Request.ID))
	}
	// A distilled finding without an author belongs to the owner of its session,
	// so a global one does not become unreachable (finding 3).
	sess := addSession(t, st, "origin", 4, roleProject, "box", "", userChunks("zebra"))
	did, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "found", Body: "b", Origin: "distilled", SessionRef: "session:" + strconv.FormatInt(sess.ID, 10) + "#1"})
	if err != nil {
		t.Fatal(err)
	}
	if st.knowledgeAuthorID(did) != 4 {
		t.Errorf("distilled author: %d, want the session owner 4", st.knowledgeAuthorID(did))
	}
	// The server passes the account itself.
	id2, _ := st.InsertKnowledge(Knowledge{Type: "note", Title: "given", Body: "b", Person: "whatever", AuthorID: 4})
	if st.knowledgeAuthorID(id2) != 4 {
		t.Errorf("explicit author: %d", st.knowledgeAuthorID(id2))
	}
}
