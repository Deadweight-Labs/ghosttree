package web

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const namesAgentID = "11111111-aaaa-4bbb-8ccc-000000000001"

// namesRoom builds a project room with an owner, a member and a guest; the
// owner's agent is named like its session UUID, as in the two-account E2E run.
func namesRoom(t *testing.T) (srv *httptest.Server, st *store.Store, room string) {
	t.Helper()
	const project = "github.com/dw/names"
	st, err := store.Open(t.TempDir() + "/web.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for i, name := range []string{"Robin", "Mia", "Gus"} {
		if _, err := st.AddAccount(name, "", i == 0); err != nil {
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
	room = store.RoomKeyForProject(project)
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: namesAgentID, Provider: "claude", RoomKey: room,
		PrincipalID: "person:1", DisplayName: namesAgentID}); err != nil {
		t.Fatal(err)
	}
	// A guest reads the room through the agent they brought along.
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:gus-box:99999999-aaaa-4bbb-8ccc-000000000009", Provider: "claude", RoomKey: room,
		PrincipalID: "person:3"}); err != nil {
		t.Fatal(err)
	}
	first, err := st.CoordinationFor(store.Principal{ID: "person:1", Label: "Robin", TokenKind: store.WebSessionKind}, "").Send(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: "h1", Body: "welcome to the room"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CoordinationFor(store.Principal{ID: "person:1", Label: "Robin"}, namesAgentID).Send(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: namesAgentID, ClientID: "a1", Body: "on it", ReplyTo: first}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CoordinationFor(store.Principal{ID: "person:3", Label: "Gus", TokenKind: store.WebSessionKind}, "").Send(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: "g1", Body: "thanks both", ReplyTo: first,
		Intent: store.IntentQuestion, Mentions: []string{namesAgentID}}); err != nil {
		t.Fatal(err)
	}
	srv = httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	return srv, st, room
}

func namesGet(t *testing.T, srv *httptest.Server, st *store.Store, account, path string) string {
	t.Helper()
	resp, err := loginInteractive(t, srv, st, account).Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	out := body(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s = %d %s", path, resp.StatusCode, out)
	}
	return out
}

var rawIDRE = regexp.MustCompile(`person:\d|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}`)

func visibleText(page string) string {
	// The ID may appear in attributes (values, hrefs); only what is read counts.
	page = regexp.MustCompile(`(?s)<script.*?</script>`).ReplaceAllString(page, "")
	return regexp.MustCompile(`<[^>]*>`).ReplaceAllString(page, "\n")
}

func TestAMemberReadsAgentsAsToolOwnerAndSeesWhoIsInTheProject(t *testing.T) {
	srv, st, room := namesRoom(t)
	page := namesGet(t, srv, st, "Mia", "/ui/coord?room="+room)
	text := visibleText(page)
	if !strings.Contains(text, "Claude · Robin") {
		t.Errorf("the agent is not named by tool and owner: %s", text)
	}
	if m := rawIDRE.FindString(text); m != "" {
		t.Errorf("a raw ID %q is shown to a member", m)
	}
	// Everybody with a role in the project is listed, with that role.
	for _, want := range []string{"Robin", "Mia", "Gus"} {
		if !strings.Contains(text, want) {
			t.Errorf("participants lack %q", want)
		}
	}
	for _, want := range []string{"Owner", "Guest"} {
		if !strings.Contains(page, want) {
			t.Errorf("roles lack %q", want)
		}
	}
	// People who are not in the room cannot be addressed from here: no chip.
	if strings.Contains(page, `name="mentions" value="person:3"`) {
		t.Error("a listed-only person is offered as a recipient")
	}
	agents := visibleText(namesGet(t, srv, st, "Mia", "/ui/agents"))
	if !strings.Contains(agents, "Claude · Robin") || rawIDRE.MatchString(agents) {
		t.Errorf("agents page: %s", agents)
	}
}

func TestAGuestSeesNeitherIDsNorTheProjectsPeople(t *testing.T) {
	srv, st, room := namesRoom(t)
	page := namesGet(t, srv, st, "Gus", "/ui/coord?room="+room)
	text := visibleText(page)
	if m := rawIDRE.FindString(text); m != "" {
		t.Errorf("a guest reads the raw ID %q: %s", m, text)
	}
	for _, leak := range []string{"Robin", "Mia", "Unknown participant"} {
		if strings.Contains(text, leak) {
			t.Errorf("a guest reads %q", leak)
		}
	}
	for _, want := range []string{"Someone", "Agent", "welcome to the room"} {
		if !strings.Contains(text, want) {
			t.Errorf("guest page lacks %q: %s", want, text)
		}
	}
}
