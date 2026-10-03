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

const otherRoleProject = "github.com/dw/other"

// mentionFixture adds the lead's agent a-lena to the project room and an agent
// a-far to another project's room. Persons 1 (owner), 3 (member) and 4
// (reviewer) have no agent in the room.
func mentionFixture(t *testing.T) (*Store, string) {
	t.Helper()
	st, room := roleRoomFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	registerRoleAgent(t, st, "a-lena", "person:2", room, "lead")
	if _, err := st.EnsureProject("person:1", otherRoleProject); err != nil {
		t.Fatal(err)
	}
	registerRoleAgent(t, st, "a-far", "person:2", RoomKeyForProject(otherRoleProject), "lead")
	return st, room
}

func readAs(t *testing.T, st *Store, owner, agent, room, body string) CoordMessage {
	t.Helper()
	msgs, err := st.CoordinationFor(Principal{ID: owner}, agent).Messages(DestinationRoom, room, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Body == body {
			return m
		}
	}
	t.Fatalf("%q not readable by %s", body, agent)
	return CoordMessage{}
}

func TestRoleWriterMentionIsDeliveredWithTheAuthorityOfTheRole(t *testing.T) {
	st, room := mentionFixture(t)
	cases := []struct{ who, id, authority string }{
		{"owner", "person:1", AuthorityDirective},
		{"member", "person:3", AuthorityRequest},
		{"reviewer", "person:4", AuthorityRequest},
	}
	for _, c := range cases {
		a := st.CoordinationFor(webPrincipal(c.id, c.who), "")
		body := "ping from " + c.who
		if _, err := a.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: c.who, Body: body, Mentions: []string{"a-lena"}}); err != nil {
			t.Fatalf("%s mention: %v", c.who, err)
		}
		if got := readAs(t, st, "person:2", "a-lena", room, body); got.Authority != c.authority {
			t.Errorf("%s: authority %q, want %q", c.who, got.Authority, c.authority)
		}
	}
	// An actionable intent creates an attention item for the agent.
	owner := st.CoordinationFor(webPrincipal("person:1", "robin"), "")
	if _, err := owner.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "q1", Body: "question for lena", Intent: "question", Mentions: []string{"a-lena"}}); err != nil {
		t.Fatal(err)
	}
	items, err := st.CoordinationFor(Principal{ID: "person:2"}, "a-lena").Attention()
	if err != nil || len(items) == 0 {
		t.Fatalf("attention %v %v", items, err)
	}
}

func TestRoleWriterMentionBehavesLikeAMemberForUnknownTargets(t *testing.T) {
	st, room := mentionFixture(t)
	registerRoleAgent(t, st, "a-mia", "person:3", room, "member")
	byRole := st.CoordinationFor(webPrincipal("person:4", "rex"), "")
	byMember := st.CoordinationFor(Principal{ID: "person:3"}, "a-mia")
	for _, target := range []string{"a-far", "a-ghost"} {
		msg := CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, Body: "x", Mentions: []string{target}}
		msg.ClientID = "r-" + target
		_, roleErr := byRole.Send(msg)
		msg.ClientID = "m-" + target
		_, memberErr := byMember.Send(msg)
		if !errors.Is(roleErr, ErrCoordUnknownRecipient) || roleErr == nil || memberErr == nil || roleErr.Error() != memberErr.Error() {
			t.Errorf("%s: by role %v, by member %v", target, roleErr, memberErr)
		}
	}
	if items, err := st.CoordinationFor(Principal{ID: "person:2"}, "a-far").Attention(); err != nil || len(items) != 0 {
		t.Errorf("foreign agent got attention: %v %v", items, err)
	}
	// The guest is refused as before: forbidden, nothing stored.
	guest := st.CoordinationFor(webPrincipal("person:5", "gus"), "")
	if _, err := guest.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "g", Body: "x", Mentions: []string{"a-lena"}}); !errors.Is(err, ErrCoordForbidden) {
		t.Errorf("guest mention: %v", err)
	}
}

func TestRoleDoesNotOpenDirectMessagesToAgents(t *testing.T) {
	st, _ := mentionFixture(t)
	owner := st.CoordinationFor(webPrincipal("person:1", "robin"), "")
	members := []string{"person:1", "person:2"}
	err := owner.EnsureDirect(CoordRoom{Key: RoomKeyForDirect(members), Kind: RoomDirect, Members: members})
	if !errors.Is(err, ErrCoordUnknownRecipient) {
		t.Errorf("owner without agent opens a DM to the lead: %v", err)
	}
	if _, err := owner.CreateGroup(GroupInput{Label: "g", Creator: "person:1", Members: []string{"person:1", "person:2"}}); !errors.Is(err, ErrCoordUnknownRecipient) {
		t.Errorf("owner without agent opens a group with the lead: %v", err)
	}
}

