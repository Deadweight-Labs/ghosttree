package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
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

func guestJSON(t *testing.T, f *accessAPIFixture, who, path string, out any) {
	t.Helper()
	body := f.expect(t, who, 200, "GET", path, nil)
	if err := json.Unmarshal([]byte(body), out); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
}

// guestPostAnswers: gus versucht, eine Frage und eine Standing-Anweisung an
// `mention` zu schreiben. miaInRoom steuert, ob dieses Ziel wirklich im Raum
// ist. Ein Gast schreibt im Projektraum nichts (room.post ist member und höher);
// die Antwort darf nicht verraten, ob das Ziel im Raum ist.
func guestPostAnswers(t *testing.T, miaInRoom, inThread bool, mention string) (int, string, error) {
	t.Helper()
	f := roomGateFixture(t, true)
	room := store.RoomKeyForProject(accProject)
	other := store.RoomKeyForProject(accOther)
	for agent, principal := range map[string]string{"claude:gus": "person:5", "claude:lena": "person:2", "claude:robin": "person:1"} {
		if _, err := f.st.RegisterCoordAgent(store.CoordAgent{ExternalID: agent, Provider: "claude", RoomKey: room, PrincipalID: principal, Person: strings.TrimPrefix(agent, "claude:")}); err != nil {
			t.Fatal(err)
		}
	}
	miaRoom := room
	if !miaInRoom {
		miaRoom = other
	}
	if _, err := f.st.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:mia", Provider: "claude", RoomKey: miaRoom, PrincipalID: "person:3", Person: "mia"}); err != nil {
		t.Fatal(err)
	}
	kind, dest := store.DestinationRoom, room
	if inThread {
		// Ein Alt-Thread ohne thread_homes-Zeile: der Raum ergibt sich nur aus dem Projekt.
		tid, err := f.st.CreateThread(store.Thread{Project: accProject, Title: "legacy", AuthorPrincipalID: "person:3"})
		if err != nil {
			t.Fatal(err)
		}
		kind, dest = store.DestinationDiscussion, strconv.FormatInt(tid, 10)
	}
	code, body := f.call(t, "gus", "POST", "/api/coord/messages", store.CoordMessage{
		DestinationKind: kind, DestinationID: dest, SenderExternalID: "claude:gus",
		ClientID: "q", Body: "please look", Intent: store.IntentQuestion, Mentions: []string{mention}})
	guestAccess := f.st.CoordinationFor(store.Principal{ID: "person:5", Label: "gus"}, "claude:gus")
	_, err := guestAccess.CreateStanding(store.StandingInput{RoomKey: room, ClientID: "s", Body: "always", Mentions: []string{mention}})
	return code, body, err
}

// Ein Gast schreibt im Projektraum nichts, und die Antwort ist dieselbe, ob das
// genannte Ziel im Raum ist oder nicht: der Raum verrät dem Gast nicht, wer in
// ihm ist.
func TestGuestPostIsRefusedTheSameForMemberAndNonMemberTargets(t *testing.T) {
	for _, inThread := range []bool{false, true} {
		inCode, inBody, inErr := guestPostAnswers(t, true, inThread, "claude:mia")
		outCode, outBody, outErr := guestPostAnswers(t, false, inThread, "claude:mia")
		if inCode != http.StatusForbidden || outCode != http.StatusForbidden {
			t.Fatalf("thread=%v status %d %d, want 403 for a guest's post", inThread, inCode, outCode)
		}
		if inBody != outBody {
			t.Errorf("thread=%v: the refusal differs with room membership of the target:\n%q\n%q", inThread, inBody, outBody)
		}
		if !errors.Is(inErr, store.ErrCoordForbidden) || !errors.Is(outErr, store.ErrCoordForbidden) {
			t.Errorf("thread=%v: a guest created a directive: %v / %v", inThread, inErr, outErr)
		}
	}
}

