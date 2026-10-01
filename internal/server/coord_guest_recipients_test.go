package server

import (
	"bytes"
	"encoding/json"
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

func attentionOf(t *testing.T, f *accessAPIFixture, id, label, agent string) []store.AttentionItem {
	t.Helper()
	items, err := f.st.CoordinationFor(store.Principal{ID: id, Label: label}, agent).Attention()
	if err != nil {
		t.Fatal(err)
	}
	return items
}

// Ein Gast darf erwähnen, aber die Antwort verrät nicht, wer im Raum ist:
// Mitglieder bekommen die Zustellung, Ziele außerhalb fallen stumm weg.
func TestGuestMentionDoesNotRevealRoomMembership(t *testing.T) {
	f, room := guestRoomFixture(t, true)
	// nora ist Org-Mitglied ohne Rolle: keine Mitgliedschaft im Raum.
	if _, err := f.st.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:outside", Provider: "claude", RoomKey: store.RoomKeyForProject(accOther), PrincipalID: "person:6", Person: "nora"}); err != nil {
		t.Fatal(err)
	}
	send := func(mention, id string) (int, string) {
		return f.call(t, "gus", "POST", "/api/coord/messages", store.CoordMessage{
			DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:gus",
			ClientID: id, Body: "please look " + id, Intent: store.IntentQuestion, Mentions: []string{mention}})
	}
	inCode, inBody := send("claude:mia", "in")
	outCode, outBody := send("claude:outside", "out")
	unkCode, unkBody := send("person:6", "unk")
	if inCode != 200 || outCode != inCode || unkCode != inCode {
		t.Fatalf("status differs: member=%d outsider=%d person=%d", inCode, outCode, unkCode)
	}
	// Der Körper trägt nur die Nachrichten-ID; ohne sie ist er identisch.
	strip := func(b string) string {
		return strings.TrimSpace(strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return -1
			}
			return r
		}, b))
	}
	if strip(inBody) != strip(outBody) || strip(inBody) != strip(unkBody) {
		t.Errorf("bodies differ: %q %q %q", inBody, outBody, unkBody)
	}
	// Nur das Mitglied bekommt eine Zustellung (Attention), als Bitte.
	items := attentionOf(t, f, "person:3", "mia", "claude:mia")
	if len(items) != 1 || !strings.Contains(items[0].Body, "in") {
		t.Fatalf("member attention: %+v", items)
	}
	for _, who := range []struct{ id, label, agent string }{{"person:6", "nora", "claude:outside"}, {"person:2", "lena", "claude:lena"}} {
		if got := attentionOf(t, f, who.id, who.label, who.agent); len(got) != 0 {
			t.Errorf("%s got attention from a guest mention: %+v", who.label, got)
		}
	}
	// Auch beim Absender erscheint nur das Mitglied als Empfänger.
	for _, it := range attentionOf(t, f, "person:5", "gus", "claude:gus") {
		if it.RecipientID != "claude:mia" {
			t.Errorf("attention item for a non-member: %+v", it)
		}
	}
	if len(items) == 1 {
		mentions, err := f.st.CoordinationFor(store.Principal{ID: "person:3", Label: "mia"}, "claude:mia").MessageMentions(items[0].MessageID)
		if err != nil || len(mentions) != 1 || mentions[0] != "claude:mia" {
			t.Errorf("stored mentions %v %v", mentions, err)
		}
	}
	// Die Nachrichten der abgewiesenen Ziele tragen keine Erwähnung.
	body := f.expect(t, "mia", 200, "GET", "/api/coord/messages?destination_id="+room+"&agent_external_id=claude:mia", nil)
	var msgs []store.CoordMessage
	if err := json.Unmarshal([]byte(body), &msgs); err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.SenderExternalID != "claude:gus" {
			continue
		}
		mentions, _ := f.st.CoordinationFor(store.Principal{ID: "person:3", Label: "mia"}, "claude:mia").MessageMentions(m.ID)
		isIn := strings.HasSuffix(m.Body, " in")
		if isIn && m.Authority != "request" {
			t.Errorf("guest directive reached the member: authority=%q", m.Authority)
		}
		if !isIn && len(mentions) != 0 {
			t.Errorf("message %q kept mentions %v", m.Body, mentions)
		}
	}
	// Ein Mitglied behält den Fehler für unbekannte Ziele und darf erwähnen.
	if code, body := f.call(t, "mia", "POST", "/api/coord/messages", store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:mia",
		ClientID: "mm", Body: "hi", Mentions: []string{"claude:lena"}}); code != 200 {
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
