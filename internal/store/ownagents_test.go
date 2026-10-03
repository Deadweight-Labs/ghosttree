package store

import "testing"

// A guest has no right to list a project's agents, but the agent they started
// themselves is theirs to see; nobody else's shows up.
func TestOwnAgentsReturnsOnlyTheCallersAgents(t *testing.T) {
	st := accessFixture(t)
	room := RoomKeyForProject(roleProject)
	registerRoleAgent(t, st, "claude:host-g:aaaa", "person:5", room, "guest")
	registerRoleAgent(t, st, "claude:host-m:bbbb", "person:3", room, "member")
	got, err := st.OwnAgents("person:5")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ExternalID != "claude:host-g:aaaa" || got[0].RoomKey != room {
		t.Fatalf("guest sees %+v, want only their own agent", got)
	}
	if got, _ := st.OwnAgents("person:4"); len(got) != 0 {
		t.Fatalf("an account without agents sees %+v", got)
	}
	if got, _ := st.OwnAgents("not-a-person"); len(got) != 0 {
		t.Fatalf("a non-account principal sees %+v", got)
	}
}

// A thread opened through the API (MCP thread_open) must show up in its
// project's room, like one opened in the browser.
func TestAPIOpenedThreadIsListedInItsRoom(t *testing.T) {
	st := accessFixture(t)
	room := RoomKeyForProject(roleProject)
	registerRoleAgent(t, st, "claude:host-m:bbbb", "person:3", room, "member")
	mia := st.CoordinationFor(Principal{ID: "person:3", Label: "mia"}, "claude:host-m:bbbb")
	id, err := mia.CreateThread(Thread{Project: roleProject, Title: "Token strategy"})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := mia.RoomThreads(room)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Thread.ID != id {
		t.Fatalf("room lists %+v, want the new thread %d", listed, id)
	}
}

// Threads the API opened before they got a home are adopted by their project
// room when the store opens.
func TestOpeningTheStoreGivesHomelessThreadsTheirProjectRoom(t *testing.T) {
	st := accessFixture(t)
	room := RoomKeyForProject(roleProject)
	registerRoleAgent(t, st, "claude:host-m:bbbb", "person:3", room, "member")
	id, err := st.CreateThread(Thread{Project: roleProject, Title: "Old API thread"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureThreadHomes(st.db); err != nil {
		t.Fatal(err)
	}
	if err := ensureThreadHomes(st.db); err != nil {
		t.Fatalf("not idempotent: %v", err)
	}
	var got string
	if err := st.db.QueryRow(`SELECT room_key FROM thread_homes WHERE thread_id=?`, id).Scan(&got); err != nil || got != room {
		t.Fatalf("home = %q, %v; want %q", got, err, room)
	}
}
