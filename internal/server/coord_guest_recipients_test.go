package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
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

// guestViews sammelt alles, was der Gast gus über seine eigene Nachricht
// zurücklesen kann. mentions: was er eingegeben hat.
type guestViews struct {
	APIMentions []string
	Attention   int
	Standing    []string
	Status      int
	Events      []string
	Delivery    store.CoordDeliverySummary
	Presented   []string
}

// guestScenario: gus postet eine Frage und eine Standing-Anweisung an
// `mention`. miaInRoom steuert, ob dieses Ziel wirklich im Raum ist.
func guestScenario(t *testing.T, miaInRoom, inThread bool, mention string) guestViews {
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
	var v guestViews
	var resp struct {
		ID int64 `json:"id"`
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
	cursor, err := f.st.LatestCoordEventSequence()
	if err != nil {
		t.Fatal(err)
	}
	code, body := f.call(t, "gus", "POST", "/api/coord/messages", store.CoordMessage{
		DestinationKind: kind, DestinationID: dest, SenderExternalID: "claude:gus",
		ClientID: "q", Body: "please look", Intent: store.IntentQuestion, Mentions: []string{mention}})
	v.Status = code
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	guestAccess := f.st.CoordinationFor(store.Principal{ID: "person:5", Label: "gus"}, "claude:gus")
	if _, err := guestAccess.CreateStanding(store.StandingInput{RoomKey: room, ClientID: "s", Body: "always", Mentions: []string{mention}}); err != nil {
		t.Fatal(err)
	}
	guestJSON(t, f, "gus", fmt.Sprintf("/api/coord/messages/%d/mentions?agent_external_id=claude:gus", resp.ID), &v.APIMentions)
	var items []store.AttentionItem
	guestJSON(t, f, "gus", "/api/coord/attention?agent_external_id=claude:gus", &items)
	v.Attention = len(items)
	standing, err := guestAccess.Standing(room)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range standing {
		v.Standing = append(v.Standing, s.Targets...)
	}
	// Mitglied und Owner sehen die echte Zustellung.
	member := f.st.CoordinationFor(store.Principal{ID: "person:3", Label: "mia"}, "claude:mia")
	if miaInRoom {
		if err := member.MarkDelivery(resp.ID, store.DeliveryFetched); err != nil {
			t.Fatal(err)
		}
	}
	replay, err := f.st.CoordEventsAfter(store.Principal{ID: "person:5", Label: "gus"}, cursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range replay.Events {
		v.Events = append(v.Events, e.Kind+"/"+e.ObjectKind)
	}
	_, presented, err := guestAccess.MessagePresentationWindow(kind, dest, store.MessageWindow{Mode: "latest", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range presented {
		v.Delivery = p.Delivery
		v.Presented = append(v.Presented, strings.Join(p.Mentions, ","))
	}
	if _, mp, err := member.MessagePresentationWindow(kind, dest, store.MessageWindow{Mode: "latest", Limit: 50}); miaInRoom {
		real := false
		for _, p := range mp {
			real = real || (p.Message.ID == resp.ID && p.Delivery.Fetched > 0)
		}
		if err != nil || !real {
			t.Errorf("member sees the real delivery summary: %+v %v", mp, err)
		}
	}
	got, err := member.MessageMentions(resp.ID)
	if err != nil {
		if miaInRoom {
			t.Fatal(err)
		}
	} else if miaInRoom != (len(got) == 1) {
		t.Errorf("member view of mentions: in room=%v mentions=%v", miaInRoom, got)
	}
	if miaInRoom {
		if a := attentionOf(t, f, "person:3", "mia", "claude:mia"); len(a) != 1 {
			t.Errorf("member attention: %+v", a)
		}
	}
	owner := f.st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, "claude:robin")
	if real, err := owner.MessageMentions(resp.ID); err != nil || (miaInRoom && len(real) != 1) || (!miaInRoom && len(real) != 0) {
		t.Errorf("owner sees the real mentions: %v %v", real, err)
	}
	return v
}

// Was ein Gast von seinen eigenen Beiträgen zurückliest, ist nur seine Eingabe,
// nie das gefilterte Ergebnis: sonst wäre das Zurücklesen ein Orakel dafür, wer
// im Raum ist.
func TestGuestViewsOfOwnPostsAreIdenticalForMemberAndNonMember(t *testing.T) {
	for _, inThread := range []bool{false, true} {
		in := guestScenario(t, true, inThread, "claude:mia")
		out := guestScenario(t, false, inThread, "claude:mia")
		if in.Status != 200 || out.Status != 200 {
			t.Fatalf("thread=%v status %d %d", inThread, in.Status, out.Status)
		}
		if !reflect.DeepEqual(in, out) {
			t.Errorf("thread=%v: guest views differ with room membership:\nmember:     %+v\nnon-member: %+v", inThread, in, out)
		}
		if in.Attention != 0 || in.Delivery != (store.CoordDeliverySummary{}) {
			t.Errorf("thread=%v: guest sees per-recipient state: %+v", inThread, in)
		}
		for _, e := range in.Events {
			if strings.HasPrefix(e, "attention/") || strings.HasPrefix(e, "delivery/") {
				t.Errorf("thread=%v: event stream shows %s to the guest", inThread, e)
			}
		}
	}
	in := guestScenario(t, true, false, "claude:mia")
	if !reflect.DeepEqual(in.APIMentions, []string{"claude:mia"}) || !reflect.DeepEqual(in.Standing, []string{"claude:mia"}) {
		t.Errorf("guest should read back exactly what it typed: %+v", in)
	}
	if in.Attention != 0 {
		t.Errorf("guest sees outgoing attention (state per target leaks delivery): %d", in.Attention)
	}
}

func TestGuestMentionDeliversOnlyToRoomMembers(t *testing.T) {
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
		bodies = append(bodies, strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return -1
			}
			return r
		}, body))
	}
	for i := range codes {
		if codes[i] != 200 || bodies[i] != bodies[0] {
			t.Errorf("response %d differs: %d %q vs %q", i, codes[i], bodies[i], bodies[0])
		}
	}
	items := attentionOf(t, f, "person:3", "mia", "claude:mia")
	if len(items) != 1 || !strings.HasSuffix(items[0].Body, " in") {
		t.Fatalf("member attention: %+v", items)
	}
	for _, who := range []struct{ id, label, agent string }{{"person:6", "nora", "claude:outside"}, {"person:2", "lena", "claude:lena"}} {
		if got := attentionOf(t, f, who.id, who.label, who.agent); len(got) != 0 {
			t.Errorf("%s got attention from a guest mention: %+v", who.label, got)
		}
	}
	// Zurückziehen an erratener Id verrät nichts: für den Gast gibt es den Eintrag nicht.
	guest := f.st.CoordinationFor(store.Principal{ID: "person:5", Label: "gus"}, "claude:gus")
	if err := guest.ResolveAttention(items[0].ID, store.AttentionActionWithdraw); !errors.Is(err, store.ErrCoordNotFound) {
		t.Errorf("guest withdraw of a delivered item: %v", err)
	}
	if err := guest.ResolveAttention(items[0].ID+100, store.AttentionActionWithdraw); !errors.Is(err, store.ErrCoordNotFound) {
		t.Errorf("guest withdraw of a missing item: %v", err)
	}
	// Das Mitglied bekommt die Nachricht als Bitte, nie als Anweisung.
	var msgs []store.CoordMessage
	guestJSON(t, f, "mia", "/api/coord/messages?destination_id="+room+"&agent_external_id=claude:mia", &msgs)
	for _, m := range msgs {
		if m.SenderExternalID == "claude:gus" && strings.HasSuffix(m.Body, " in") && m.Authority != "request" {
			t.Errorf("guest directive reached the member: authority=%q", m.Authority)
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
	f.expect(t, "gus", 200, "POST", "/api/coord/messages", store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:gus",
		ClientID: "log", Body: "hi", Mentions: []string{"claude:mia"}})
	if !strings.Contains(buf.String(), "access: would deny") {
		t.Errorf("expected a would-deny line, got %q", buf.String())
	}
}

