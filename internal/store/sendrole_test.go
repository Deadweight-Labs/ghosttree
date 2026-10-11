package store

import "testing"

// Die Rolle, die ein Leser neben einer Nachricht sieht, ist die zum Zeitpunkt
// des Sendens; die Autorität bleibt live (Spec 7.1).
func TestSenderRoleIsTheRoleAtSendTime(t *testing.T) {
	e := authorityFixture(t)
	anna := e.agent("a-anna")
	id, err := anna.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: e.room, ClientID: "sr1", Body: "before demotion"})
	if err != nil {
		t.Fatal(err)
	}
	if err := setRole(e.st, "person:1", "person:2", RoleMember, false); err != nil {
		t.Fatal(err)
	}
	read := func(reader CoordAccess) CoordMessage {
		msgs, err := reader.Messages(DestinationRoom, e.room, id-1, 1)
		if err != nil || len(msgs) != 1 || msgs[0].ID != id {
			t.Fatalf("read: %v %+v", err, msgs)
		}
		return msgs[0]
	}
	got := read(e.agent("a-ben"))
	if got.SenderRole != RoleLead {
		t.Fatalf("sender_role must stay the role at send time, got %q", got.SenderRole)
	}
	// Authority is computed live from the roles now.
	if got.Authority != AuthorityRequest {
		t.Fatalf("authority must follow the current roles, got %q", got.Authority)
	}
	if roles := e.st.SenderRolesInProject(e.project, []CoordMessage{got}); roles[0] != RoleLead {
		t.Fatalf("browser display must show the role at send time, got %q", roles[0])
	}
}

func TestOldMessagesWithoutStoredRoleFallBackToTheCurrentOne(t *testing.T) {
	e := authorityFixture(t)
	id, err := e.agent("a-anna").Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: e.room, ClientID: "sr2", Body: "old"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.db.Exec(`DELETE FROM coord_message_sender_roles WHERE message_id=?`, id); err != nil {
		t.Fatal(err)
	}
	msgs, _ := e.agent("a-ben").Messages(DestinationRoom, e.room, id-1, 1)
	if len(msgs) != 1 || msgs[0].SenderRole != RoleLead {
		t.Fatalf("fallback to the live role: %+v", msgs)
	}
}

func TestMessagesTellAnAgentWhatIsAddressedToIt(t *testing.T) {
	e := authorityFixture(t)
	owner := e.agent("a-owner")
	send := func(body string, mention ...string) int64 {
		id, err := owner.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: e.room, ClientID: "ty-" + body, Body: body, Mentions: mention})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	plain := send("plain")
	mine := send("for ben", "a-ben")
	other := send("for cleo", "a-cleo")
	byText := send("hey @a-ben again")
	got := map[int64]string{}
	msgs, err := e.agent("a-ben").Messages(DestinationRoom, e.room, plain-1, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		got[m.ID] = m.ToYou
	}
	if got[plain] != "" || got[other] != "" {
		t.Fatalf("not addressed to ben: plain=%q other=%q", got[plain], got[other])
	}
	if got[mine] != ToYouMention || got[byText] != ToYouMention {
		t.Fatalf("mentions of ben: %q %q", got[mine], got[byText])
	}
	// A direct message is addressed to the other member without any mention.
	members := []string{"a-ben", "a-owner"}
	key := RoomKeyForDirect(members)
	if err := owner.EnsureDirect(CoordRoom{Key: key, Kind: RoomDirect, Members: members}); err != nil {
		t.Fatal(err)
	}
	dm, err := owner.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: key, ClientID: "ty-dm", Body: "dm"})
	if err != nil {
		t.Fatal(err)
	}
	dms, _ := e.agent("a-ben").Messages(DestinationRoom, key, dm-1, 1)
	if len(dms) != 1 || dms[0].ToYou != ToYouDirect {
		t.Fatalf("dm: %+v", dms)
	}
	// The sender never sees their own message marked as addressed to them.
	own, _ := owner.Messages(DestinationRoom, key, dm-1, 1)
	if len(own) != 1 || own[0].ToYou != "" {
		t.Fatalf("own message: %+v", own)
	}
}
