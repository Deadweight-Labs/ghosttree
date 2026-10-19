package store

import (
	"testing"
	"time"
)

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

// The waiting states are derived from messages to others; shown for an agent
// the viewer started but may not list, they would reveal whether the @-name
// they wrote is a real hidden member.
func TestOwnAgentsPresenceDoesNotRevealWhoTheyWaitFor(t *testing.T) {
	st := accessFixture(t)
	room := RoomKeyForProject(roleProject)
	registerRoleAgent(t, st, "claude:host-g:aaaa", "person:5", room, "guest")
	registerRoleAgent(t, st, "claude:host-m:bbbb", "person:3", room, "member")
	guest := st.CoordinationFor(Principal{ID: "person:5", Label: "nora"}, "claude:host-g:aaaa")
	for i, target := range []string{"claude:host-m:bbbb", "claude:host-ghost:zzzz"} {
		_, _ = guest.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room,
			ClientID: "q" + itoa(int64(i)), Body: "ping", Intent: IntentQuestion, Mentions: []string{target}})
	}
	got, err := st.OwnAgents("person:5")
	if err != nil || len(got) != 1 {
		t.Fatalf("own agents %+v err=%v", got, err)
	}
	if v := got[0].Presence.WorkState.Value; v == WorkWaitingPeer || v == WorkWaitingUser || v == WorkBlocked || got[0].Presence.Cycle != nil {
		t.Fatalf("own agent shows a wait: %+v", got[0].Presence)
	}
}

// With fresh tool activity the agent reads "working" whether or not the person
// it asked is a real member; a mask applied after the wait is derived would
// turn the first case into "unknown" and tell the two apart.
func TestOwnAgentsPresenceIsTheSameForRealAndMissingMentionTargets(t *testing.T) {
	state := func(target string) (string, string) {
		st := accessFixture(t)
		room := RoomKeyForProject(roleProject)
		const guestAgent = "claude:host-g:aaaa"
		registerRoleAgent(t, st, guestAgent, "person:5", room, "guest")
		registerRoleAgent(t, st, "claude:host-m:bbbb", "person:3", room, "member")
		now := time.Now().UTC().Format(time.RFC3339)
		seen := time.Now().UTC().Add(-20 * time.Second).Format(time.RFC3339)
		if _, err := st.db.Exec(`INSERT INTO sessions(harness,external_id,project,started_at,last_seen_at,account_id) VALUES('claude','sess-g',?,?,?,5)`, roleProject, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`UPDATE coord_agents SET session_id='sess-g' WHERE external_id=?`, guestAgent); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`INSERT INTO path_activity(project,session_external_id,tool,path,quality,at,account_id) VALUES(?,?,?,?,?,?,5)`, roleProject, "sess-g", "Edit", "a.go", "intent", seen); err != nil {
			t.Fatal(err)
		}
		guest := st.CoordinationFor(Principal{ID: "person:5", Label: "nora"}, guestAgent)
		_, _ = guest.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room,
			ClientID: "q", Body: "ping", Intent: IntentQuestion, Mentions: []string{target}})
		got, err := st.OwnAgents("person:5")
		if err != nil || len(got) != 1 {
			t.Fatalf("own agents %+v err=%v", got, err)
		}
		return got[0].Presence.WorkState.Value, got[0].Presence.Reachability.Value
	}
	realW, realR := state("claude:host-m:bbbb")
	ghostW, ghostR := state("claude:host-ghost:zzzz")
	if realW != ghostW || realR != ghostR {
		t.Fatalf("member target gives %s/%s, missing target %s/%s", realW, realR, ghostW, ghostR)
	}
	if realW != WorkWorking {
		t.Fatalf("fresh activity shows %q, want working", realW)
	}
}
