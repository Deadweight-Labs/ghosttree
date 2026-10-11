package server

import (
	"encoding/json"
	"testing"
)

func TestNamedProjectCreationOverTheAPI(t *testing.T) {
	f := newOrgFixture(t)
	f.mustCall(t, 201, "POST", "/api/orgs", f.robin, map[string]any{"name": "Alpha", "slug": "alpha"})
	inv := f.invite(t, "person:1", "alpha", "", "member")
	f.mustCall(t, 200, "POST", "/api/invitations/accept", f.anna, map[string]any{"code": inv["code"]})

	// A plain member creates a project and owns it; the id carries the organization.
	out := f.mustCall(t, 201, "POST", "/api/projects", f.anna, map[string]any{"name": "infra", "org": "alpha"})
	if out["remote"] != "alpha/infra" || out["org"] != "alpha" {
		t.Fatalf("created = %v", out)
	}
	if role := f.st.ProjectRole("alpha/infra", "person:2").Role; role != "owner" {
		t.Errorf("creator role = %q", role)
	}

	// It shows up as a project the creator can write to.
	resp := req(t, "GET", f.srv.URL+"/api/projects/writable", f.anna, nil)
	defer resp.Body.Close()
	var writable []string
	if err := json.NewDecoder(resp.Body).Decode(&writable); err != nil || len(writable) != 1 || writable[0] != "alpha/infra" {
		t.Fatalf("writable = %v, %v", writable, err)
	}

	if code, out := f.call(t, "POST", "/api/projects", f.anna, map[string]any{"name": "Not Valid"}); code != 400 || out["code"] != "invalid_input" {
		t.Errorf("invalid name: %d %v", code, out)
	}
	if code, out := f.call(t, "POST", "/api/projects", f.anna, map[string]any{"name": "a/b"}); code != 400 || out["code"] != "invalid_input" {
		t.Errorf("a path as a name: %d %v", code, out)
	}
	if code, out := f.call(t, "POST", "/api/projects", f.anna, map[string]any{"name": "infra"}); code != 409 || out["code"] != "project_name_taken" {
		t.Errorf("duplicate in the own organization: %d %v", code, out)
	}
	if code, out := f.call(t, "POST", "/api/projects", f.carl, map[string]any{"name": "mine", "org": "alpha"}); code != 403 || out["code"] != "not_org_member" {
		t.Errorf("outsider: %d %v", code, out)
	}
	if code, out := f.call(t, "POST", "/api/projects", f.carl, map[string]any{"name": "mine"}); code != 409 || out["code"] != "no_org" {
		t.Errorf("no organization: %d %v", code, out)
	}
}

// The same short name in two organizations is two projects, and an outsider
// cannot tell "belongs to someone else" from "does not exist".
func TestNamedProjectsOfOtherOrganizationsAreInvisible(t *testing.T) {
	f := newOrgFixture(t)
	f.mustCall(t, 201, "POST", "/api/orgs", f.robin, map[string]any{"name": "Alpha", "slug": "alpha"})
	f.mustCall(t, 201, "POST", "/api/orgs", f.robin, map[string]any{"name": "Beta", "slug": "beta"})
	f.mustCall(t, 201, "POST", "/api/projects", f.robin, map[string]any{"name": "infra", "org": "alpha"})
	f.mustCall(t, 201, "POST", "/api/projects", f.robin, map[string]any{"name": "infra", "org": "beta"})
	inv := f.invite(t, "person:1", "beta", "", "member")
	f.mustCall(t, 200, "POST", "/api/invitations/accept", f.anna, map[string]any{"code": inv["code"]})

	write := func(token, project string) (int, map[string]any) {
		return f.call(t, "POST", "/api/knowledge", token, map[string]any{"type": "note", "title": "t", "body": "b", "scope": map[string]any{"project": project}})
	}
	// Anna is in beta only: alpha/infra answers exactly like a project that is not there.
	codeOther, outOther := write(f.anna, "alpha/infra")
	codeNone, outNone := write(f.anna, "alpha/none")
	if codeOther != 404 || codeNone != 404 || outOther["code"] != "project_not_found" || outNone["code"] != "project_not_found" || outOther["message"] != outNone["message"] {
		t.Errorf("foreign %d %v, unknown %d %v", codeOther, outOther, codeNone, outNone)
	}
}

func TestAWriteToAnUnknownNamedProjectCreatesNothing(t *testing.T) {
	f := newOrgFixture(t)
	f.mustCall(t, 201, "POST", "/api/orgs", f.robin, map[string]any{"name": "Alpha", "slug": "alpha"})
	code, out := f.call(t, "POST", "/api/knowledge", f.robin, map[string]any{"type": "note", "title": "t", "body": "b", "scope": map[string]any{"project": "alpha/ghost"}})
	if code != 404 || out["code"] != "project_not_found" {
		t.Fatalf("write into an unknown name: %d %v", code, out)
	}
	if _, ok := f.st.ProjectByRemote("alpha/ghost"); ok {
		t.Fatal("a project named ghost exists")
	}
}

// Account (cli), legacy and browser sessions create projects. There is no agent
// token kind and no MCP tool for it: agents act through the token of their
// person and have no way to ask for a project.
func TestProjectCreationWorksForCLIAndLegacyTokens(t *testing.T) {
	f := newOrgFixture(t)
	f.mustCall(t, 201, "POST", "/api/orgs", f.robin, map[string]any{"name": "Alpha", "slug": "alpha"})
	legacy, err := f.st.AddPerson("dora")
	if err != nil {
		t.Fatal(err)
	}
	inv := f.invite(t, "person:1", "alpha", "", "member")
	f.mustCall(t, 200, "POST", "/api/invitations/accept", legacy, map[string]any{"code": inv["code"]})
	f.mustCall(t, 201, "POST", "/api/projects", f.robin, map[string]any{"name": "cli-made", "org": "alpha"})
	f.mustCall(t, 201, "POST", "/api/projects", legacy, map[string]any{"name": "legacy-made", "org": "alpha"})
}