// Die Ids der eigenen Attention-Einträge folgen keinem Zählmuster: Eine Erwähnung
// eines Mitglieds legt zusätzlich einen Eintrag an, eine eines Nicht-Mitglieds
// nicht, und bei einem Zähler zeigte die Lücke in den eigenen Ids, welches von
// beiden zutraf.
func TestGuestAttentionIDsFollowNoCountingPattern(t *testing.T) {
	for _, targetInRoom := range []bool{true, false} {
		f := roomGateFixture(t, true)
		room := store.RoomKeyForProject(accProject)
		other := store.RoomKeyForProject(accOther)
		miaRoom := room
		if !targetInRoom {
			miaRoom = other
		}
		for agent, c := range map[string]struct{ principal, room string }{
			"claude:gus": {"person:5", room}, "claude:mia": {"person:3", miaRoom}} {
			if _, err := f.st.RegisterCoordAgent(store.CoordAgent{ExternalID: agent, Provider: "claude", RoomKey: c.room, PrincipalID: c.principal, Person: agent}); err != nil {
				t.Fatal(err)
			}
		}
		var ids []int64
		for i := 0; i < 12; i++ {
			f.expect(t, "gus", 200, "POST", "/api/coord/messages", store.CoordMessage{
				DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:gus",
				ClientID: fmt.Sprintf("m%d", i), Body: "q", Intent: store.IntentQuestion, Mentions: []string{"claude:mia", "claude:gus"}})
		}
		var items []store.AttentionItem
		guestJSON(t, f, "gus", "/api/coord/attention?agent_external_id=claude:gus", &items)
		if len(items) != 12 {
			t.Fatalf("in room=%v: guest sees %d own items, want 12", targetInRoom, len(items))
		}
		for _, it := range items {
			ids = append(ids, it.ID)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for i, id := range ids {
			if id < 1<<32 {
				t.Errorf("in room=%v: id %d looks like a counter", targetInRoom, id)
			}
			if i > 0 && ids[i]-ids[i-1] <= 2 {
				t.Errorf("in room=%v: ids %d and %d follow a counting pattern", targetInRoom, ids[i-1], ids[i])
			}
		}
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
			"claude:gus": {"person:5", room}, "claude:mia": {"person:3", miaRoom}} {
			if _, err := f.st.RegisterCoordAgent(store.CoordAgent{ExternalID: agent, Provider: "claude", RoomKey: c.room, PrincipalID: c.principal, Person: agent}); err != nil {
				t.Fatal(err)
			}
		}
		f.expect(t, "gus", 200, "POST", "/api/coord/messages", store.CoordMessage{
			DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:gus",
			ClientID: "q", Body: "q", Intent: store.IntentQuestion, Mentions: []string{"claude:mia"}})
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
		f.expect(t, "gus", 200, "POST", "/api/coord/messages", store.CoordMessage{
			DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:gus", ClientID: "marker", Body: "m"})
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
