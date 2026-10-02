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

// roleForm schneidet das Rollenformular eines Kontos aus der Seite.
func roleForm(page, account string) string {
	marker := `name="account" value="` + account + `"><select name="role"`
	i := strings.Index(page, marker)
	if i < 0 {
		return ""
	}
	rest := page[i:]
	return rest[:strings.Index(rest, "</form>")]
}

func TestRoleSelectShowsTheCurrentRoleNeverOwnerByDefault(t *testing.T) {
	base, st, alice, anna := roleWeb(t)
	get := func(c *http.Client) string {
		resp, _ := c.Get(base + "/ui/orgs?org=alpha")
		return body(t, resp)
	}
	// Ohne Rolle: Platzhalter vorgewählt, nie owner.
	form := roleForm(get(alice), "person:2")
	if !strings.Contains(form, `<option value="" selected>`) || strings.Contains(form, `value="owner" selected`) || strings.Count(form, " selected") != 1 {
		t.Fatalf("role-less account: %s", form)
	}
	if err := st.SetProjectRole("person:1", webRoleProject, "person:2", "member", false, store.RoleViaWeb); err != nil {
		t.Fatal(err)
	}
	form = roleForm(get(alice), "person:2")
	if !strings.Contains(form, `<option value="member" selected>`) || strings.Contains(form, `value=""`) || strings.Count(form, " selected") != 1 {
		t.Fatalf("member: %s", form)
	}
	// Nur das Häkchen umschalten, die Rolle bleibt.
	resp := postOrg(t, alice, base, "/ui/orgs/project/role", url.Values{"org": {"alpha"}, "remote": {webRoleProject}, "account": {"person:2"}, "role": {"member"}, "review": {"1"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("toggle review: %d", resp.StatusCode)
	}
	if got := st.ProjectRole(webRoleProject, "person:2"); got.Role != "member" || !got.CanReview {
		t.Fatalf("after toggling the flag: %+v", got)
	}
	// Ein Lead sieht bei sich die eigene Stufe vorgewählt und kann das Häkchen
	// ablegen, ohne die Rolle zu ändern, aber sich nicht erhöhen.
	if err := st.SetProjectRole("person:1", webRoleProject, "person:2", "lead", true, store.RoleViaWeb); err != nil {
		t.Fatal(err)
	}
	form = roleForm(get(anna), "person:2")
	if !strings.Contains(form, `<option value="lead" selected>`) || strings.Contains(form, `value="owner"`) {
		t.Fatalf("lead's own row: %s", form)
	}
	resp = postOrg(t, anna, base, "/ui/orgs/project/role", url.Values{"org": {"alpha"}, "remote": {webRoleProject}, "account": {"person:2"}, "role": {"lead"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("lead drops the flag: %d", resp.StatusCode)
	}
	if got := st.ProjectRole(webRoleProject, "person:2"); got.Role != "lead" || got.CanReview {
		t.Fatalf("after dropping the flag: %+v", got)
	}
	resp = postOrg(t, anna, base, "/ui/orgs/project/role", url.Values{"org": {"alpha"}, "remote": {webRoleProject}, "account": {"person:2"}, "role": {"lead"}, "review": {"1"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("lead raising its own flag: %d", resp.StatusCode)
	}
	// Eine leere Wahl (Platzhalter) ändert nichts.
	if err := st.RemoveProjectRole("person:1", webRoleProject, "person:2", store.RoleViaWeb); err != nil {
		t.Fatal(err)
	}
	resp = postOrg(t, alice, base, "/ui/orgs/project/role", url.Values{"org": {"alpha"}, "remote": {webRoleProject}, "account": {"person:2"}, "role": {""}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("placeholder submitted: %d", resp.StatusCode)
	}
	if got := st.ProjectRole(webRoleProject, "person:2"); got.Role != "" {
		t.Fatalf("placeholder set a role: %+v", got)
	}
}

func TestCoordMessagesShowSenderRole(t *testing.T) {
	_, st, _, _ := roleWeb(t)
	if err := st.SetProjectRole("person:1", webRoleProject, "person:2", "member", false, store.RoleViaWeb); err != nil {
		t.Fatal(err)
	}
	room := store.RoomKeyForProject(webRoleProject)
	pres := []store.CoordMessagePresentation{
		{Message: store.CoordMessage{ID: 1, Sequence: 1, SenderExternalID: "person:1", AuthorPrincipalID: "person:1", AuthorKind: store.AuthorHuman, Body: "I am the lead"}},
		{Message: store.CoordMessage{ID: 2, Sequence: 2, SenderExternalID: "person:2", AuthorPrincipalID: "person:2", AuthorKind: store.AuthorHuman, Body: "hi"}},
		{Message: store.CoordMessage{ID: 3, Sequence: 3, SenderExternalID: "ghost", AuthorKind: store.AuthorAgent, Body: "hi"}},
	}
	views := buildCoordMessageViews(pres, room, nil)
	applyMessageRoles(st, room, views, pres)
	if views[0].SenderRole != "owner" || views[1].SenderRole != "member" || views[2].SenderRole != "" {
		t.Fatalf("roles = %q %q %q", views[0].SenderRole, views[1].SenderRole, views[2].SenderRole)
	}
	views = buildCoordMessageViews(pres, "machine:host", nil)
	applyMessageRoles(st, "machine:host", views, pres)
	for _, v := range views {
		if v.SenderRole != "" {
			t.Fatalf("machine room shows a role: %+v", v)
		}
	}
}

// Ein Gast sieht in der Rollentabelle nur den eigenen Eintrag, wie in der API.
func TestOrgPageRoleTableHidesMembersFromGuests(t *testing.T) {
	base, st, alice, anna := roleWeb(t)
	if err := st.SetProjectRole("person:1", webRoleProject, "person:2", store.RoleGuest, false, store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	st.SetAccessMode(store.AccessMode{Enforce: true})
	roles := func(c *http.Client) string {
		resp, _ := c.Get(base + "/ui/orgs?org=alpha")
		page := body(t, resp)
		i := strings.Index(page, "Project roles")
		if i < 0 {
			t.Fatalf("no role table: %s", page)
		}
		return page[i:]
	}
	got := roles(anna)
	if !strings.Contains(got, "anna") || strings.Contains(got, "alice") {
		t.Fatalf("guest must see only their own role row: %s", got)
	}
	if owner := roles(alice); !strings.Contains(owner, "alice") || !strings.Contains(owner, "anna") {
		t.Fatalf("owner lost rows: %s", owner)
	}
}
