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

func TestProjectRolesAreReadableButNotWritableOverTheAPI(t *testing.T) {
	f, base := roleAPI(t)
	out := f.mustCall(t, 200, "GET", base, f.robin, nil)
	you := out["you"].(map[string]any)
	if you["role"] != "owner" || you["implicit"] != true {
		t.Fatalf("robin: %v", out)
	}
	f.mustCall(t, 404, "GET", base, f.carl, nil) // draußen: gibt es nicht
	f.mustCall(t, 401, "GET", base, "", nil)
	f.mustCall(t, 404, "GET", "/api/projects/999/members", f.robin, nil)
	if err := f.st.SetProjectRole("person:1", apiRoleProject, "person:3", "member", false, store.RoleViaWeb); err != nil {
		t.Fatal(err)
	}
	out = f.mustCall(t, 200, "GET", base, f.anna, nil)
	if members := out["members"].([]any); len(members) != 2 {
		t.Fatalf("members: %v", members)
	}
}

// Rollen, Mitgliedschaften, Einladungen und Projektverschiebungen ändert kein
// Bearer-Token, auch nicht das des Owners und auch nicht mit agent_external_id.
func TestAdminRoutesRefuseBearerTokens(t *testing.T) {
	f, base := roleAPI(t)
	if err := f.st.SetProjectRole("person:1", apiRoleProject, "person:3", "member", false, store.RoleViaWeb); err != nil {
		t.Fatal(err)
	}
	beta, _ := f.st.CreateOrg("person:1", "Beta", "beta")
	_ = beta
	type call struct {
		method, path string
		body         map[string]any
	}
	calls := []call{
		{"PUT", base + "/anna", map[string]any{"role": "owner"}},
		{"DELETE", base + "/ben", nil},
		{"PUT", "/api/orgs/alpha/members/anna", map[string]any{"role": "owner"}},
		{"DELETE", "/api/orgs/alpha/members/ben", nil},
		{"POST", "/api/orgs/alpha/invitations", map[string]any{"role": "owner"}},
		{"POST", "/api/projects/move", map[string]any{"remote": apiRoleProject, "org": "beta"}},
	}
	for _, c := range calls {
		for name, token := range map[string]string{"owner token": f.robin, "member token": f.anna} {
			variants := map[string]struct {
				path string
				body map[string]any
			}{
				"plain":          {c.path, c.body},
				"agent in query": {c.path + "?agent_external_id=claude:h:1", c.body},
			}
			if c.body != nil {
				withAgent := map[string]any{"agent_external_id": "claude:h:1"}
				for k, v := range c.body {
					withAgent[k] = v
				}
				variants["agent in body"] = struct {
					path string
					body map[string]any
				}{c.path, withAgent}
			}
			for vname, v := range variants {
				code, out := f.call(t, c.method, v.path, token, v.body)
				// Das Mitglied darf bei manchen Routen schon vorher abgewiesen
				// werden; 403 ist es in jedem Fall.
				if code != 403 {
					t.Fatalf("%s %s with %s (%s) = %d %v", c.method, c.path, name, vname, code, out)
				}
				if token == f.robin && out["code"] != "web_session_required" {
					t.Fatalf("%s %s with %s (%s): %v", c.method, c.path, name, vname, out)
				}
			}
		}
	}
	// Nichts hat sich geändert.
	if got := f.st.ProjectRole(apiRoleProject, "person:3"); got.Role != "member" {
		t.Fatalf("role changed: %+v", got)
	}
	if got := f.st.ProjectRole(apiRoleProject, "person:2"); got.Role != "" {
		t.Fatalf("role granted: %+v", got)
	}
	if f.st.OrgRole(1, "person:2") != "member" || f.st.OrgRole(1, "person:3") != "member" {
		t.Fatal("membership changed")
	}
	if invs, _ := f.st.ListInvitations("person:1", 1); len(invs) != 2 { // nur die zwei aus roleAPI
		t.Fatalf("invitations = %d", len(invs))
	}
	if p, _ := f.st.ProjectByRemote(apiRoleProject); p.Org != "alpha" {
		t.Fatalf("project moved: %+v", p)
	}
	// Ohne Token: 401. Das Verlassen der eigenen Organisation bleibt erlaubt.
	f.mustCall(t, 401, "PUT", base+"/anna", "", map[string]any{"role": "owner"})
}

func TestAgentRoleOverAPIAndPeers(t *testing.T) {
	f, base := roleAPI(t)
	if err := f.st.SetProjectRole("person:1", apiRoleProject, "person:3", "member", true, store.RoleViaWeb); err != nil {
		t.Fatal(err)
	}
	_ = base
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
