package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Ein Gast liest den Projektraum, bekommt aber keine Mitgliederliste: weder
// Peers noch Empfänger. Die Seite muss trotzdem öffnen.
func TestGuestOpensProjectRoomWithoutMemberList(t *testing.T) {
	const project = "github.com/dw/guestroom"
	st, err := store.Open(t.TempDir() + "/web.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tokens := map[string]string{}
	for i, name := range []string{"robin", "mia", "gus"} {
		if _, err := st.AddAccount(name, "", i == 0); err != nil {
			t.Fatal(err)
		}
		if tokens[name], _, err = st.CreateToken(name, store.TokenSpec{Label: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	org, err := st.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"person:2", "person:3"} {
		code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
		if _, err := st.AcceptInvitation(who, code); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.EnsureProject("person:1", project); err != nil {
		t.Fatal(err)
	}
	for who, role := range map[string]string{"person:2": store.RoleMember, "person:3": store.RoleGuest} {
		if err := st.SetProjectRole("person:1", project, who, role, false, store.RoleViaAPI); err != nil {
			t.Fatal(err)
		}
	}
	room := store.RoomKeyForProject(project)
	for agent, principal := range map[string]string{"claude:mia": "person:2", "claude:gus": "person:3", "claude:silent": "person:2"} {
		if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: agent, Provider: "claude", RoomKey: room, PrincipalID: principal, Person: strings.TrimPrefix(agent, "claude:"), DisplayName: "zed-" + strings.TrimPrefix(agent, "claude:")}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CoordinationFor(store.Principal{ID: "person:2", Label: "mia"}, "claude:mia").Send(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:mia", ClientID: "c1", Body: "hello from the room"}); err != nil {
		t.Fatal(err)
	}
	st.SetAccessMode(store.AccessMode{Enforce: true})
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)

	page := func(who string) string {
		t.Helper()
		resp, err := login(t, srv, tokens[who]).Get(srv.URL + "/ui/coord?room=" + room)
		if err != nil {
			t.Fatal(err)
		}
		out := body(t, resp)
		if resp.StatusCode != 200 {
			t.Fatalf("%s opens the room: %d %s", who, resp.StatusCode, out)
		}
		return out
	}
	if out := page("mia"); !strings.Contains(out, "zed-gus") {
		t.Error("a member sees the room's agents")
	}
	out := page("gus")
	if !strings.Contains(out, "hello from the room") {
		t.Error("guest does not see the room's messages")
	}
	for _, leak := range []string{"zed-silent", "claude:silent"} {
		if strings.Contains(out, leak) {
			t.Errorf("guest page leaks the member list: %q", leak)
		}
	}
}

// Was ein Gast auf der Seite von seinem eigenen Beitrag liest, ist seine
// Eingabe: dieselbe Seite für ein Ziel im Raum und eines außerhalb.
func TestGuestWebViewsShowTypedMentionsNotDeliveredOnes(t *testing.T) {
	const project = "github.com/dw/guestmention"
	build := func(ghostInRoom bool) (page, overview string) {
		st, err := store.Open(t.TempDir() + "/web.db")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		tokens := map[string]string{}
		for i, name := range []string{"robin", "mia", "gus"} {
			if _, err := st.AddAccount(name, "", i == 0); err != nil {
				t.Fatal(err)
			}
			if tokens[name], _, err = st.CreateToken(name, store.TokenSpec{Label: "t"}); err != nil {
				t.Fatal(err)
			}
		}
		org, _ := st.CreateOrg("person:1", "Alpha", "alpha")
		for _, who := range []string{"person:2", "person:3"} {
			code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
			if _, err := st.AcceptInvitation(who, code); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := st.EnsureProject("person:1", project); err != nil {
			t.Fatal(err)
		}
		for who, role := range map[string]string{"person:2": store.RoleMember, "person:3": store.RoleGuest} {
			if err := st.SetProjectRole("person:1", project, who, role, false, store.RoleViaAPI); err != nil {
				t.Fatal(err)
			}
		}
		st.SetAccessMode(store.AccessMode{Enforce: true})
		room := store.RoomKeyForProject(project)
		ghostRoom := room
		if !ghostInRoom {
			ghostRoom = store.RoomKeyForProject("github.com/dw/elsewhere")
		}
		for agent, r := range map[string]string{"claude:mia": room, "claude:gus": room, "claude:ghost": ghostRoom} {
			principal := map[string]string{"claude:mia": "person:2", "claude:gus": "person:3", "claude:ghost": "person:2"}[agent]
			if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: agent, Provider: "claude", RoomKey: r, PrincipalID: principal, Person: "p"}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := st.CoordinationFor(store.Principal{ID: "person:3", Label: "gus"}, "claude:gus").Send(store.CoordMessage{
			DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:gus", ClientID: "c1",
			Body: "a question", Intent: store.IntentQuestion, Mentions: []string{"claude:ghost"}}); err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(New(st))
		t.Cleanup(srv.Close)
		get := func(path string) string {
			resp, err := login(t, srv, tokens["gus"]).Get(srv.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			out := body(t, resp)
			if resp.StatusCode != 200 {
				t.Fatalf("GET %s = %d", path, resp.StatusCode)
			}
			return out
		}
		return get("/ui/coord?room=" + room), get("/ui/coord")
	}
	inPage, inOverview := build(true)
	outPage, outOverview := build(false)
	for name, page := range map[string]string{"room/in": inPage, "room/out": outPage} {
		if !strings.Contains(page, "Erwähnt: Unbekannter Teilnehmer") {
			t.Errorf("%s: the guest does not read back what it typed", name)
		}
	}
	// Die Nachricht gleicht sich in beiden Fällen; der Rest unterscheidet sich nur in IDs/Zeiten.
	strip := func(s string) string {
		s = regexp.MustCompile(`(name="(csrf_token|form_id)" value|data-coord-event-cursor)="[^"]*"`).ReplaceAllString(s, "")
		return strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return -1
			}
			return r
		}, s)
	}
	if strip(inPage) != strip(outPage) {
		t.Error("room page differs for the guest depending on whether the target is in the room")
	}
	if strip(inOverview) != strip(outOverview) {
		t.Error("overview differs for the guest depending on whether the target is in the room")
	}
}

// guestStream spielt dieselbe Lage mit einem Ziel im oder außerhalb des Raums
// durch und liefert, was der Gast im Ereignisstrom sieht.
func guestStream(t *testing.T, targetInRoom bool) (events []string, ids []string, raw string) {
	t.Helper()
	const project = "github.com/dw/guestsse"
	st, err := store.Open(t.TempDir() + "/web.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tokens := map[string]string{}
	for i, name := range []string{"robin", "mia", "gus"} {
		if _, err := st.AddAccount(name, "", i == 0); err != nil {
			t.Fatal(err)
		}
		if tokens[name], _, err = st.CreateToken(name, store.TokenSpec{Label: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	org, _ := st.CreateOrg("person:1", "Alpha", "alpha")
	for _, who := range []string{"person:2", "person:3"} {
		code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
		if _, err := st.AcceptInvitation(who, code); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.EnsureProject("person:1", project); err != nil {
		t.Fatal(err)
	}
	for who, role := range map[string]string{"person:2": store.RoleMember, "person:3": store.RoleGuest} {
		if err := st.SetProjectRole("person:1", project, who, role, false, store.RoleViaAPI); err != nil {
			t.Fatal(err)
		}
	}
	st.SetAccessMode(store.AccessMode{Enforce: true})
	room := store.RoomKeyForProject(project)
	targetRoom := room
	if !targetInRoom {
		targetRoom = store.RoomKeyForProject("github.com/dw/elsewhere")
	}
	for agent, r := range map[string]string{"claude:mia": room, "claude:gus": room, "claude:target": targetRoom} {
		principal := map[string]string{"claude:mia": "person:2", "claude:gus": "person:3", "claude:target": "person:2"}[agent]
		if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: agent, Provider: "claude", RoomKey: r, PrincipalID: principal, Person: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	guest := login(t, srv, tokens["gus"])
	read := func(lastID string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/ui/coord/events?after=0", nil)
		if lastID != "" {
			req.Header.Set("Last-Event-ID", lastID)
		}
		resp, err := guest.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	start := read("")
	cursor := lastSSEIDOrEmpty(start)
	gus := st.CoordinationFor(store.Principal{ID: "person:3", Label: "gus"}, "claude:gus")
	id, err := gus.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:gus",
		ClientID: "q", Body: "question", Intent: store.IntentQuestion, Mentions: []string{"claude:target"}})
	if err != nil {
		t.Fatal(err)
	}
	mia := st.CoordinationFor(store.Principal{ID: "person:2", Label: "mia"}, "claude:mia")
	if targetInRoom {
		if err := mia.MarkDelivery(id, store.DeliveryFetched); err != nil {
			t.Fatal(err)
		}
	}
	if err := mia.MarkRead(store.DestinationRoom, room, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := gus.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:gus", ClientID: "marker", Body: "marker"}); err != nil {
		t.Fatal(err)
	}
	raw = read(cursor)
	for _, line := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "event: "):
			events = append(events, strings.TrimPrefix(line, "event: "))
		case strings.HasPrefix(line, "id: "):
			ids = append(ids, strings.TrimPrefix(line, "id: "))
		}
	}
	// Mit dem letzten Cursor setzt der Strom fort, ohne Wiederholung.
	if again := read(ids[len(ids)-1]); strings.Contains(again, "coord.changed") {
		t.Errorf("resuming with the last token replays events: %q", again)
	}
	return events, ids, raw
}

func lastSSEIDOrEmpty(stream string) string {
	id := ""
	for _, line := range strings.Split(stream, "\n") {
		if strings.HasPrefix(line, "id: ") {
			id = strings.TrimPrefix(line, "id: ")
		}
	}
	return id
}

// Der Gast liest den Ereignisstrom: ob das Ziel im Raum ist, darf an Anzahl,
// Art und Form der Ids nicht erkennbar sein. Die Ids sind undurchsichtig, keine
// vergleichbaren Zahlen.
func TestGuestEventStreamLooksTheSameForMemberAndNonMemberTargets(t *testing.T) {
	inEvents, inIDs, inRaw := guestStream(t, true)
	outEvents, outIDs, outRaw := guestStream(t, false)
	if strings.Join(inEvents, ",") != strings.Join(outEvents, ",") || len(inIDs) != len(outIDs) {
		t.Errorf("event streams differ:\nmember:     %v\nnon-member: %v", inEvents, outEvents)
	}
	if len(inIDs) == 0 {
		t.Fatal("the guest sees no events at all")
	}
	numeric := regexp.MustCompile(`^[0-9]+$`)
	seen := map[string]bool{}
	for _, id := range append(append([]string{}, inIDs...), outIDs...) {
		if numeric.MatchString(id) || len(id) < 30 {
			t.Errorf("event id looks like a counter: %q", id)
		}
		if seen[id] {
			t.Errorf("event id repeats, so ids can be compared: %q", id)
		}
		seen[id] = true
	}
	for _, raw := range []string{inRaw, outRaw} {
		if strings.Contains(raw, `"sequence"`) || strings.Contains(raw, "attention/") || strings.Contains(raw, `"kind":"delivery"`) || strings.Contains(raw, `"kind":"read"`) || strings.Contains(raw, `"kind":"attention"`) {
			t.Errorf("stream carries hidden or numeric data: %q", raw)
		}
	}
}

func TestCoordCursorTokenResyncAndTampering(t *testing.T) {
	srv, st, client := signedIn(t)
	room := store.RoomKeyForProject("github.com/x/y")
	materializeWebRoom(t, st, room)
	first, _ := readFiniteSSE(t, client, srv.URL+"/ui/coord/events?after=0", "")
	token := lastSSEID(t, first)
	if _, err := st.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "fixture:" + room, ClientID: "after-token", Body: "x"}); err != nil {
		t.Fatal(err)
	}
	// Mit dem Token geht es dort weiter, wo es stand.
	body, _ := readFiniteSSE(t, client, srv.URL+"/ui/coord/events", token)
	if !strings.Contains(body, "event: coord.changed") || strings.Contains(body, "event: resync") {
		t.Fatalf("resume with a valid token: %q", body)
	}
	// Verändert, abgeschnitten, fremd, leer oder eine rohe Zahl: Resync, kein Fehler.
	mid := len(token) / 2
	flipped := token[:mid] + map[bool]string{true: "B", false: "A"}[token[mid] == 'A'] + token[mid+1:]
	for name, bad := range map[string]string{"flipped": flipped, "truncated": token[:len(token)-4], "number": "3", "junk": "!!!", "huge": strings.Repeat("A", 4096)} {
		body, headers := readFiniteSSE(t, client, srv.URL+"/ui/coord/events", bad)
		if !strings.Contains(body, "event: resync") || strings.Contains(body, "coord.changed") {
			t.Errorf("%s: want an opaque resync, got %q", name, body)
		}
		if headers.Get("Content-Type") != "text/event-stream" {
			t.Errorf("%s: content type %q", name, headers.Get("Content-Type"))
		}
	}
}
