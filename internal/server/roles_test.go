package server

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const apiRoleProject = "github.com/dw/p"

// roleAPI richtet Org alpha mit robin (Owner), anna und ben (Mitglieder) und
// dem Projekt ein; carl steht draußen.
func roleAPI(t *testing.T) (orgFixture, string) {
	t.Helper()
	f := newOrgFixture(t)
	org, err := f.st.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"person:2", "person:3"} {
		code, _, _ := f.st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
		if _, err := f.st.AcceptInvitation(who, code); err != nil {
			t.Fatal(err)
		}
	}
	p, err := f.st.EnsureProject("person:1", apiRoleProject)
	if err != nil {
		t.Fatal(err)
	}
	return f, "/api/projects/" + strconv.FormatInt(p.ID, 10) + "/members"
}

func TestProjectRoleAPIGrantRules(t *testing.T) {
	f, base := roleAPI(t)
	out := f.mustCall(t, 200, "GET", base, f.robin, nil)
	you := out["you"].(map[string]any)
	if you["role"] != "owner" || you["implicit"] != true {
		t.Fatalf("robin: %v", out)
	}
	f.mustCall(t, 404, "GET", base, f.carl, nil) // draußen: gibt es nicht
	f.mustCall(t, 401, "GET", base, "", nil)     //
	f.mustCall(t, 404, "GET", "/api/projects/999/members", f.robin, nil)

	f.mustCall(t, 200, "PUT", base+"/anna", f.robin, map[string]any{"role": "lead", "can_review": true})
	f.mustCall(t, 200, "PUT", base+"/person:3", f.anna, map[string]any{"role": "member"})
	// Lead vergibt keinen Owner und keinen Lead.
	if code, out := f.call(t, "PUT", base+"/ben", f.anna, map[string]any{"role": "owner"}); code != 403 || out["code"] != "role_forbidden" {
		t.Fatalf("lead grants owner: %d %v", code, out)
	}
	f.mustCall(t, 403, "PUT", base+"/ben", f.anna, map[string]any{"role": "lead"})
	// Selbsterhöhung.
	if code, out := f.call(t, "PUT", base+"/anna", f.anna, map[string]any{"role": "owner"}); code != 403 || out["code"] != "self_promotion" {
		t.Fatalf("self promotion: %d %v", code, out)
	}
	// Ein Member vergibt nichts.
	if code, out := f.call(t, "PUT", base+"/ben", f.ben, map[string]any{"role": "lead"}); code != 403 || out["code"] != "not_grantor" {
		t.Fatalf("member grants: %d %v", code, out)
	}
	// Von außen: das Projekt existiert nicht.
	f.mustCall(t, 404, "PUT", base+"/ben", f.carl, map[string]any{"role": "guest"})
	// Org-Owner sind implizit Owner.
	if code, out := f.call(t, "PUT", base+"/robin", f.robin, map[string]any{"role": "guest"}); code != 409 || out["code"] != "implicit_owner" {
		t.Fatalf("implicit owner: %d %v", code, out)
	}
	// Agenten vergeben keine Rollen: weder per Körperfeld noch per Query.
	f.mustCall(t, 403, "PUT", base+"/ben", f.robin, map[string]any{"role": "guest", "agent_external_id": "claude:h:1"})
	f.mustCall(t, 403, "PUT", base+"/ben?agent_external_id=claude:h:1", f.robin, map[string]any{"role": "guest"})
	f.mustCall(t, 403, "DELETE", base+"/ben?agent_external_id=claude:h:1", f.robin, nil)
	if got := f.st.ProjectRole(apiRoleProject, "person:3"); got.Role != "member" {
		t.Fatalf("a rejected call changed the role: %+v", got)
	}
	f.mustCall(t, 400, "PUT", base+"/ben", f.robin, map[string]any{"role": "reviewer"})
	f.mustCall(t, 404, "PUT", base+"/nobody", f.robin, map[string]any{"role": "guest"})

	f.mustCall(t, 204, "DELETE", base+"/ben", f.robin, nil)
	f.mustCall(t, 404, "DELETE", base+"/ben", f.robin, nil)
	out = f.mustCall(t, 200, "GET", base, f.anna, nil)
	if members := out["members"].([]any); len(members) != 2 {
		t.Fatalf("members after removal: %v", members)
	}
}

func TestAgentRoleOverAPIAndPeers(t *testing.T) {
	f, base := roleAPI(t)
	f.mustCall(t, 200, "PUT", base+"/ben", f.robin, map[string]any{"role": "member", "can_review": true})
	room := "project:" + apiRoleProject
	reg := func(token, id, role string, want int) {
		t.Helper()
		resp := req(t, "POST", f.srv.URL+"/api/coord/agents", token, store.CoordAgent{ExternalID: id, Provider: "claude", RoomKey: room, Role: role})
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("register %s as %q = %d, want %d", id, role, resp.StatusCode, want)
		}
	}
	reg(f.robin, "claude:h:lead", "lead", 200)
	reg(f.ben, "claude:h:ben", "lead", 200)   // über dem Konto: wird gekappt
	reg(f.anna, "claude:h:anna", "lead", 200) // Org-Mitglied ohne Rolle: guest
	reg(f.robin, "claude:h:bad", "owner", 400)
	reg(f.robin, "claude:h:bad", "admin", 400)

	resp := req(t, "GET", f.srv.URL+"/api/coord/agents?room_key="+room+"&agent_external_id=claude:h:lead", f.robin, nil)
	defer resp.Body.Close()
	var peers []store.CoordAgent
	if err := json.NewDecoder(resp.Body).Decode(&peers); err != nil || len(peers) != 3 {
		t.Fatalf("peers: %+v %v", peers, err)
	}
	got := map[string]store.CoordAgent{}
	for _, p := range peers {
		got[p.ExternalID] = p
	}
	if p := got["claude:h:lead"]; p.Role != "lead" || p.Owner != "robin" {
		t.Errorf("lead = %+v", p)
	}
	if p := got["claude:h:ben"]; p.Role != "member" || !p.CanReview {
		t.Errorf("ben's agent = %+v", p)
	}
	if p := got["claude:h:anna"]; p.Role != "guest" {
		t.Errorf("anna's agent = %+v", p)
	}
}