func TestOnlyAuthorManagerOrLeadSetsAThreadState(t *testing.T) {
	st, _ := mentionFixture(t)
	author := st.CoordinationFor(webPrincipal("person:3", "mia"), "")
	tid, err := author.CreateThread(Thread{Project: roleProject, Title: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	for who, id := range map[string]string{"author": "person:3", "lead": "person:2", "owner": "person:1"} {
		a := st.CoordinationFor(webPrincipal(id, who), "")
		if !a.CanSetThreadState(tid) {
			t.Errorf("%s: CanSetThreadState false", who)
		}
		if err := a.SetThreadState(tid, ThreadResolved); err != nil {
			t.Errorf("%s resolves: %v", who, err)
		}
	}
	other := st.CoordinationFor(webPrincipal("person:4", "rex"), "")
	if other.CanSetThreadState(tid) {
		t.Error("another member may set the state")
	}
	if err := other.SetThreadState(tid, ThreadDeferred); !errors.Is(err, ErrCoordForbidden) {
		t.Errorf("another member defers: %v", err)
	}
	guest := st.CoordinationFor(webPrincipal("person:5", "gus"), "")
	if err := guest.SetThreadState(tid, ThreadDeferred); err == nil {
		t.Error("guest sets the state")
	}
}

func TestRoleMemberCannotEndALeadOrOwnerInstruction(t *testing.T) {
	st, room := mentionFixture(t)
	owner := st.CoordinationFor(webPrincipal("person:1", "robin"), "")
	member := st.CoordinationFor(webPrincipal("person:3", "mia"), "")
	lead := st.CoordinationFor(webPrincipal("person:2", "lena"), "")
	ids := map[string]string{}
	for i, who := range []struct {
		name string
		a    CoordAccess
	}{{"owner", owner}, {"lead", lead}, {"member", member}} {
		body := who.name + " rule"
		if _, err := who.a.CreateStanding(StandingInput{RoomKey: room, ClientID: "s" + who.name, Body: body}); err != nil {
			t.Fatalf("%s standing %d: %v", who.name, i, err)
		}
	}
	list, err := st.StandingInstructions(room)
	if err != nil || len(list) != 3 {
		t.Fatalf("standing %d %v", len(list), err)
	}
	for _, item := range list {
		ids[item.Body] = item.MessageID
	}
	if err := member.EndStanding(room, ids["owner rule"]); !errors.Is(err, ErrCoordForbidden) {
		t.Errorf("member ends an owner rule: %v", err)
	}
	if err := member.EndStanding(room, ids["lead rule"]); !errors.Is(err, ErrCoordForbidden) {
		t.Errorf("member ends a lead rule: %v", err)
	}
	if err := member.EndStanding(room, ids["member rule"]); err != nil {
		t.Errorf("member ends own rule: %v", err)
	}
	if err := owner.EndStanding(room, ids["lead rule"]); err != nil {
		t.Errorf("owner ends a lead rule: %v", err)
	}
}

func TestRoleChangesTakeEffectAtOnceAndStayInTheirProject(t *testing.T) {
	st, room := mentionFixture(t)
	other := RoomKeyForProject(otherRoleProject)
	msg := CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "r1", Body: "hello"}
	mia := st.CoordinationFor(webPrincipal("person:3", "mia"), "")
	if _, err := mia.Send(msg); err != nil {
		t.Fatal(err)
	}
	// Project A's role does not reach project B (answer: as for a missing room).
	b := msg
	b.DestinationID, b.ClientID = other, "r2"
	_, errOther := mia.Send(b)
	b.DestinationID, b.ClientID = "project:github.com/none/missing", "r3"
	_, errMissing := mia.Send(b)
	if !errors.Is(errOther, ErrCoordNotFound) || errOther.Error() != errMissing.Error() {
		t.Errorf("other project %v, missing room %v", errOther, errMissing)
	}
	// Demotion to guest closes writing at once.
	if err := st.SetProjectRole("person:1", roleProject, "person:3", RoleGuest, false, RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	msg.ClientID = "r4"
	if _, err := mia.Send(msg); !errors.Is(err, ErrCoordForbidden) || mia.CanPost(room) {
		t.Errorf("demoted member: %v", err)
	}
	// Removal closes it too, with the not-found answer of a stranger.
	if err := st.RemoveProjectRole("person:1", roleProject, "person:3", RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	msg.ClientID = "r5"
	if _, err := mia.Send(msg); !errors.Is(err, ErrCoordNotFound) {
		t.Errorf("removed member: %v", err)
	}
	// A web principal that acts as an agent is not the role path: it needs its own membership.
	registerRoleAgent(t, st, "a-rex", "person:4", other, "member")
	rex := st.CoordinationFor(webPrincipal("person:4", "rex"), "a-rex")
	msg.ClientID = "r6"
	if _, err := rex.Send(msg); !errors.Is(err, ErrCoordForbidden) {
		t.Errorf("agent without membership in the room: %v", err)
	}
}

func TestPrivateThreadWithTheHumanOnTheList(t *testing.T) {
	st, _ := mentionFixture(t)
	tid, err := st.CreateThread(Thread{Project: roleProject, Title: "private"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO thread_visibility(thread_id,member_external_id) VALUES(?,?)`, tid, "person:3"); err != nil {
		t.Fatal(err)
	}
	mia := st.CoordinationFor(webPrincipal("person:3", "mia"), "")
	if _, err := mia.ThreadPost(tid, CoordMessage{ClientID: "p1", Body: "listed"}); err != nil {
		t.Errorf("listed human posts: %v", err)
	}
	rex := st.CoordinationFor(webPrincipal("person:4", "rex"), "")
	if _, err := rex.ThreadPost(tid, CoordMessage{ClientID: "p2", Body: "unlisted"}); !errors.Is(err, ErrCoordNotFound) {
		t.Errorf("unlisted role holder: %v", err)
	}
}
