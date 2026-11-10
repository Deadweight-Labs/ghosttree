package server

import (
	"encoding/json"
	"errors"
	"io"
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

// invite stellt eine Einladung über den Store aus: die API tut das nicht mehr,
// das ist Sache der Weboberfläche. Die Antwort hat die Form der früheren.
func (f orgFixture) invite(t *testing.T, actor, org, email, role string) map[string]any {
	t.Helper()
	o, err := f.st.OrgByRef(org)
	if err != nil {
		t.Fatal(err)
	}
	code, inv, err := f.st.CreateInvitation(actor, o.ID, email, role, 0)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"code": code, "invitation": map[string]any{"id": float64(inv.ID), "status": inv.Status}}
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

	inv := f.invite(t, "person:1", "alpha", "", "member")
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
	f.mustCall(t, 403, "GET", "/api/orgs/alpha/invitations", f.anna, nil)
	// Owner befördert über die Weboberfläche (hier: Store); der letzte Owner bleibt.
	if err := f.st.SetOrgRole("person:1", 1, "person:2", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetOrgRole("person:2", 1, "person:1", "member"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetOrgRole("person:2", 1, "person:2", "member"); !errors.Is(err, store.ErrLastOrgOwner) {
		t.Fatalf("last owner: %v", err)
	}
	// Das eigene Verlassen geht über die API und nimmt nur eigene Macht.
	f.mustCall(t, 204, "DELETE", "/api/orgs/alpha/members/robin", f.robin, nil)
	f.mustCall(t, 404, "GET", "/api/orgs/alpha/members", f.robin, nil)

	// Widerruf.
	inv = f.invite(t, "person:2", "alpha", "ben@example.test", "")
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
	inv := f.invite(t, "person:1", "alpha", "ben@example.test", "")
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
}

func TestProjectsClaimMoveAndImplicitAssignment(t *testing.T) {
	f := newOrgFixture(t)
	f.mustCall(t, 201, "POST", "/api/orgs", f.robin, map[string]any{"name": "Alpha", "slug": "alpha"})
	f.mustCall(t, 201, "POST", "/api/orgs", f.robin, map[string]any{"name": "Beta", "slug": "beta"})
	for _, org := range []string{"alpha", "beta"} {
		inv := f.invite(t, "person:1", org, "", "member")
		f.mustCall(t, 200, "POST", "/api/invitations/accept", f.ben, map[string]any{"code": inv["code"]})
		o, err := f.st.OrgByRef(org)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.st.SetOrgRole("person:1", o.ID, "person:3", "owner"); err != nil {
			t.Fatal(err)
		}
	}
	inv := f.invite(t, "person:1", "alpha", "", "")
	f.mustCall(t, 200, "POST", "/api/invitations/accept", f.anna, map[string]any{"code": inv["code"]})

	// REQ-623: ohne Präfix und ohne bestätigtes eigenes GitHub-Login bleibt eine
	// neue Remote unbeansprucht; Sessions darin laufen ohne Projekt.
	for _, sess := range []struct{ tok, ext, machine string }{{f.anna, "s1", "annabox"}, {f.robin, "s0", "robinbox"}} {
		f.mustCall(t, 200, "POST", "/api/sessions", sess.tok, map[string]any{"harness": "claude-code", "external_id": sess.ext,
			"scope": map[string]any{"project": "github.com/x/one", "machine": sess.machine, "branch": "main"}})
	}
	if _, ok := f.st.ProjectByRemote("github.com/x/one"); ok {
		t.Fatal("a session claimed a remote")
	}
	var proj, branch string
	if err := f.st.DB().QueryRow(`SELECT project, branch FROM sessions WHERE external_id='s0'`).Scan(&proj, &branch); err != nil || proj != "" || branch != "" {
		t.Fatalf("session in an unclaimed remote kept it: %q %q %v", proj, branch, err)
	}
	f.mustCall(t, 200, "POST", "/api/knowledge", f.ben, map[string]any{"type": "note", "title": "t", "body": "b",
		"scope": map[string]any{"project": "github.com/x/two"}})
	if _, ok := f.st.ProjectByRemote("github.com/x/two"); ok {
		t.Fatal("a write claimed a remote")
	}
	// Jedes Mitglied legt in seiner Org an und wird Owner des Projekts (17 A,
	// #4376); wer nicht Mitglied ist, nicht.
	if out := f.mustCall(t, 200, "POST", "/api/projects/claim", f.anna, map[string]any{"remote": "https://gitlab.com/x/annas.git", "org": "alpha"}); out["org"] != "alpha" {
		t.Fatalf("claim by a plain member: %v", out)
	}
	if role := f.st.ProjectRole("gitlab.com/x/annas", "person:2"); role.Role != "owner" {
		t.Fatalf("the member who created it: %+v", role)
	}
	if code, out := f.call(t, "POST", "/api/projects/claim", f.carl, map[string]any{"remote": "https://gitlab.com/x/three.git", "org": "alpha"}); code != 403 || out["code"] != "not_org_member" {
		t.Fatalf("claim by a non-member: %d %v", code, out)
	}
	if _, ok := f.st.ProjectByRemote("gitlab.com/x/three"); ok {
		t.Fatal("a refused claim left a project")
	}
	f.mustCall(t, 200, "POST", "/api/projects/claim", f.robin, map[string]any{"remote": "https://gitlab.com/x/three.git", "org": "beta"})
	f.mustCall(t, 201, "POST", "/api/requests", f.ben, map[string]any{"type": "feature", "title": "r", "project": "gitlab.com/x/three"})
	// Carl hat keine Org: sein Auftrag wird gespeichert, die Remote bleibt frei,
	// bis er sie ausdrücklich beansprucht.
	f.mustCall(t, 201, "POST", "/api/requests", f.carl, map[string]any{"type": "feature", "title": "r", "project": "gitlab.com/x/four"})
	if _, ok := f.st.ProjectByRemote("gitlab.com/x/four"); ok {
		t.Fatal("a write claimed a remote")
	}
	if out := f.mustCall(t, 200, "POST", "/api/projects/claim", f.carl, map[string]any{"remote": "gitlab.com/x/four"}); out["org_kind"] != "user" {
		t.Fatalf("carl's explicit claim: %v", out)
	}
	// On GitHub the login decides (review point 1): carl takes nobody's repository.
	if code, out := f.call(t, "POST", "/api/projects/claim", f.carl, map[string]any{"remote": "github.com/alice/repo"}); code != 403 || out["code"] != "not_github_owner" {
		t.Fatalf("carl claims alice's repository: %d %v", code, out)
	}
	// Wegwerf-Remotes: 400 für Einträge, eine Session wird ohne Projekt gespeichert.
	if code, out := f.call(t, "POST", "/api/requests", f.robin, map[string]any{"type": "feature", "title": "r", "project": "/home/r/.cache/live-1/origin"}); code != 400 || out["code"] != "invalid_remote" {
		t.Fatalf("throwaway remote: %d %v", code, out)
	}
	f.mustCall(t, 200, "POST", "/api/sessions", f.robin, map[string]any{"harness": "claude-code", "external_id": "tmp",
		"scope": map[string]any{"project": "benchmark.invalid/x", "machine": "robinbox"}})
	if _, ok := f.st.ProjectByRemote("benchmark.invalid/x"); ok {
		t.Fatal("a test session created a project")
	}
	// Schreibzugriff in ein fremdes Projekt: 409, auch ohne Org (Sessions siehe
	// oben: sie bleiben, ohne Projekt). Der Owner
	// (wie der Prod-Collector von person:1) schreibt unverändert weiter.
	for name, tok := range map[string]string{"account without org": f.carl, "member of another org": f.anna} {
		for _, call := range []struct {
			path string
			body map[string]any
		}{
			{"/api/requests", map[string]any{"type": "feature", "title": "r", "project": "gitlab.com/x/three"}},
			{"/api/knowledge", map[string]any{"type": "note", "title": "t", "body": "b", "scope": map[string]any{"project": "gitlab.com/x/three"}}},
		} {
			if code, out := f.call(t, "POST", call.path, tok, call.body); code != 409 || out["code"] != "project_claimed" {
				t.Fatalf("%s writing %s into a foreign project: %d %v", name, call.path, code, out)
			}
		}
	}
	f.mustCall(t, 200, "POST", "/api/sessions", f.robin, map[string]any{"harness": "claude-code", "external_id": "own", "scope": map[string]any{"project": "gitlab.com/x/three", "machine": "robinbox"}})
	// Fremdes Projekt beanspruchen: 409. Verschieben: nur Owner beider Orgs.
	f.mustCall(t, 409, "POST", "/api/projects/claim", f.carl, map[string]any{"remote": "gitlab.com/x/three"})
	// Verschieben geht nur über die Weboberfläche.
	f.mustCall(t, 403, "POST", "/api/projects/move", f.robin, map[string]any{"remote": "gitlab.com/x/three", "org": "alpha"})
	if p, err := f.st.MoveProject("person:1", "gitlab.com/x/three", "alpha"); err != nil || p.Org != "alpha" {
		t.Fatalf("move: %+v %v", p, err)
	}
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

func TestProjectInvitationsNeedAWebSessionAndGuestsSeeFewMembers(t *testing.T) {
	f := newOrgFixture(t)
	f.st.SetAccessMode(store.AccessMode{Enforce: true})
	f.mustCall(t, 201, "POST", "/api/orgs", f.robin, map[string]any{"name": "Alpha", "slug": "alpha"})
	if _, err := claimInOrg(f.st, "person:1", "github.com/alpha/app"); err != nil {
		t.Fatal(err)
	}
	o, _ := f.st.OrgByRef("alpha")
	guest, _, err := f.st.CreateProjectInvitation("person:1", o.ID, "github.com/alpha/app", store.RoleGuest, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Ein unbekannter und ein verbrauchter Code bekommen dieselbe Antwort.
	f.mustCall(t, 400, "POST", "/api/invitations/accept", f.anna, map[string]any{"code": strings.Repeat("ab", 32)})
	out := f.mustCall(t, 403, "POST", "/api/invitations/accept", f.anna, map[string]any{"code": guest})
	if out["code"] != "web_session_required" {
		t.Fatalf("token accept of a project invitation: %v", out)
	}
	if _, err := f.st.PreviewInvitation(guest, true); err != nil {
		t.Fatal("the refused token accept consumed the invitation")
	}
	// Im Browser angenommen (Store), sieht der Gast über die API nur sich und den Owner.
	if _, err := f.st.AcceptInvitation("person:2", guest); err != nil {
		t.Fatal(err)
	}
	member, _, _ := f.st.CreateProjectInvitation("person:1", o.ID, "github.com/alpha/app", store.RoleMember, 0)
	if _, err := f.st.AcceptInvitation("person:3", member); err != nil {
		t.Fatal(err)
	}
	req := req(t, "GET", f.srv.URL+"/api/orgs/alpha/members", f.anna, nil)
	defer req.Body.Close()
	var list []map[string]any
	if err := json.NewDecoder(req.Body).Decode(&list); err != nil || len(list) != 2 {
		t.Fatalf("guest sees %v %v", list, err)
	}
	for _, m := range list {
		if m["account"] == "ben" {
			t.Fatal("the guest sees another member")
		}
		if m["account"] == "robin" && m["account_id"] != nil {
			t.Fatalf("the owner entry carries an id: %v", m)
		}
	}
}

// PAIN-67: joining a project room (ctx coord inbox does it before it reads) is
// no work on the project and creates none.
func TestJoiningARoomCreatesNoProject(t *testing.T) {
	f := newOrgFixture(t)
	// A real-looking remote: a reserved test owner would be refused outright.
	const remote = "github.com/somebody/unknown-repo-xyz"
	f.mustCall(t, 200, "POST", "/api/coord/agents", f.robin, map[string]any{"external_id": "claude:join-1", "provider": "claude",
		"room_key": "project:" + remote, "display_name": "a"})
	if _, ok := f.st.ProjectByRemote(remote); ok {
		t.Fatal("joining the room created a project")
	}
	// An explicit claim makes it a project; only then is a join checked.
	f.mustCall(t, 200, "POST", "/api/projects/claim", f.robin, map[string]any{"remote": remote})
	if p, ok := f.st.ProjectByRemote(remote); !ok || p.OrgKind != "user" {
		t.Fatalf("the claim did not create it in the own namespace: %+v %v", p, ok)
	}
	// A known project of somebody else stays closed to a room join.
	if code, out := f.call(t, "POST", "/api/coord/agents", f.carl, map[string]any{"external_id": "claude:join-2", "provider": "claude",
		"room_key": "project:" + remote, "display_name": "b"}); code != 409 || out["code"] != "project_claimed" {
		t.Fatalf("foreign join: %d %v", code, out)
	}
}

// Review point 4: the room of an unclaimed remote opens only to accounts that
// worked under it (own sessions or entries) and to the instance admin.
func TestRoomOfAnUnclaimedRemoteOpensOnlyToThoseWhoWorkedThere(t *testing.T) {
	f := newOrgFixture(t)
	f.st.SetAccessMode(store.AccessMode{Enforce: true})
	const remote = "github.com/somebody/quiet-repo"
	room := map[string]any{"external_id": "claude:carl-1", "provider": "claude", "room_key": "project:" + remote, "display_name": "c"}
	read := "/api/coord/messages?destination_id=" + store.RoomKeyForProject(remote) + "&agent_external_id=claude:carl-1"
	f.call(t, "POST", "/api/coord/agents", f.carl, room)
	if code, _ := f.call(t, "GET", read, f.carl, nil); code == 200 {
		t.Fatal("a stranger reads the room of an unclaimed remote")
	}
	// Sessions in an unclaimed remote run without the project; an entry keeps it.
	f.mustCall(t, 200, "POST", "/api/knowledge", f.carl, map[string]any{"type": "note", "title": "t", "body": "b",
		"scope": map[string]any{"project": remote}})
	f.mustCall(t, 200, "POST", "/api/coord/agents", f.carl, room)
	if code, out := f.call(t, "GET", read, f.carl, nil); code != 200 {
		t.Fatalf("own work there, room closed: %d %v", code, out)
	}
	if code, _ := f.call(t, "GET", "/api/coord/messages?destination_id="+store.RoomKeyForProject(remote)+"&agent_external_id=claude:anna-1", f.anna, nil); code == 200 {
		t.Fatal("another stranger reads it")
	}
}

// Review point 2 over the API: an agent learns how many entries a project
// holds back, never their titles; deciding them is the web's. Ownership never
// moves over the API (third review, point 1).
func TestHeldEntriesOverTheAPI(t *testing.T) {
	f := newOrgFixture(t)
	f.st.SetAccessMode(store.AccessMode{Enforce: true})
	f.mustCall(t, 201, "POST", "/api/orgs", f.robin, map[string]any{"name": "Alpha", "slug": "alpha"})
	const remote = "gitlab.com/alpha/held"
	k := f.mustCall(t, 200, "POST", "/api/knowledge", f.carl, map[string]any{"type": "note", "title": "planted", "body": "b",
		"scope": map[string]any{"project": remote}})
	if code, out := f.call(t, "POST", "/api/projects/claim", f.robin, map[string]any{"remote": remote, "org": "alpha", "force": true}); code != 403 || out["code"] != "web_session_required" {
		t.Fatalf("forced claim over the API: %d %v", code, out)
	}
	if _, err := f.st.ClaimContested("person:1", remote, "alpha"); err != nil {
		t.Fatal(err)
	}
	if code, out := f.call(t, "POST", "/api/projects/reassign", f.robin, map[string]any{"remote": remote, "to": "robin"}); code != 403 || out["code"] != "web_session_required" {
		t.Fatalf("reassign over the API: %d %v", code, out)
	}
	resp := req(t, "GET", f.srv.URL+"/api/projects/held?project="+remote, f.robin, nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(raw), `"held":1`) || strings.Contains(string(raw), "planted") {
		t.Fatalf("held over the API: %s", raw)
	}
	if code, _ := f.call(t, "GET", "/api/projects/held?project="+remote, f.carl, nil); code != 404 {
		t.Fatalf("an outsider counts held entries: %d", code)
	}
	id := int64(k["id"].(float64))
	if code, out := f.call(t, "POST", "/api/projects/held", f.robin, map[string]any{"kind": "knowledge", "id": id, "action": "release"}); code != 403 || out["code"] != "web_session_required" {
		t.Fatalf("released over the API: %d %v", code, out)
	}
}
