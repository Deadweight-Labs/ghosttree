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

// Ein Gast (Konto mit ausdrücklicher Rolle guest, Enforce an) sieht die
// Mitglieder des Projektraums nicht, also auch keinen Namen (Pitfall #2447:
// jede Anzeige pro Empfänger geht über guestViewForMessageTx). Im Logmodus
// gilt das alte Verhalten, siehe TestSenderDisplayNameLogMode.
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

func TestSenderDisplayNameLogMode(t *testing.T) {
	e := authorityFixture(t) // Enforce aus: Logmodus
	e.sendAndRead(t, e.human("person:1"), e.room, "a-dev", "log-mode")
	if got := nameOf(t, e, "a-dev", "log-mode"); got.SenderDisplayName != "robin" {
		t.Fatalf("log mode keeps the old behaviour (name visible): %+v", got)
	}
}

// Das Web zeigt einem Gast weder Kontonamen noch den Besitzernamen eines
// Agenten ohne display_name, sondern die ID. Mitglieder sehen die Namen.
func TestWebAuthorLabelsHiddenFromGuests(t *testing.T) {
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
	if _, err := e.st.db.Exec(`UPDATE coord_agents SET display_name='' WHERE external_id='a-ben'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.human("person:1").Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: e.room, ClientID: "w1", Body: "w1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.agent("a-ben").Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: e.room, ClientID: "w2", Body: "w2", ReplyTo: 1}); err != nil {
		t.Fatal(err)
	}
	labels := func(a CoordAccess) (human, agent, reply string) {
		_, ps, err := a.MessagePresentationWindow(DestinationRoom, e.room, MessageWindow{Mode: messageWindowLatest, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ps {
			switch p.Message.Body {
			case "w1":
				human = p.AuthorLabel
			case "w2":
				agent = p.AuthorLabel
				if p.Reply != nil {
					reply = p.Reply.Author
				}
			}
		}
		return
	}
	if h, a, r := labels(e.human("person:3")); h != "robin" || a != "ben" || r != "robin" {
		t.Fatalf("member labels: %q %q %q", h, a, r)
	}
	if h, a, r := labels(e.human("person:5")); h != "person:1" || a != "a-ben" || r != "person:1" {
		t.Fatalf("guest labels must be IDs: %q %q %q", h, a, r)
	}
}
