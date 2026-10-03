package store

import (
	"errors"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func fixRequest(t *testing.T, st *Store, project string) int64 {
	t.Helper()
	c, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "feature", Title: "Title in " + project, Scope: scope.Axes{Project: project}}})
	if err != nil {
		t.Fatal(err)
	}
	return c.Request.ID
}

func threadCount(t *testing.T, st *Store) int {
	t.Helper()
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM threads`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMigrationLeavesRestrictedThreadsHomeless(t *testing.T) {
	st := accessFixture(t)
	id, err := st.CreateThread(Thread{Project: roleProject, Title: "private legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO thread_visibility(thread_id,member_external_id) VALUES(?,?)`, id, "claude:host-l:cccc"); err != nil {
		t.Fatal(err)
	}
	if err := ensureThreadHomes(st.db); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM thread_homes WHERE thread_id=?`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("restricted thread got a home: n=%d err=%v", n, err)
	}
}

func TestVisibilityListNarrowsAccessEvenWithAHome(t *testing.T) {
	st := accessFixture(t)
	room := RoomKeyForProject(roleProject)
	registerRoleAgent(t, st, "claude:host-m:bbbb", "person:3", room, "member")
	registerRoleAgent(t, st, "claude:host-l:cccc", "person:2", room, "lead")
	id, err := st.CreateThread(Thread{Project: roleProject, Title: "private legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO thread_homes(thread_id,room_key,created_at) VALUES(?,?,'t0')`, id, room); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO thread_visibility(thread_id,member_external_id) VALUES(?,?)`, id, "claude:host-l:cccc"); err != nil {
		t.Fatal(err)
	}
	outsider := st.CoordinationFor(Principal{ID: "person:3", Label: "mia"}, "claude:host-m:bbbb")
	if _, err := outsider.Thread(id); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("project member read a restricted thread: %v", err)
	}
	listed, err := outsider.RoomThreads(room)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range listed {
		if it.Thread.ID == id {
			t.Fatalf("restricted thread listed to a non-member")
		}
	}
	insider := st.CoordinationFor(Principal{ID: "person:2", Label: "lena"}, "claude:host-l:cccc")
	if _, err := insider.Thread(id); err != nil {
		t.Fatalf("listed member rejected: %v", err)
	}
}

func TestRoomThreadsHideRequestsOfOtherProjects(t *testing.T) {
	st := accessFixture(t)
	room := RoomKeyForProject(roleProject)
	registerRoleAgent(t, st, "claude:host-m:bbbb", "person:3", room, "member")
	foreign := fixRequest(t, st, "github.com/other/secret")
	own := fixRequest(t, st, roleProject)
	mia := st.CoordinationFor(Principal{ID: "person:3", Label: "mia"}, "claude:host-m:bbbb")
	a, _ := mia.CreateThread(Thread{Project: roleProject, Title: "foreign link"})
	b, _ := mia.CreateThread(Thread{Project: roleProject, Title: "own link"})
	for id, rid := range map[int64]int64{a: foreign, b: own} {
		if _, err := st.db.Exec(`INSERT INTO thread_links(thread_id,object_kind,object_id,created_at) VALUES(?,?,?,'t0')`,
			id, "request", "REQ-"+itoa(rid)); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := mia.RoomThreads(room)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range listed {
		switch it.Thread.ID {
		case a:
			if it.RequestID != "" || it.RequestTitle != "" {
				t.Fatalf("foreign request shown: %+v", it)
			}
		case b:
			if it.RequestTitle == "" {
				t.Fatalf("own request hidden: %+v", it)
			}
		}
	}
}

func TestCreateThreadWithLinkIsAtomic(t *testing.T) {
	st := accessFixture(t)
	room := RoomKeyForProject(roleProject)
	registerRoleAgent(t, st, "claude:host-m:bbbb", "person:3", room, "member")
	mia := st.CoordinationFor(Principal{ID: "person:3", Label: "mia"}, "claude:host-m:bbbb")
	foreign := fixRequest(t, st, "github.com/other/secret")
	own := fixRequest(t, st, roleProject)
	for name, link := range map[string]ThreadLink{
		"unknown":       {Kind: "request", ID: "REQ-9999"},
		"foreign":       {Kind: "request", ID: "REQ-" + itoa(foreign)},
		"non-canonical": {Kind: "request", ID: itoa(own)},
	} {
		l := link
		if _, err := mia.CreateThread(Thread{Project: roleProject, Title: name, Link: &l}); err == nil {
			t.Fatalf("%s: want an error", name)
		}
		if n := threadCount(t, st); n != 0 {
			t.Fatalf("%s: a failed open left %d thread(s) behind", name, n)
		}
	}
	id, err := mia.CreateThread(Thread{Project: roleProject, Title: "ok", Link: &ThreadLink{Kind: "request", ID: "REQ-" + itoa(own)}})
	if err != nil {
		t.Fatal(err)
	}
	links, err := mia.ThreadLinks(id)
	if err != nil || len(links) != 1 || links[0].ID != "REQ-"+itoa(own) {
		t.Fatalf("links=%+v err=%v", links, err)
	}
}

func TestMigrationTrimsProjectKey(t *testing.T) {
	st := accessFixture(t)
	room := RoomKeyForProject(roleProject)
	registerRoleAgent(t, st, "claude:host-m:bbbb", "person:3", room, "member")
	if _, err := st.db.Exec(`INSERT INTO threads(project,title,question,state,archived,person,author_principal_id,created_at,updated_at)
		VALUES(?,?,?,?,0,'','','t0','t0')`, " "+roleProject+" ", "padded", "", ThreadOpen); err != nil {
		t.Fatal(err)
	}
	if err := ensureThreadHomes(st.db); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := st.db.QueryRow(`SELECT room_key FROM thread_homes`).Scan(&got); err != nil || got != room {
		t.Fatalf("home=%q err=%v want %q", got, err, room)
	}
}
