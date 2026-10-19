package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Eine Sitzung aus eingefügtem Token ist lesend: das Token liegt in der
// Konfiguration jedes Rechners, auf dem ein Agent läuft.
func TestPastedTokenSessionIsReadOnly(t *testing.T) {
	srv, st, aliceTok := testWeb(t)
	if _, err := st.AddPerson("anna"); err != nil {
		t.Fatal(err)
	}
	org, err := st.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOrg("person:1", "Beta", "beta"); err != nil {
		t.Fatal(err)
	}
	code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimProject("person:1", webRoleProject, "alpha"); err != nil {
		t.Fatal(err)
	}
	pasted := login(t, srv, aliceTok)
	interactiveClient := loginInteractive(t, srv, st, "alice")

	// Lesen geht, Formulare fehlen.
	resp, _ := pasted.Get(srv.URL + "/ui/orgs?org=alpha")
	page := body(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(page, "Read-only") || !strings.Contains(page, "anna") || !strings.Contains(page, webRoleProject) {
		t.Fatalf("pasted-token org page: %d %s", resp.StatusCode, page)
	}
	for _, form := range []string{"/ui/orgs/project/role", "/ui/orgs/invite", "/ui/orgs/member/role", "/ui/orgs/member/remove", "/ui/orgs/project/move"} {
		if strings.Contains(page, `action="`+form) {
			t.Fatalf("pasted-token session sees the form %s", form)
		}
	}
	resp, _ = interactiveClient.Get(srv.URL + "/ui/orgs?org=alpha")
	page = body(t, resp)
	for _, form := range []string{"/ui/orgs/project/role", "/ui/orgs/invite", "/ui/orgs/member/role", "/ui/orgs/member/remove"} {
		if !strings.Contains(page, `action="`+form) {
			t.Fatalf("interactive session lacks the form %s", form)
		}
	}
	resp, _ = pasted.Get(srv.URL + "/ui/device")
	if page = body(t, resp); strings.Contains(page, `action="/ui/device"`) || !strings.Contains(page, "login") {
		t.Fatalf("device page for a pasted token: %s", page)
	}

	// Jedes Verwaltungsformular: 403 mit Erklärung, nichts ändert sich.
	forms := map[string]url.Values{
		"/ui/orgs/project/role":  {"org": {"alpha"}, "remote": {webRoleProject}, "account": {"person:2"}, "role": {"owner"}},
		"/ui/orgs/member/role":   {"org": {"alpha"}, "account": {"person:2"}, "role": {"owner"}},
		"/ui/orgs/member/remove": {"org": {"alpha"}, "account": {"person:2"}},
		"/ui/orgs/invite":        {"org": {"alpha"}, "role": {"owner"}},
		"/ui/orgs/invite/revoke": {"org": {"alpha"}, "id": {"1"}},
		"/ui/orgs/project/move":  {"org": {"alpha"}, "remote": {webRoleProject}, "to": {"beta"}},
		"/ui/device":             {"user_code": {"ABCD-EFGH"}},
		"/ui/device/decide":      {"user_code": {"ABCD-EFGH"}, "decision": {"approve"}},
	}
	for path, form := range forms {
		f := url.Values{"csrf_token": {renderedCSRFToken(t, pasted, srv.URL+"/ui/orgs")}}
		for k, v := range form {
			f[k] = v
		}
		resp := sameOriginPostForm(t, pasted, srv.URL+path, f)
		page := body(t, resp)
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(page, "read-only session") || !strings.Contains(page, "login-link") {
			t.Fatalf("%s with a pasted token: %d %s", path, resp.StatusCode, page)
		}
	}
	if got := st.ProjectRole(webRoleProject, "person:2"); got.Role != "" {
		t.Fatalf("role granted: %+v", got)
	}
	if st.OrgRole(org.ID, "person:2") != store.OrgMember {
		t.Fatal("membership changed")
	}
	if invs, _ := st.ListInvitations("person:1", org.ID); len(invs) != 1 {
		t.Fatalf("invitations = %d", len(invs))
	}
	if p, _ := st.ProjectByRemote(webRoleProject); p.Org != "alpha" {
		t.Fatalf("project moved: %+v", p)
	}

	// Dieselben Formulare aus einer Login-Link-Sitzung gehen.
	f := url.Values{"csrf_token": {renderedCSRFToken(t, interactiveClient, srv.URL+"/ui/orgs")}}
	for k, v := range forms["/ui/orgs/project/role"] {
		f[k] = v
	}
	f.Set("role", "member")
	if resp := sameOriginPostForm(t, interactiveClient, srv.URL+"/ui/orgs/project/role", f); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("interactive role change: %d", resp.StatusCode)
	}
	if got := st.ProjectRole(webRoleProject, "person:2"); got.Role != "member" {
		t.Fatalf("role after interactive change: %+v", got)
	}
}

// Fremde Tokens widerruft ein Administrator nur aus einer interaktiven Sitzung,
// eigene darf jede Sitzung widerrufen.
func TestPastedTokenCannotRevokeForeignTokens(t *testing.T) {
	srv, st, aliceTok := testWeb(t)
	if _, err := st.AddPerson("bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE persons SET is_admin=1 WHERE name='alice'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateToken("bob", store.TokenSpec{Label: "ci"}); err != nil {
		t.Fatal(err)
	}
	bobTokens, _ := st.ListTokens("bob")
	pasted := login(t, srv, aliceTok)
	csrf := renderedCSRFToken(t, pasted, srv.URL+"/ui/account/tokens")
	page, _ := pasted.Get(srv.URL + "/ui/account/tokens")
	if strings.Contains(body(t, page), "/ui/account/tokens/revoke") {
		t.Fatal("a pasted-token admin session shows revoke buttons")
	}
	resp := sameOriginPostForm(t, pasted, srv.URL+"/ui/account/tokens/revoke", url.Values{"token_id": {strconv.FormatInt(bobTokens[0].ID, 10)}, "csrf_token": {csrf}})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign revoke with a pasted token: %d", resp.StatusCode)
	}
	if tk, _ := st.TokenByID(bobTokens[0].ID); tk.RevokedAt != "" {
		t.Fatal("token was revoked")
	}
	interactiveClient := loginInteractive(t, srv, st, "alice")
	csrf = renderedCSRFToken(t, interactiveClient, srv.URL+"/ui/account/tokens")
	resp = sameOriginPostForm(t, interactiveClient, srv.URL+"/ui/account/tokens/revoke", url.Values{"token_id": {strconv.FormatInt(bobTokens[0].ID, 10)}, "csrf_token": {csrf}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("foreign revoke interactive: %d", resp.StatusCode)
	}
}
