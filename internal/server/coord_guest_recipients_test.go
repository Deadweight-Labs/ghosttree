package server

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func guestRoomFixture(t *testing.T, enforce bool) (*accessAPIFixture, string) {
	t.Helper()
	f := roomGateFixture(t, enforce)
	room := store.RoomKeyForProject(accProject)
	for agent, principal := range map[string]string{"claude:mia": "person:3", "claude:gus": "person:5", "claude:lena": "person:2"} {
		if _, err := f.st.RegisterCoordAgent(store.CoordAgent{ExternalID: agent, Provider: "claude", RoomKey: room, PrincipalID: principal, Person: strings.TrimPrefix(agent, "claude:")}); err != nil {
			t.Fatal(err)
		}
	}
	return f, room
}

func recipientIDs(t *testing.T, f *accessAPIFixture, id, label, agent string) []string {
	t.Helper()
	got, err := f.st.CoordinationFor(store.Principal{ID: id, Label: label}, agent).Recipients()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.PrincipalID)
	}
	return ids
}

func TestGuestGetsNoRecipientListFromProjectRoom(t *testing.T) {
	f, _ := guestRoomFixture(t, true)
	if got := recipientIDs(t, f, "person:3", "mia", "claude:mia"); len(got) == 0 {
		t.Fatalf("member must still see recipients: %v", got)
	}
	if got := recipientIDs(t, f, "person:5", "gus", "claude:gus"); len(got) != 0 {
		t.Errorf("guest gets the room's members as recipients: %v", got)
	}
}

func TestGuestMentionDoesNotRevealRoomMembership(t *testing.T) {
	f, room := guestRoomFixture(t, true)
	send := func(who string, mention string) (int, string) {
		return f.call(t, who, "POST", "/api/coord/messages", store.CoordMessage{
			DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:" + who,
			ClientID: "m-" + who + mention, Body: "hi", Mentions: []string{mention}})
	}
	inRoom, inBody := send("gus", "claude:mia")
	absent, absentBody := send("gus", "claude:nobody")
	if inRoom != absent || inBody != absentBody {
		t.Errorf("guest can tell members from non-members: %d %q vs %d %q", inRoom, inBody, absent, absentBody)
	}
	if inRoom < 400 || inRoom >= 500 {
		t.Errorf("guest mention should be refused with a 4xx, got %d", inRoom)
	}
	// Ohne Erwähnung schreibt der Gast weiter; ein Mitglied darf erwähnen.
	f.expect(t, "gus", 200, "POST", "/api/coord/messages", store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:gus", ClientID: "plain", Body: "hi"})
	if code, body := send("mia", "claude:lena"); code != 200 {
		t.Errorf("member mention: %d %s", code, body)
	}
}

func TestUnknownRecipientIsClientError(t *testing.T) {
	f, room := guestRoomFixture(t, true)
	code, body := f.call(t, "mia", "POST", "/api/coord/messages", store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:mia",
		ClientID: "unk", Body: "hi", Mentions: []string{"claude:nobody"}})
	if code != 400 {
		t.Errorf("unknown recipient: %d %s", code, body)
	}
	if strings.Contains(body, "nobody") {
		t.Errorf("error echoes the probed id: %s", body)
	}
}

func TestGuestRecipientGateLogModeKeepsOldBehaviour(t *testing.T) {
	f, room := guestRoomFixture(t, false)
	var buf bytes.Buffer
	f.st.SetAccessMode(store.AccessMode{Logger: slog.New(slog.NewTextHandler(&buf, nil))})
	if got := recipientIDs(t, f, "person:5", "gus", "claude:gus"); len(got) == 0 {
		t.Error("log mode must keep the recipient list")
	}
	f.expect(t, "gus", 200, "POST", "/api/coord/messages", store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:gus",
		ClientID: "log", Body: "hi", Mentions: []string{"claude:mia"}})
	if !strings.Contains(buf.String(), "access: would deny") {
		t.Errorf("expected a would-deny line, got %q", buf.String())
	}
}
