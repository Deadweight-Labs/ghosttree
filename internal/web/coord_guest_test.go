package web

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

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
		s = regexp.MustCompile(`name="(csrf_token|form_id)" value="[^"]*"`).ReplaceAllString(s, "")
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
