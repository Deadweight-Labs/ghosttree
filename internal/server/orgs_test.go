package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type orgFixture struct {
	srv                    *httptest.Server
	st                     *store.Store
	robin, anna, ben, carl string // Tokens; robin ist Admin (person:1)
}

func newOrgFixture(t *testing.T) orgFixture {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := orgFixture{st: st}
	for _, p := range []struct {
		name, email string
		admin       bool
		tok         *string
	}{{"robin", "", true, &f.robin}, {"anna", "", false, &f.anna}, {"ben", "Ben@Example.test", false, &f.ben}, {"carl", "", false, &f.carl}} {
		if _, err := st.AddAccount(p.name, p.email, p.admin); err != nil {
			t.Fatal(err)
		}
		if *p.tok, _, err = st.CreateToken(p.name, store.TokenSpec{Label: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	f.srv = httptest.NewServer(New(st))
	t.Cleanup(f.srv.Close)
	return f
}

func (f orgFixture) call(t *testing.T, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	resp := req(t, method, f.srv.URL+path, token, body)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (f orgFixture) mustCall(t *testing.T, want int, method, path, token string, body any) map[string]any {
	t.Helper()
	code, out := f.call(t, method, path, token, body)
	if code != want {
		t.Fatalf("%s %s = %d %v, want %d", method, path, code, out, want)
	}
	return out
}

func TestOrgCreationIsAdminOnly(t *testing.T) {
	f := newOrgFixture(t)
	code, out := f.call(t, "POST", "/api/orgs", f.anna, map[string]any{"name": "Alpha"})
	if code != 403 || out["code"] != "admin_only" {
		t.Fatalf("non-admin: %d %v", code, out)
	}
	out = f.mustCall(t, 201, "POST", "/api/orgs", f.robin, map[string]any{"name": "Alpha Team"})
	if out["slug"] != "alpha-team" || out["role"] != "owner" {
		t.Fatalf("org = %v", out)
	}
	f.mustCall(t, 409, "POST", "/api/orgs", f.robin, map[string]any{"name": "Again", "slug": "alpha-team"})
	f.mustCall(t, 400, "POST", "/api/orgs", f.robin, map[string]any{"name": "X", "slug": "Bad Slug"})
	f.mustCall(t, 401, "POST", "/api/orgs", "", map[string]any{"name": "X"})
}

func TestOrgMembersInvitationsAndPrivacy(t *testing.T) {
	f := newOrgFixture(t)
	f.mustCall(t, 201, "POST", "/api/orgs", f.robin, map[string]any{"name": "Alpha", "slug": "alpha"})

	// Nichtmitglied: die Org existiert nicht.
	f.mustCall(t, 404, "GET", "/api/orgs/alpha/members", f.anna, nil)
	f.mustCall(t, 404, "POST", "/api/orgs/alpha/invitations", f.anna, map[string]any{})

	inv := f.mustCall(t, 201, "POST", "/api/orgs/alpha/invitations", f.robin, map[string]any{"role": "member"})
	code, _ := inv["code"].(string)
	if code == "" || strings.Contains(inv["invitation"].(map[string]any)["status"].(string), "accepted") {
		t.Fatalf("invitation = %v", inv)
	}
	// Die Liste zeigt keinen Code.
	_, list := f.call(t, "GET", "/api/orgs/alpha/invitations", f.robin, nil)
	if b, _ := json.Marshal(list); strings.Contains(string(b), code) {
		t.Fatal("the code must not appear in listings")
	}
	// Falscher Code, richtiger Code, zweite Verwendung.
	f.mustCall(t, 400, "POST", "/api/invitations/accept", f.anna, map[string]any{"code": "wrong"})
	f.mustCall(t, 200, "POST", "/api/invitations/accept", f.anna, map[string]any{"code": code})
	f.mustCall(t, 400, "POST", "/api/invitations/accept", f.ben, map[string]any{"code": code})

	// Mitglied sieht Mitglieder, lädt aber nicht ein und ändert keine Rollen.
	f.mustCall(t, 200, "GET", "/api/orgs/alpha/members", f.anna, nil)
	f.mustCall(t, 403, "POST", "/api/orgs/alpha/invitations", f.anna, map[string]any{})
	f.mustCall(t, 403, "GET", "/api/orgs/alpha/invitations", f.anna, nil)
	f.mustCall(t, 403, "PUT", "/api/orgs/alpha/members/anna", f.anna, map[string]any{"role": "owner"})
	f.mustCall(t, 403, "DELETE", "/api/orgs/alpha/members/robin", f.anna, nil)
	// Owner befördert, der letzte Owner bleibt.
	f.mustCall(t, 200, "PUT", "/api/orgs/alpha/members/anna", f.robin, map[string]any{"role": "owner"})
	f.mustCall(t, 200, "PUT", "/api/orgs/alpha/members/person:1", f.anna, map[string]any{"role": "member"})
	out := f.mustCall(t, 409, "PUT", "/api/orgs/alpha/members/anna", f.anna, map[string]any{"role": "member"})
	if out["code"] != "last_owner" {
		t.Fatalf("last owner: %v", out)
	}
	f.mustCall(t, 404, "PUT", "/api/orgs/alpha/members/nobody", f.anna, map[string]any{"role": "member"})
	f.mustCall(t, 204, "DELETE", "/api/orgs/alpha/members/robin", f.robin, nil)
	f.mustCall(t, 404, "GET", "/api/orgs/alpha/members", f.robin, nil)

	// Widerruf.
	inv = f.mustCall(t, 201, "POST", "/api/orgs/alpha/invitations", f.anna, map[string]any{"email": "ben@example.test"})
	id := int(inv["invitation"].(map[string]any)["id"].(float64))
	f.mustCall(t, 204, "DELETE", "/api/orgs/alpha/invitations/"+itoa(id), f.anna, nil)
	f.mustCall(t, 400, "POST", "/api/invitations/accept", f.ben, map[string]any{"code": inv["code"]})
}

func itoa(i int) string { return strconv.Itoa(i) }

// Eine an eine E-Mail gebundene Einladung geht über die API nur für ein Konto,
// dessen E-Mail der Betreiber oder ein verifizierter Claim gesetzt hat.
func TestAPIInvitationBoundToEmail(t *testing.T) {
	f := newOrgFixture(t)
	f.mustCall(t, 201, "POST", "/api/orgs", f.robin, map[string]any{"name": "Alpha", "slug": "alpha"})
	// Der Eingeladene muss die Adresse haben; carl hat keine.
	inv := f.mustCall(t, 201, "POST", "/api/orgs/alpha/invitations", f.robin, map[string]any{"email": "ben@example.test"})
	out := f.mustCall(t, 403, "POST", "/api/invitations/accept", f.carl, map[string]any{"code": inv["code"]})
	if out["code"] != "invitation_email" {
		t.Fatalf("account without a matching email: %v", out)
	}
	f.mustCall(t, 200, "POST", "/api/invitations/accept", f.ben, map[string]any{"code": inv["code"]})
}

func TestAPIInvitationBruteForceAndLimits(t *testing.T) {
	f := newOrgFixture(t)
	for i := 0; i < 5; i++ {
		f.mustCall(t, 400, "POST", "/api/invitations/accept", f.carl, map[string]any{"code": "guess"})
	}
	if code, out := f.call(t, "POST", "/api/invitations/accept", f.carl, map[string]any{"code": "guess"}); code != 429 || out["code"] != "too_many_attempts" {
		t.Fatalf("after five wrong codes: %d %v", code, out)
	}
	// Ein anderes Konto ist nicht betroffen.
	f.mustCall(t, 400, "POST", "/api/invitations/accept", f.anna, map[string]any{"code": "guess"})

	// Körper über der Grenze: 413, kein Speicher für beliebig große Bodies.
	big := strings.Repeat("a", maxOrgBody+10)
	if code, _ := f.call(t, "POST", "/api/invitations/accept", f.anna, map[string]any{"code": big}); code != 413 {
		t.Fatalf("oversized body: %d", code)
	}
	if code, _ := f.call(t, "POST", "/api/orgs", f.robin, map[string]any{"name": big}); code != 413 {
		t.Fatalf("oversized org body: %d", code)
	}
	f.mustCall(t, 404, "POST", "/api/orgs/x/invitations", f.robin, map[string]any{})
}

func TestProjectsClaimMoveAndImplicitAssignment(t *testing.T) {
	f := newOrgFixture(t)
	f.mustCall(t, 201, "POST", "/api/orgs", f.robin, map[string]any{"name": "Alpha", "slug": "alpha"})
	f.mustCall(t, 201, "POST", "/api/orgs", f.robin, map[string]any{"name": "Beta", "slug": "beta"})
	for _, org := range []string{"alpha", "beta"} {
		inv := f.mustCall(t, 201, "POST", "/api/orgs/"+org+"/invitations", f.robin, map[string]any{"role": "owner"})
		f.mustCall(t, 200, "POST", "/api/invitations/accept", f.ben, map[string]any{"code": inv["code"]})
	}
	inv := f.mustCall(t, 201, "POST", "/api/orgs/alpha/invitations", f.robin, map[string]any{})
	f.mustCall(t, 200, "POST", "/api/invitations/accept", f.anna, map[string]any{"code": inv["code"]})

	// Anna (einfaches Mitglied): der Upload geht durch, besetzt aber nichts.
	f.mustCall(t, 200, "POST", "/api/sessions", f.anna, map[string]any{"harness": "claude-code", "external_id": "s1",
		"scope": map[string]any{"project": "github.com/x/one", "machine": "annabox"}})
	if _, ok := f.st.ProjectByRemote("github.com/x/one"); ok {
		t.Fatal("a plain member's write must leave the project unclaimed")
	}
	// Robin (Owner) schreibt dieselbe Remote und gewinnt sie.
	f.mustCall(t, 200, "POST", "/api/sessions", f.robin, map[string]any{"harness": "claude-code", "external_id": "s0",
		"scope": map[string]any{"project": "github.com/x/one", "machine": "robinbox"}})
	if p, ok := f.st.ProjectByRemote("github.com/x/one"); !ok || p.Org != "alpha" {
		t.Fatalf("implicit assignment by an owner: %+v %v", p, ok)
	}
	// Ben: zwei Orgs. Erst ein Standard (Alpha, zuerst beigetreten), also geht es.
	f.mustCall(t, 200, "POST", "/api/knowledge", f.ben, map[string]any{"type": "note", "title": "t", "body": "b",
		"scope": map[string]any{"project": "github.com/x/two"}})
	if p, _ := f.st.ProjectByRemote("github.com/x/two"); p.Org != "alpha" {
		t.Fatalf("default org: %+v", p)
	}
	// Ohne Standard: nicht raten.
	if err := f.st.SetDefaultOrg("person:3", 0); err != nil {
		t.Fatal(err)
	}
	code, out := f.call(t, "POST", "/api/requests", f.ben, map[string]any{"type": "feature", "title": "r", "project": "github.com/x/three"})
	choices, _ := out["choices"].([]any)
	if code != 409 || out["code"] != "project_unclaimed" || len(choices) != 2 {
		t.Fatalf("several orgs without default: %d %v", code, out)
	}
	if _, ok := f.st.ProjectByRemote("github.com/x/three"); ok {
		t.Fatal("refused write left a project")
	}
	// Ein Mitglied kann nicht besetzen; ein Owner schon.
	if code, out := f.call(t, "POST", "/api/projects/claim", f.anna, map[string]any{"remote": "https://github.com/x/three.git", "org": "alpha"}); code != 403 || out["code"] != "not_org_owner" {
		t.Fatalf("claim by a plain member: %d %v", code, out)
	}
	if _, ok := f.st.ProjectByRemote("github.com/x/three"); ok {
		t.Fatal("a refused claim left a project")
	}
	f.mustCall(t, 200, "POST", "/api/projects/claim", f.robin, map[string]any{"remote": "https://github.com/x/three.git", "org": "beta"})
	f.mustCall(t, 201, "POST", "/api/requests", f.ben, map[string]any{"type": "feature", "title": "r", "project": "github.com/x/three"})
	// Carl hat keine Org: schreibt wie bisher, kein Projekt entsteht.
	f.mustCall(t, 201, "POST", "/api/requests", f.carl, map[string]any{"type": "feature", "title": "r", "project": "github.com/x/four"})
	if _, ok := f.st.ProjectByRemote("github.com/x/four"); ok {
		t.Fatal("an account without an org must not claim")
	}
	// Schreibzugriff in ein fremdes Projekt: 409, auch ohne Org. Der Owner
	// (wie der Prod-Collector von person:1) schreibt unverändert weiter.
	for name, tok := range map[string]string{"account without org": f.carl, "member of another org": f.anna} {
		for _, call := range []struct {
			path string
			body map[string]any
		}{
			{"/api/requests", map[string]any{"type": "feature", "title": "r", "project": "github.com/x/three"}},
			{"/api/knowledge", map[string]any{"type": "note", "title": "t", "body": "b", "scope": map[string]any{"project": "github.com/x/three"}}},
			{"/api/sessions", map[string]any{"harness": "claude-code", "external_id": "squat-" + name, "scope": map[string]any{"project": "github.com/x/three", "machine": "m-" + name}}},
		} {
			if code, out := f.call(t, "POST", call.path, tok, call.body); code != 409 || out["code"] != "project_claimed" {
				t.Fatalf("%s writing %s into a foreign project: %d %v", name, call.path, code, out)
			}
		}
	}
	f.mustCall(t, 200, "POST", "/api/sessions", f.robin, map[string]any{"harness": "claude-code", "external_id": "own", "scope": map[string]any{"project": "github.com/x/three", "machine": "robinbox"}})
	// Fremdes Projekt beanspruchen: 409. Verschieben: nur Owner beider Orgs.
	f.mustCall(t, 409, "POST", "/api/projects/claim", f.carl, map[string]any{"remote": "github.com/x/three"})
	f.mustCall(t, 403, "POST", "/api/projects/move", f.anna, map[string]any{"remote": "github.com/x/three", "org": "alpha"})
	out = f.mustCall(t, 200, "POST", "/api/projects/move", f.robin, map[string]any{"remote": "github.com/x/three", "org": "alpha"})
	if out["org"] != "alpha" {
		t.Fatalf("move: %v", out)
	}
	f.mustCall(t, 404, "POST", "/api/projects/move", f.robin, map[string]any{"remote": "github.com/x/none", "org": "alpha"})
	// Liste: nur Projekte der eigenen Orgs.
	resp := req(t, "GET", f.srv.URL+"/api/projects?org=beta", f.robin, nil)
	defer resp.Body.Close()
	var list []map[string]any
	json.NewDecoder(resp.Body).Decode(&list)
	if resp.StatusCode != http.StatusOK || len(list) != 0 {
		t.Fatalf("beta projects = %d %v", resp.StatusCode, list)
	}
	f.mustCall(t, 404, "GET", "/api/projects?org=nope", f.robin, nil)
}
