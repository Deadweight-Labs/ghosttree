package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// roomGateFixture: wie accessAPI, dazu ist nora (person:6) Mitglied der Org ohne
// Rolle im Projekt (ben im Befund), mia Member, gus Guest.
func roomGateFixture(t *testing.T, enforce bool) *accessAPIFixture {
	t.Helper()
	f := accessAPI(t, enforce)
	code, _, err := f.st.CreateInvitation("person:1", 1, "", store.OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AcceptInvitation("person:6", code); err != nil {
		t.Fatal(err)
	}
	return f
}

func registerAgent(t *testing.T, f *accessAPIFixture, who, room string) int {
	t.Helper()
	code, _ := f.call(t, who, "POST", "/api/coord/agents", store.CoordAgent{ExternalID: "claude:" + who, Provider: "claude", RoomKey: room})
	return code
}

func roomsOf(t *testing.T, f *accessAPIFixture, who string) []store.CoordRoom {
	t.Helper()
	var out []store.CoordRoom
	body := f.expect(t, who, 200, "GET", "/api/coord/rooms?agent_external_id=claude:"+who, nil)
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return out
}

func TestOrgMemberWithoutRoleCannotJoinProjectRoom(t *testing.T) {
	f := roomGateFixture(t, true)
	room := store.RoomKeyForProject(accProject)
	for who, want := range map[string]int{"robin": 200, "lena": 200, "mia": 200, "gus": 200, "nora": 404} {
		if got := registerAgent(t, f, who, room); got != want {
			t.Errorf("%s registers in the project room: %d, want %d", who, got, want)
		}
	}
	// Ein Projekt, in dem mia keine Rolle hat.
	if code, _ := f.call(t, "mia", "POST", "/api/coord/agents", store.CoordAgent{ExternalID: "claude:mia-other", Provider: "claude", RoomKey: store.RoomKeyForProject(accOther)}); code != 404 {
		t.Errorf("member without role in the other project: %d", code)
	}
	// nora ist nicht registriert und kann auch keine Räume für sich auflisten.
	f.expect(t, "nora", 403, "GET", "/api/coord/rooms?agent_external_id=claude:nora", nil)
}

func TestRoomsHideProjectRoomsAndMemberListsWithoutRole(t *testing.T) {
	f := roomGateFixture(t, true)
	room := store.RoomKeyForProject(accProject)
	for agent, principal := range map[string]string{"claude:mia": "person:3", "claude:gus": "person:5", "claude:nora": "person:6", "claude:lena": "person:2"} {
		if _, err := f.st.RegisterCoordAgent(store.CoordAgent{ExternalID: agent, Provider: "claude", RoomKey: room, PrincipalID: principal, Person: strings.TrimPrefix(agent, "claude:")}); err != nil {
			t.Fatal(err)
		}
	}
	find := func(who string) (store.CoordRoom, bool) {
		for _, r := range roomsOf(t, f, who) {
			if r.Key == room {
				return r, true
			}
		}
		return store.CoordRoom{}, false
	}
	if r, ok := find("mia"); !ok || len(r.Members) != 4 {
		t.Errorf("member sees the room with its members: %+v %v", r, ok)
	}
	if r, ok := find("gus"); !ok || len(r.Members) != 0 {
		t.Errorf("guest sees the room but not the agents: %+v %v", r, ok)
	}
	// nora ist Mitglied des Raums (Altbestand), hat aber keine Rolle.
	if r, ok := find("nora"); ok {
		t.Errorf("no role, but the room is listed: %+v", r)
	}

	// Rolle entzogen: die Mitgliedschaft bleibt in der Tabelle, wirkt aber nicht mehr.
	if err := f.st.RemoveProjectRole("person:1", accProject, "person:3", store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	if r, ok := find("mia"); ok {
		t.Errorf("role removed, room still listed: %+v", r)
	}
	f.expect(t, "mia", 404, "GET", "/api/coord/messages?destination_id="+room+"&agent_external_id=claude:mia", nil)
	// Org verlassen: ebenso.
	if err := f.st.RemoveOrgMember("person:1", 1, "person:2"); err != nil {
		t.Fatal(err)
	}
	if r, ok := find("lena"); ok {
		t.Errorf("org membership removed, room still listed: %+v", r)
	}
	// Die Rolle kommt zurück: die Mitgliedschaft lebt wieder auf, ohne neu zu joinen.
	if err := f.st.SetProjectRole("person:1", accProject, "person:3", store.RoleMember, false, store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	if _, ok := find("mia"); !ok {
		t.Error("role restored, room should be listed again")
	}
}

func TestRoomGateLogModeKeepsOldBehaviourAndLogsWouldDeny(t *testing.T) {
	f := roomGateFixture(t, false)
	var buf bytes.Buffer
	f.st.SetAccessMode(store.AccessMode{Logger: slog.New(slog.NewTextHandler(&buf, nil))})
	room := store.RoomKeyForProject(accProject)
	if got := registerAgent(t, f, "nora", room); got != 200 {
		t.Fatalf("log mode refuses: %d", got)
	}
	found := false
	for _, r := range roomsOf(t, f, "nora") {
		found = found || r.Key == room
	}
	if !found {
		t.Error("log mode must keep listing the room")
	}
	if !bytes.Contains(buf.Bytes(), []byte("access: would deny")) {
		t.Errorf("expected a would-deny line, got %q", buf.String())
	}
}

// Ein Mitglied ohne Rolle und ein Fremder bekommen dieselbe Antwort, und der
// Claim verrät dem Mitglied ohne Rolle weder Id noch Namen.
func TestProjectProbesAreIndistinguishable(t *testing.T) {
	f := roomGateFixture(t, true)
	p, _ := f.st.ProjectByRemote(accProject)
	members := idPath("/api/projects/%d/members", p.ID)
	unknown := "/api/projects/9999/members"
	var bodies []string
	for _, c := range []struct{ who, path string }{{"nora", members}, {"nora", unknown}} {
		code, body := f.call(t, c.who, "GET", c.path, nil)
		if code != 404 {
			t.Fatalf("%s %s = %d", c.who, c.path, code)
		}
		bodies = append(bodies, body)
	}
	if bodies[0] != bodies[1] {
		t.Errorf("member without role can tell a known project from an unknown one:\n%s\n%s", bodies[0], bodies[1])
	}
	code, body := f.call(t, "nora", "POST", "/api/projects/claim", map[string]string{"remote": accProject})
	if code != 404 || strings.Contains(body, `"id"`) || strings.Contains(body, accProject) {
		t.Errorf("claim leaks the project to a member without role: %d %s", code, body)
	}
	// Der Owner behält den idempotenten Claim.
	if code, body := f.call(t, "robin", "POST", "/api/projects/claim", map[string]string{"remote": accProject}); code != 200 {
		t.Errorf("owner claim: %d %s", code, body)
	}
	// mia hat eine Rolle und sieht das Projekt ohnehin.
	if code, body := f.call(t, "mia", "POST", "/api/projects/claim", map[string]string{"remote": accProject}); code != 200 {
		t.Errorf("member with role: %d %s", code, body)
	}
}

func TestSystemPrefixCannotBeRegisteredOverTheAPI(t *testing.T) {
	f := roomGateFixture(t, true)
	room := store.RoomKeyForProject(accProject)
	for _, id := range []string{"system:wait-cycle", "System:x"} {
		code, body := f.call(t, "mia", "POST", "/api/coord/agents", store.CoordAgent{ExternalID: id, Provider: "claude", RoomKey: room})
		if code != 400 || !strings.Contains(body, "reserved_external_id") {
			t.Errorf("registering %q: %d %s", id, code, body)
		}
	}
}
