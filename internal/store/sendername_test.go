package store

import "testing"

func nameOf(t *testing.T, e authorityEnv, reader, body string) CoordMessage {
	t.Helper()
	return e.read(t, reader, e.room, body)
}

func TestSenderDisplayNameForMembers(t *testing.T) {
	e := authorityFixture(t)
	e.sendAndRead(t, e.human("person:1"), e.room, "a-ben", "from-robin")
	m := nameOf(t, e, "a-ben", "from-robin")
	if m.SenderDisplayName != "robin" || m.AuthorKind != AuthorHuman {
		t.Fatalf("member must see the account name: %+v", m)
	}
	// Agenten tragen keinen Personennamen.
	e.sendAndRead(t, e.agent("a-anna"), e.room, "a-ben", "from-agent")
	if got := nameOf(t, e, "a-ben", "from-agent"); got.SenderDisplayName != "" {
		t.Fatalf("agent post must carry no display name: %+v", got)
	}
}

func TestSenderDisplayNameIsLive(t *testing.T) {
	e := authorityFixture(t)
	e.sendAndRead(t, e.human("person:1"), e.room, "a-ben", "live")
	if _, err := e.st.db.Exec(`UPDATE persons SET name='renamed' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if got := nameOf(t, e, "a-ben", "live"); got.SenderDisplayName != "renamed" {
		t.Fatalf("name must be read live: %+v", got)
	}
}

// Ein Gast (Konto ohne Projektrolle) sieht die Mitglieder des Projektraums
// nicht, also auch keinen Namen (Pitfall #2447: eine Anzeige pro Empfänger geht
// über guestViewForMessageTx).
func TestSenderDisplayNameHiddenFromGuests(t *testing.T) {
	e := authorityFixture(t)
	e.st.SetAccessMode(AccessMode{Enforce: true})
	var orgID int64
	if err := e.st.db.QueryRow(`SELECT MIN(id) FROM orgs`).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	code, _, err := e.st.CreateInvitation("person:1", orgID, "", OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.AcceptInvitation("person:5", code); err != nil {
		t.Fatal(err)
	}
	if err := setRole(e.st, "person:1", "person:5", RoleGuest, false); err != nil {
		t.Fatal(err)
	}
	e.sendAndRead(t, e.human("person:1"), e.room, "a-ben", "secret-name")
	// Im Logmodus gilt das alte Verhalten; ein Mitglied sieht den Namen immer.
	if got := nameOf(t, e, "a-ben", "secret-name"); got.SenderDisplayName != "robin" {
		t.Fatalf("member lost the name under enforcement: %+v", got)
	}
	if got := nameOf(t, e, "a-dev", "secret-name"); got.SenderDisplayName != "" || got.SenderExternalID == "" {
		t.Fatalf("guest must not get the name: %+v", got)
	}
	msgs, err := e.human("person:5").Messages(DestinationRoom, e.room, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.SenderDisplayName != "" {
			t.Fatalf("guest human reader got a name: %+v", m)
		}
	}
}

func TestSenderDisplayNameCannotBeForged(t *testing.T) {
	e := authorityFixture(t)
	got := e.sendMsgAndRead(t, e.agent("a-cleo"), CoordMessage{DestinationKind: DestinationRoom, DestinationID: e.room,
		ClientID: "fn", Body: "fn", SenderDisplayName: "Robin"}, "a-ben")
	if got.SenderDisplayName != "" {
		t.Fatalf("client-supplied name was kept: %+v", got)
	}
}
