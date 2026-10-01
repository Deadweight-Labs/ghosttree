package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func orgWeb(t *testing.T) (srvURL string, st *store.Store, alice, anna *http.Client, org store.Org) {
	t.Helper()
	srv, st, aliceTok := testWeb(t)
	annaTok, err := st.AddPerson("anna")
	if err != nil {
		t.Fatal(err)
	}
	if org, err = st.CreateOrg("person:1", "Alpha", "alpha"); err != nil {
		t.Fatal(err)
	}
	return srv.URL, st, login(t, srv, aliceTok), login(t, srv, annaTok), org
}

func postOrg(t *testing.T, c *http.Client, base, path string, form url.Values) *http.Response {
	t.Helper()
	form.Set("csrf_token", renderedCSRFToken(t, c, base+"/ui/orgs"))
	return sameOriginPostForm(t, c, base+path, form)
}

func TestOrgPageShowsMembersProjectsAndRoleDependentControls(t *testing.T) {
	base, st, alice, anna, org := orgWeb(t)
	if _, err := st.ClaimProject("person:1", "github.com/x/one", "alpha"); err != nil {
		t.Fatal(err)
	}
	code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if resp, _ := anon.Get(base + "/ui/orgs"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("anonymous: %d", resp.StatusCode)
	}
	resp, _ := alice.Get(base + "/ui/orgs")
	page := body(t, resp)
	for _, want := range []string{"Alpha", "alice", "anna", "github.com/x/one", "Create invitation", "Make owner"} {
		if !strings.Contains(page, want) {
			t.Fatalf("owner page lacks %q: %s", want, page)
		}
	}
	resp, _ = anna.Get(base + "/ui/orgs")
	page = body(t, resp)
	if !strings.Contains(page, "github.com/x/one") || strings.Contains(page, "Create invitation") || strings.Contains(page, "Make owner") {
		t.Fatalf("member page: %s", page)
	}
	if !strings.Contains(page, "Leave") {
		t.Fatal("a member can leave")
	}
}

func TestOrgFormsNeedCSRFAndSameOrigin(t *testing.T) {
	base, st, alice, _, org := orgWeb(t)
	st.ClaimProject("person:1", "github.com/x/one", "alpha")
	csrf := renderedCSRFToken(t, alice, base+"/ui/orgs")
	forms := map[string]url.Values{
		"/ui/orgs/invite":        {"org": {"alpha"}, "role": {"member"}},
		"/ui/orgs/member/role":   {"org": {"alpha"}, "account": {"person:1"}, "role": {"member"}},
		"/ui/orgs/member/remove": {"org": {"alpha"}, "account": {"person:1"}},
		"/ui/orgs/project/move":  {"org": {"alpha"}, "remote": {"github.com/x/one"}, "to": {"alpha"}},
		"/ui/orgs/invite/revoke": {"org": {"alpha"}, "id": {"1"}},
		"/ui/orgs/accept":        {"code": {"x"}},
		"/ui/orgs/default":       {"org": {"alpha"}},
	}
	for path, form := range forms {
		for name, token := range map[string]string{"missing": "", "wrong": "nope"} {
			f := url.Values{}
			for k, v := range form {
				f[k] = v
			}
			if token != "" {
				f.Set("csrf_token", token)
			}
			if resp := sameOriginPostForm(t, alice, base+path, f); resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s with %s token: %d", path, name, resp.StatusCode)
			}
		}
		// Gültiges Token, fremder Origin.
		f := url.Values{"csrf_token": {csrf}}
		for k, v := range form {
			f[k] = v
		}
		req, _ := http.NewRequest("POST", base+path, strings.NewReader(f.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "https://evil.example")
		if resp, err := alice.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s foreign origin: %v %v", path, resp, err)
		}
	}
	if members, _ := st.ListOrgMembers(org.ID); len(members) != 1 || members[0].Role != store.OrgOwner {
		t.Fatalf("a rejected form changed something: %+v", members)
	}
	if invs, _ := st.ListInvitations("person:1", org.ID); len(invs) != 0 {
		t.Fatalf("a rejected form created an invitation: %+v", invs)
	}
	// Ein Körper über der Grenze kommt nicht durch.
	big := url.Values{"csrf_token": {csrf}, "org": {"alpha"}, "email": {strings.Repeat("a", maxDeviceForm)}}
	if resp := sameOriginPostForm(t, alice, base+"/ui/orgs/invite", big); resp.StatusCode == http.StatusOK {
		t.Fatal("oversized form accepted")
	}
}

