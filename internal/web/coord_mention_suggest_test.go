package web

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func suggestRoom(t *testing.T) (srv *httptest.Server, tokens map[string]string, room string) {
	t.Helper()
	const project = "github.com/dw/suggest"
	st, err := store.Open(t.TempDir() + "/web.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tokens = map[string]string{}
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
	room = store.RoomKeyForProject(project)
	for agent, principal := range map[string]string{"claude:mia": "person:2", "claude:robin": "person:1", "claude:gus": "person:3"} {
		if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: agent, Provider: "claude", RoomKey: room, PrincipalID: principal, Person: strings.TrimPrefix(agent, "claude:"), DisplayName: "zed-" + strings.TrimPrefix(agent, "claude:")}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CoordinationFor(store.Principal{ID: "person:2", Label: "mia"}, "claude:mia").Send(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:mia", ClientID: "c1", Body: "hello from the room"}); err != nil {
		t.Fatal(err)
	}
	st.SetAccessMode(store.AccessMode{Enforce: true})
	srv = httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	return srv, tokens, room
}

func suggestPage(t *testing.T, srv *httptest.Server, token, room string) string {
	t.Helper()
	resp, err := login(t, srv, token).Get(srv.URL + "/ui/coord?room=" + room)
	if err != nil {
		t.Fatal(err)
	}
	out := body(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("room: %d %s", resp.StatusCode, out)
	}
	return out
}

// The composer carries the names it can complete, as plain markup for the
// external script. Only names the viewer may use are in it.
func TestRoomComposerOffersTheNamesOfThePeopleAndAgentsOfTheRoom(t *testing.T) {
	srv, tokens, room := suggestRoom(t)
	page := suggestPage(t, srv, tokens["mia"], room)
	if !strings.Contains(page, `data-handle="zed-robin"`) {
		t.Fatalf("member does not get the agent as a suggestion:\n%s", regexp.MustCompile(`(?s)comp-suggest.{0,600}`).FindString(page))
	}
	if !strings.Contains(page, `src="/static/mentions.js"`) {
		t.Error("the completion script is not loaded")
	}
}

func TestRoomComposerOffersNothingToAGuest(t *testing.T) {
	srv, tokens, room := suggestRoom(t)
	page := suggestPage(t, srv, tokens["gus"], room)
	if !strings.Contains(page, "hello from the room") {
		t.Fatal("the guest does not read the room in this fixture")
	}
	if strings.Contains(page, "data-handle") {
		t.Error("a guest must not get suggestions from the member list")
	}
}

func TestMentionScriptIsExternalAndKeepsEnterForTheList(t *testing.T) {
	js := string(mustReadEmbedded(t, "static/mentions.js"))
	for _, want := range []string{`event.key === "Enter"`, `"Escape"`, `"ArrowDown"`, "isComposing", "stopPropagation", "dataset.handle", ", true)"} {
		if !strings.Contains(js, want) {
			t.Errorf("mentions.js missing %q", want)
		}
	}
	for _, bad := range []string{"innerHTML", "eval(", "document.write"} {
		if strings.Contains(js, bad) {
			t.Errorf("mentions.js uses %s", bad)
		}
	}
}
