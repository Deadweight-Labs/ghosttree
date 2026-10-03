package store

import (
	"errors"
	"testing"
)

func roleRoomFixture(t *testing.T) (*Store, string) {
	t.Helper()
	st := accessFixture(t)
	room := RoomKeyForProject(roleProject)
	if err := st.EnsureCoordRoom(CoordRoom{Key: room, Kind: RoomProject, Label: roleProject}); err != nil {
		t.Fatal(err)
	}
	return st, room
}

func webPrincipal(id, label string) Principal {
	return Principal{ID: id, Label: label, TokenKind: WebSessionKind}
}

func TestRoleMemberWritesInTheProjectRoomWithoutAnAgent(t *testing.T) {
	for name, enforce := range map[string]bool{"enforced": true, "log mode": false} {
		t.Run(name, func(t *testing.T) {
			st, room := roleRoomFixture(t)
			st.SetAccessMode(AccessMode{Enforce: enforce})
			for who, id := range map[string]string{"owner": "person:1", "lead": "person:2", "member": "person:3", "reviewer": "person:4"} {
				a := st.CoordinationFor(webPrincipal(id, who), "")
				if !a.CanPost(room) {
					t.Errorf("%s: CanPost false", who)
				}
				mid, err := a.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: who + "-1", Body: "hello from " + who})
				if err != nil || mid == 0 {
					t.Fatalf("%s Send: %d %v", who, mid, err)
				}
				tid, err := a.CreateThread(Thread{Project: roleProject, Title: "thread by " + who})
				if err != nil {
					t.Fatalf("%s CreateThread: %v", who, err)
				}
				if _, err := a.ThreadPost(tid, CoordMessage{ClientID: who + "-2", Body: "reply by " + who}); err != nil {
					t.Fatalf("%s ThreadPost: %v", who, err)
				}
			}
		})
	}
}

func TestRoleWriteNeedsAWebSessionAndMemberRank(t *testing.T) {
	st, room := roleRoomFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	msg := CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "x", Body: "nope"}
	guest := st.CoordinationFor(webPrincipal("person:5", "gus"), "")
	if guest.CanPost(room) {
		t.Error("guest CanPost")
	}
	_, guestErr := guest.Send(msg)
	if !errors.Is(guestErr, ErrCoordForbidden) {
		t.Errorf("guest Send: %v", guestErr)
	}
	// No org, no role: the answer is the one for a room that does not exist.
	stranger := st.CoordinationFor(webPrincipal("person:6", "nora"), "")
	_, strangerErr := stranger.Send(msg)
	missing := msg
	missing.DestinationID = "project:github.com/none/missing"
	_, missingErr := stranger.Send(missing)
	if !errors.Is(strangerErr, ErrCoordNotFound) || strangerErr.Error() != missingErr.Error() {
		t.Errorf("stranger %v, missing room %v", strangerErr, missingErr)
	}
	for _, kind := range []string{"personal", "legacy", "device", "paste", ""} {
		a := st.CoordinationFor(Principal{ID: "person:1", Label: "robin", TokenKind: kind}, "")
		if _, err := a.Send(msg); !errors.Is(err, ErrCoordForbidden) {
			t.Errorf("token kind %q: %v", kind, err)
		}
	}
	// The role gives no way into a direct room.
	group, err := st.CreateCoordGroup(GroupInput{Label: "private", Creator: "person:2", Members: []string{"person:2", "person:3"}})
	if err != nil {
		t.Fatal(err)
	}
	dm := group.Key
	owner := st.CoordinationFor(webPrincipal("person:1", "robin"), "")
	if _, err := owner.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: dm, ClientID: "d", Body: "x"}); !errors.Is(err, ErrCoordNotFound) {
		t.Errorf("owner into a group: %v", err)
	}
}

func TestRoleDoesNotOpenAPrivateThread(t *testing.T) {
	st, _ := roleRoomFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	tid, err := st.CreateThread(Thread{Project: roleProject, Title: "private question"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO thread_visibility(thread_id,member_external_id) VALUES(?,?)`, tid, "claude:somebody"); err != nil {
		t.Fatal(err)
	}
	for who, id := range map[string]string{"owner": "person:1", "member": "person:3"} {
		a := st.CoordinationFor(webPrincipal(id, who), "")
		if _, err := a.ThreadPost(tid, CoordMessage{ClientID: who, Body: "sneak"}); !errors.Is(err, ErrCoordNotFound) {
			t.Errorf("%s posts into a private thread: %v", who, err)
		}
	}
}