func TestGuestMentionReachesNobodyAndAnswersTheSame(t *testing.T) {
	f, room := guestRoomFixture(t, true)
	if _, err := f.st.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:outside", Provider: "claude", RoomKey: store.RoomKeyForProject(accOther), PrincipalID: "person:6", Person: "nora"}); err != nil {
		t.Fatal(err)
	}
	var codes []int
	var bodies []string
	for _, c := range []struct{ id, mention string }{{"in", "claude:mia"}, {"out", "claude:outside"}, {"nobody", "claude:nobody"}, {"person", "person:6"}} {
		code, body := f.call(t, "gus", "POST", "/api/coord/messages", store.CoordMessage{
			DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:gus",
			ClientID: c.id, Body: "please look " + c.id, Intent: store.IntentQuestion, Mentions: []string{c.mention}})
		codes = append(codes, code)
		bodies = append(bodies, body)
	}
	// A guest writes nothing in the project room, and says nothing about who is there.
	for i := range codes {
		if codes[i] != http.StatusForbidden || bodies[i] != bodies[0] {
			t.Errorf("response %d: %d %q vs %q", i, codes[i], bodies[i], bodies[0])
		}
	}
	for _, who := range []struct{ id, label, agent string }{{"person:3", "mia", "claude:mia"}, {"person:6", "nora", "claude:outside"}, {"person:2", "lena", "claude:lena"}} {
		if got := attentionOf(t, f, who.id, who.label, who.agent); len(got) != 0 {
			t.Errorf("%s got attention from a guest mention: %+v", who.label, got)
		}
	}
	// Ein Mitglied behält den Fehler für unbekannte Ziele und darf erwähnen.
	if code, body := f.call(t, "mia", "POST", "/api/coord/messages", store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:mia",
		ClientID: "mm", Body: "hi", Mentions: []string{"claude:lena"}}); code != 200 {
		t.Errorf("member mention: %d %s", code, body)
	}
	if code, _ := f.call(t, "mia", "POST", "/api/coord/messages", store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:mia",
		ClientID: "mu", Body: "hi", Mentions: []string{"claude:nobody"}}); code != 400 {
		t.Errorf("member mention of unknown: %d", code)
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
	// What a guest may not write is refused in log mode too (strictDenial); only
	// the reads stay open, and they leave the would-deny line.
	f.expect(t, "gus", 403, "POST", "/api/coord/messages", store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:gus",
		ClientID: "log", Body: "hi", Mentions: []string{"claude:mia"}})
	if !strings.Contains(buf.String(), "access: would deny") {
		t.Errorf("expected a would-deny line for the recipient list, got %q", buf.String())
	}
}

// Lazy-Aufräumen um die Fenstergrenze: Bei einem Mitglied als Ziel liegt direkt
// hinter dem Cursor des Gastes die versteckte Attention-Zeile aus derselben
// Transaktion. Altert und verschwindet sie, darf das den Resync des Gastes nicht
// unterscheiden. Für ihn entscheidet nur die Zeit im Cursor (Web-Schicht), nie
// der Tabelleninhalt.
func TestGuestEventResyncDoesNotDependOnHiddenRows(t *testing.T) {
	decide := func(targetInRoom bool) (guestResync, memberResync bool) {
		f := roomGateFixture(t, true)
		room := store.RoomKeyForProject(accProject)
		other := store.RoomKeyForProject(accOther)
		miaRoom := room
		if !targetInRoom {
			miaRoom = other
		}
		for agent, c := range map[string]struct{ principal, room string }{
			"claude:gus": {"person:5", room}, "claude:lena": {"person:2", room}, "claude:mia": {"person:3", miaRoom}} {
			if _, err := f.st.RegisterCoordAgent(store.CoordAgent{ExternalID: agent, Provider: "claude", RoomKey: c.room, PrincipalID: c.principal, Person: agent}); err != nil {
				t.Fatal(err)
			}
		}
		// A lead writes (a guest writes nothing in the project room); with the target
		// in the room the question leaves a hidden attention row, without it not.
		q := store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:lena", ClientID: "q", Body: "q"}
		if targetInRoom {
			q.Intent, q.Mentions = store.IntentQuestion, []string{"claude:mia"}
		}
		f.expect(t, "lena", 200, "POST", "/api/coord/messages", q)
		var cursor int64
		if err := f.st.DB().QueryRow(`SELECT MAX(sequence) FROM coord_events WHERE kind='message'`).Scan(&cursor); err != nil {
			t.Fatal(err)
		}
		// Das Protokoll altert von vorn: der Cursor und, beim Mitglied als Ziel, die
		// versteckte Attention-Zeile direkt dahinter. Ein weiteres Ereignis löst
		// das Aufräumen aus.
		if _, err := f.st.DB().Exec(`UPDATE coord_events SET created_at='2000-01-01T00:00:00.000Z' WHERE sequence<=?`, cursor+1); err != nil {
			t.Fatal(err)
		}
		f.expect(t, "lena", 200, "POST", "/api/coord/messages", store.CoordMessage{
			DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:lena", ClientID: "marker", Body: "m"})
		replay, err := f.st.CoordEventsAfter(store.Principal{ID: "person:5", Label: "gus"}, cursor, 100)
		if err != nil {
			t.Fatal(err)
		}
		member, err := f.st.CoordEventsAfter(store.Principal{ID: "person:3", Label: "mia"}, cursor, 100)
		if err != nil {
			t.Fatal(err)
		}
		return replay.Resync, member.Resync
	}
	inGuest, inMember := decide(true)
	outGuest, outMember := decide(false)
	if inGuest != outGuest {
		t.Errorf("the guest's resync decision differs: member target=%v non-member target=%v", inGuest, outGuest)
	}
	if inGuest || outGuest {
		t.Error("a guest must not resync on missing rows; only the cursor time decides")
	}
	// Ein Mitglied behält die Lückenprüfung als Netz gegen Uhrensprünge: Fehlt
	// hinter einem frischen Cursor Verlauf (hier die Attention-Zeile), lädt es neu.
	if !inMember {
		t.Errorf("non-guest gap safety net did not resync: member target=%v non-member target=%v", inMember, outMember)
	}
}
