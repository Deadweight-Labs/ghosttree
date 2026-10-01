package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const webRoleProject = "github.com/x/one"

func roleWeb(t *testing.T) (base string, st *store.Store, alice, anna *http.Client) {
	t.Helper()
	base, st, alice, anna, org := orgWeb(t)
	if _, err := st.ClaimProject("person:1", webRoleProject, "alpha"); err != nil {
		t.Fatal(err)
	}
	code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	return base, st, alice, anna
}

func TestOrgPageShowsAndChangesProjectRoles(t *testing.T) {
	base, st, alice, anna := roleWeb(t)
	resp, _ := alice.Get(base + "/ui/orgs?org=alpha")
	page := body(t, resp)
	for _, want := range []string{"Project roles", webRoleProject, "/ui/orgs/project/role", "organization owner", `value="lead"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("owner page lacks %q: %s", want, page)
		}
	}
	// Ein Member ohne Rolle sieht die Rollen, aber keine Auswahl.
	resp, _ = anna.Get(base + "/ui/orgs?org=alpha")
	page = body(t, resp)
	if !strings.Contains(page, "Project roles") || strings.Contains(page, "/ui/orgs/project/role") {
		t.Fatalf("role-less member page: %s", page)
	}

	form := func(who, role string, review bool) url.Values {
		f := url.Values{"org": {"alpha"}, "remote": {webRoleProject}, "account": {who}, "role": {role}}
		if review {
			f.Set("review", "1")
		}
		return f
	}
	resp = postOrg(t, alice, base, "/ui/orgs/project/role", form("person:2", "lead", true))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("owner sets role: %d", resp.StatusCode)
	}
	if got := st.ProjectRole(webRoleProject, "person:2"); got.Role != "lead" || !got.CanReview {
		t.Fatalf("role after web change: %+v", got)
	}
	resp, _ = anna.Get(base + "/ui/orgs?org=alpha")
	page = body(t, resp)
	if !strings.Contains(page, "/ui/orgs/project/role") || !strings.Contains(page, `value="member"`) {
		t.Fatalf("lead page lacks the role select: %s", page)
	}
	// Der Lead darf weder sich noch den Org-Owner anfassen noch einen Owner vergeben.
	for name, f := range map[string]url.Values{
		"self-promotion": form("person:2", "owner", false),
		"org owner":      form("person:1", "guest", false),
	} {
		resp = postOrg(t, anna, base, "/ui/orgs/project/role", f)
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusConflict {
			t.Fatalf("%s: %d", name, resp.StatusCode)
		}
	}
	if got := st.ProjectRole(webRoleProject, "person:2"); got.Role != "lead" {
		t.Fatalf("a rejected change went through: %+v", got)
	}
	// Remove und fremdes Projekt.
	if resp = postOrg(t, alice, base, "/ui/orgs/project/role", form("person:2", "none", false)); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove: %d", resp.StatusCode)
	}
	if got := st.ProjectRole(webRoleProject, "person:2"); got.Role != "" {
		t.Fatalf("role after removal: %+v", got)
	}
	f := form("person:2", "guest", false)
	f.Set("remote", "github.com/x/unknown")
	if resp = postOrg(t, alice, base, "/ui/orgs/project/role", f); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown project: %d", resp.StatusCode)
	}
}

func TestProjectRoleFormNeedsCSRFAndSameOrigin(t *testing.T) {
	base, st, alice, _ := roleWeb(t)
	csrf := renderedCSRFToken(t, alice, base+"/ui/orgs")
	form := url.Values{"org": {"alpha"}, "remote": {webRoleProject}, "account": {"person:2"}, "role": {"lead"}}
	for name, token := range map[string]string{"missing": "", "wrong": "nope"} {
		f := url.Values{}
		for k, v := range form {
			f[k] = v
		}
		if token != "" {
			f.Set("csrf_token", token)
		}
		if resp := sameOriginPostForm(t, alice, base+"/ui/orgs/project/role", f); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s token: %d", name, resp.StatusCode)
		}
	}
	f := url.Values{"csrf_token": {csrf}}
	for k, v := range form {
		f[k] = v
	}
	req, _ := http.NewRequest("POST", base+"/ui/orgs/project/role", strings.NewReader(f.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	if resp, err := alice.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %v %v", resp, err)
	}
	if got := st.ProjectRole(webRoleProject, "person:2"); got.Role != "" {
		t.Fatalf("a rejected form set a role: %+v", got)
	}
}

func TestCoordParticipantsShowRoles(t *testing.T) {
	_, st, _, _ := roleWeb(t)
	if err := st.SetProjectRole("person:1", webRoleProject, "person:2", "member", true, store.RoleViaWeb); err != nil {
		t.Fatal(err)
	}
	room := store.RoomKeyForProject(webRoleProject)
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:h:a", Provider: "claude", RoomKey: room, DisplayName: "A", PrincipalID: "person:2", Role: "lead"}); err != nil {
		t.Fatal(err)
	}
	peers, err := st.CoordPeers(room, "")
	if err != nil {
		t.Fatal(err)
	}
	r := store.CoordRoom{Key: room, Kind: store.RoomProject, Members: []string{"person:1", "person:2"}}
	parts := buildCoordParticipants(r, peers, nil, store.Principal{ID: "person:1", Label: "alice"}, nil)
	applyParticipantRoles(st, room, parts)
	got := map[string]coordParticipantView{}
	for _, p := range parts {
		got[p.ID] = p
	}
	if p := got["claude:h:a"]; p.Role != "member" || !p.CanReview {
		t.Errorf("agent = %+v", p)
	}
	if p := got["person:1"]; p.Role != "owner" {
		t.Errorf("owner = %+v", p)
	}
	if p := got["person:2"]; p.Role != "member" || !p.CanReview {
		t.Errorf("member = %+v", p)
	}
	// Ohne Projektraum bleiben Rollen leer.
	parts = buildCoordParticipants(r, peers, nil, store.Principal{ID: "person:1"}, nil)
	applyParticipantRoles(st, "machine:host", parts)
	for _, p := range parts {
		if p.ID != "claude:h:a" && p.Role != "" {
			t.Errorf("machine room participant has a role: %+v", p)
		}
	}
}
