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

	// A plain member creates a project and owns it.
	out := f.mustCall(t, 201, "POST", "/api/projects", f.anna, map[string]any{"name": "infra", "org": "alpha"})
	if out["remote"] != "infra" || out["org"] != "alpha" {
		t.Fatalf("created = %v", out)
	}
	if role := f.st.ProjectRole("infra", "person:2").Role; role != "owner" {
		t.Errorf("creator role = %q", role)
	}

	// It shows up as a project the creator can write to, so ResolveProject finds it.
	resp := req(t, "GET", f.srv.URL+"/api/projects/writable", f.anna, nil)
	defer resp.Body.Close()
	var writable []string
	if err := json.NewDecoder(resp.Body).Decode(&writable); err != nil || len(writable) != 1 || writable[0] != "infra" {
		t.Fatalf("writable = %v, %v", writable, err)
	}

	// Name rules and the duplicate answer.
	if code, out := f.call(t, "POST", "/api/projects", f.anna, map[string]any{"name": "Not Valid"}); code != 400 || out["code"] != "invalid_input" {
		t.Errorf("invalid name: %d %v", code, out)
	}
	if code, out := f.call(t, "POST", "/api/projects", f.anna, map[string]any{"name": "infra"}); code != 409 || out["code"] != "project_name_taken" {
		t.Errorf("duplicate: %d %v", code, out)
	}
	// Someone outside the organization cannot create in it, and without any
	// organization there is nowhere to put the project.
	if code, out := f.call(t, "POST", "/api/projects", f.carl, map[string]any{"name": "mine", "org": "alpha"}); code != 403 || out["code"] != "not_org_member" {
		t.Errorf("outsider: %d %v", code, out)
	}
	if code, out := f.call(t, "POST", "/api/projects", f.carl, map[string]any{"name": "mine"}); code != 409 || out["code"] != "no_org" {
		t.Errorf("no organization: %d %v", code, out)
	}
}

func TestAWriteToAnUnknownProjectNameCreatesNothing(t *testing.T) {
	f := newOrgFixture(t)
	f.mustCall(t, 201, "POST", "/api/orgs", f.robin, map[string]any{"name": "Alpha", "slug": "alpha"})
	f.mustCall(t, 201, "POST", "/api/projects", f.robin, map[string]any{"name": "infra"})
	// A marker that names a project nobody created must not conjure it.
	code, out := f.call(t, "POST", "/api/knowledge", f.robin, map[string]any{"type": "note", "title": "t", "body": "b", "scope": map[string]any{"project": "ghost"}})
	if code != 404 || out["code"] != "project_not_found" {
		t.Fatalf("write into an unknown name: %d %v", code, out)
	}
	if _, ok := f.st.ProjectByRemote("ghost"); ok {
		t.Fatal("a project named ghost exists")
	}
}