func TestOrgInviteAcceptRoleMoveAndRemoveInTheBrowser(t *testing.T) {
	base, st, alice, anna, org := orgWeb(t)
	beta, _ := st.CreateOrg("person:1", "Beta", "beta")
	st.ClaimProject("person:1", "github.com/x/one", "alpha")

	// Einladung: der Code erscheint nur in der Antwort auf das Formular.
	resp := postOrg(t, alice, base, "/ui/orgs/invite", url.Values{"org": {"alpha"}, "role": {"member"}, "days": {"3"}})
	page := body(t, resp)
	code := between(page, `<code class="org-code">`, `</code>`)
	if resp.StatusCode != 200 || len(code) != 64 {
		t.Fatalf("invite: %d %s", resp.StatusCode, page)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("the code page must not be cached")
	}
	again, _ := alice.Get(base + "/ui/orgs?org=alpha")
	if strings.Contains(body(t, again), code) {
		t.Fatal("the code must not be shown again")
	}
	// Anna löst ihn ein, falscher Code geht nicht.
	if resp := postOrg(t, anna, base, "/ui/orgs/accept", url.Values{"code": {"wrong"}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong code: %d", resp.StatusCode)
	}
	resp = postOrg(t, anna, base, "/ui/orgs/accept", url.Values{"code": {code}})
	if resp.StatusCode != http.StatusSeeOther || st.OrgRole(org.ID, "person:2") != store.OrgMember {
		t.Fatalf("accept: %d role=%q", resp.StatusCode, st.OrgRole(org.ID, "person:2"))
	}
	if resp := postOrg(t, anna, base, "/ui/orgs/accept", url.Values{"code": {code}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("reuse: %d", resp.StatusCode)
	}
	// Mitglied darf weder einladen noch befördern noch verschieben.
	for path, form := range map[string]url.Values{
		"/ui/orgs/invite":       {"org": {"alpha"}},
		"/ui/orgs/member/role":  {"org": {"alpha"}, "account": {"person:2"}, "role": {"owner"}},
		"/ui/orgs/project/move": {"org": {"alpha"}, "remote": {"github.com/x/one"}, "to": {"beta"}},
	} {
		if resp := postOrg(t, anna, base, path, form); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("member on %s: %d", path, resp.StatusCode)
		}
	}
	// Owner: Rolle, Verschieben, Entfernen.
	if resp := postOrg(t, alice, base, "/ui/orgs/member/role", url.Values{"org": {"alpha"}, "account": {"person:2"}, "role": {"owner"}}); resp.StatusCode != http.StatusSeeOther || st.OrgRole(org.ID, "person:2") != store.OrgOwner {
		t.Fatalf("role: %d", resp.StatusCode)
	}
	if resp := postOrg(t, alice, base, "/ui/orgs/project/move", url.Values{"org": {"alpha"}, "remote": {"github.com/x/one"}, "to": {"beta"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("move: %d", resp.StatusCode)
	}
	if p, _ := st.ProjectByRemote("github.com/x/one"); p.OrgID != beta.ID {
		t.Fatalf("project not moved: %+v", p)
	}
	if resp := postOrg(t, alice, base, "/ui/orgs/member/remove", url.Values{"org": {"alpha"}, "account": {"person:2"}}); resp.StatusCode != http.StatusSeeOther || st.OrgRole(org.ID, "person:2") != "" {
		t.Fatalf("remove: %d", resp.StatusCode)
	}
	// Der letzte Owner bleibt.
	if resp := postOrg(t, alice, base, "/ui/orgs/member/remove", url.Values{"org": {"alpha"}, "account": {"person:1"}}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("last owner: %d", resp.StatusCode)
	}
	// Eine fremde Organisation ist nicht vorhanden.
	if resp := postOrg(t, anna, base, "/ui/orgs/default", url.Values{"org": {"alpha"}}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign org: %d", resp.StatusCode)
	}
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	j := strings.Index(s, b)
	if j < 0 {
		return ""
	}
	return s[:j]
}

// Ohne IdP erzeugt ein Einladungslink per Code und gewähltem Namen ein Konto,
// das Mitglied ist und angemeldet wird.
func TestLocalInvitationLinkCreatesAccountAndSession(t *testing.T) {
	srv, st, _ := testWeb(t)
	org, _ := st.CreateOrg("person:1", "Alpha", "alpha")
	code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	b := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, _ := b.Get(srv.URL + "/ui/login/code?code=" + code)
	page := body(t, resp)
	if !strings.Contains(page, "Join an organization") || !strings.Contains(page, `name="name"`) {
		t.Fatalf("landing page: %s", page)
	}
	// Ein GET verbraucht nichts.
	if st.CodeKindFor(code) != store.CodeInvitation {
		t.Fatal("viewing the link used the code")
	}
	resp = sameOriginPostForm(t, b, srv.URL+"/ui/login/code", url.Values{"code": {code}, "name": {"newbie"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("redeem: %d", resp.StatusCode)
	}
	acct, err := st.AccountByName("newbie")
	if err != nil || st.OrgRole(org.ID, acct.ID) != store.OrgMember {
		t.Fatalf("account %+v err %v", acct, err)
	}
	var session bool
	for _, c := range resp.Cookies() {
		session = session || c.Name == sessionCookie
	}
	if !session {
		t.Fatal("no session after redeeming")
	}
	resp = sameOriginPostForm(t, b, srv.URL+"/ui/login/code", url.Values{"code": {code}, "name": {"again"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reuse: %d", resp.StatusCode)
	}
}

func TestOIDCInvitationLandingPointsToTheIdentityProvider(t *testing.T) {
	env, org := inviteEnv(t)
	code := env.invite(t, org, "")
	resp, _ := http.Get(env.web.URL + "/ui/login/code?code=" + code)
	page := body(t, resp)
	if !strings.Contains(page, "identity provider") || !strings.Contains(page, `action="/ui/login/oidc"`) || strings.Contains(page, `name="name"`) {
		t.Fatalf("landing page: %s", page)
	}
	b := newBrowser(t)
	resp = sameOriginPostForm(t, b, env.web.URL+"/ui/login/code", url.Values{"code": {code}, "name": {"x"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("local redemption on an OIDC instance: %d", resp.StatusCode)
	}
}
