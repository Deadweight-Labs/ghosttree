package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// channelAction is one writing action as the browser and the API (which the
// CLI and MCP call) offer it. done reports whether attempt n took effect, so
// the test reads the outcome from the store, not from a status code.
type channelAction struct {
	name  string
	right store.Right
	org   bool
	web   func(n int) (path string, form url.Values)
	api   func(n int) (method, path string, body any)
	done  func(t *testing.T, n int) bool
	// reset runs before each attempt, when the action changes a state that the
	// next attempt needs back.
	reset func(t *testing.T)
}

// TestSameRightInBrowserAndAPI: every writing action asks the same named right
// in the browser, from a pasted token in the browser and over the API (AC-722.3).
// An action goes through exactly when the account holds the right and, for a
// human right, the session comes from a sign-in (DEC-50).
func TestSameRightInBrowserAndAPI(t *testing.T) {
	srv, st, aliceTok := testWeb(t)
	st.SetAccessMode(store.AccessMode{Enforce: true})
	api := httptest.NewServer(server.New(st))
	t.Cleanup(api.Close)
	const project = "github.com/alpha/rights"
	annaTok, _ := st.AddPerson("anna")
	ginaTok, _ := st.AddPerson("gina")
	org, err := st.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPerson("dora"); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"person:2", "person:3", "person:4"} {
		code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
		if _, err := st.AcceptInvitation(who, code); err != nil {
			t.Fatal(err)
		}
	}
	proj, err := st.ClaimProject("person:1", project, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectRole("person:1", project, "person:2", store.RoleMember, false, store.RoleViaCLIDB); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectRole("person:1", project, "person:3", store.RoleGuest, false, store.RoleViaCLIDB); err != nil {
		t.Fatal(err)
	}
	owner := st.Access(store.Principal{ID: "person:1", Label: "alice"})
	invitations := map[int]int64{}
	name := func(kind string, n int) string { return kind + "-" + strconv.Itoa(n) }

	actions := []channelAction{
		{
			name: "package create", right: store.RightPackageManage,
			web: func(n int) (string, url.Values) {
				return "/ui/packages/create", url.Values{"project": {project}, "name": {name("pkg", n)}, "branch": {name("feat/pkg", n)}}
			},
			api: func(n int) (string, string, any) {
				return "POST", "/api/packages", map[string]any{"project": project, "name": name("pkg", n), "branch": name("feat/pkg", n)}
			},
			done: func(t *testing.T, n int) bool {
				list, err := owner.PackagesOfProject(project, "")
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range list {
					if p.Name == name("pkg", n) {
						return true
					}
				}
				return false
			},
		},
		{
			name: "feature create", right: store.RightFeatureWrite,
			web: func(n int) (string, url.Values) {
				return "/ui/features/create", url.Values{"project": {project}, "name": {name("Feature", n)}, "summary": {"what it does"}, "behavior": {"how it behaves"}}
			},
			api: func(n int) (string, string, any) {
				return "POST", "/api/features", map[string]any{"project": project, "name": name("Feature", n), "summary": "what it does", "behavior": "how it behaves"}
			},
			done: func(t *testing.T, n int) bool {
				list, err := owner.SearchFeatures(feature.Filter{Project: project})
				if err != nil {
					t.Fatal(err)
				}
				for _, f := range list {
					if f.Name == name("Feature", n) {
						return true
					}
				}
				return false
			},
		},
		{
			name: "org rename", right: store.RightOrgManage, org: true,
			web: func(n int) (string, url.Values) {
				return "/ui/orgs/rename", url.Values{"org": {"alpha"}, "name": {name("Alpha", n)}}
			},
			api: func(n int) (string, string, any) {
				return "PATCH", "/api/orgs/alpha", map[string]any{"name": name("Alpha", n)}
			},
			done: func(t *testing.T, n int) bool {
				o, err := st.OrgByRef("alpha")
				return err == nil && o.Name == name("Alpha", n)
			},
		},
		{
			name: "project role", right: store.RightRolesGrant,
			web: func(n int) (string, url.Values) {
				return "/ui/orgs/project/role", url.Values{"org": {"alpha"}, "remote": {project}, "account": {"person:4"}, "role": {store.RoleMember}}
			},
			api: func(n int) (string, string, any) {
				return "PUT", "/api/projects/" + strconv.FormatInt(proj.ID, 10) + "/members/person:4", map[string]any{"role": store.RoleMember}
			},
			done: func(t *testing.T, n int) bool {
				return st.ProjectRole(project, "person:4").Role == store.RoleMember
			},
			reset: func(t *testing.T) {
				if err := st.SetProjectRole("person:1", project, "person:4", store.RoleGuest, false, store.RoleViaCLIDB); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "invitation revoke", right: store.RightOrgMembers, org: true,
			web: func(n int) (string, url.Values) {
				return "/ui/orgs/invite/revoke", url.Values{"org": {"alpha"}, "id": {strconv.FormatInt(invitations[n], 10)}}
			},
			api: func(n int) (string, string, any) {
				return "DELETE", "/api/orgs/alpha/invitations/" + strconv.FormatInt(invitations[n], 10), nil
			},
			done: func(t *testing.T, n int) bool {
				list, err := st.ListInvitations("person:1", org.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, inv := range list {
					if inv.ID == invitations[n] {
						return inv.Status == "revoked"
					}
				}
				return false
			},
		},
	}

	type actor struct {
		label, principal, token string
	}
	actors := []actor{{"alice", "person:1", aliceTok}, {"anna", "person:2", annaTok}, {"gina", "person:3", ginaTok}}
	n := 0
	for _, act := range actions {
		info, ok := store.RightOf(act.right)
		if !ok {
			t.Fatalf("%s: %s is not in the catalog", act.name, act.right)
		}
		for _, who := range actors {
			holds := st.ProjectRole(project, who.principal).Has(act.right)
			if act.org {
				holds = store.OrgRoleHas(st.OrgRole(org.ID, who.principal), act.right)
			}
			channels := []struct {
				name     string
				signedIn bool
				run      func(n int)
			}{
				{"api", false, func(n int) { apiCall(t, api.URL, who.token, act.api, n) }},
				{"pasted token", false, func(n int) { webCall(t, srv, login(t, srv, who.token), act.web, n) }},
				{"sign-in", true, func(n int) { webCall(t, srv, loginInteractive(t, srv, st, who.label), act.web, n) }},
			}
			for _, ch := range channels {
				n++
				if act.name == "invitation revoke" {
					_, inv, err := st.CreateInvitation("person:1", org.ID, "x"+strconv.Itoa(n)+"@example.test", store.OrgMember, 0)
					if err != nil {
						t.Fatal(err)
					}
					invitations[n] = inv.ID
				}
				if act.reset != nil {
					act.reset(t)
				}
				ch.run(n)
				want := holds && (!info.Human || ch.signedIn)
				if got := act.done(t, n); got != want {
					t.Errorf("%s by %s (%s): went through %v, want %v (holds %s: %v, human: %v)", act.name, who.label, ch.name, got, want, act.right, holds, info.Human)
				}
			}
		}
	}
}

func apiCall(t *testing.T, base, token string, spec func(int) (string, string, any), n int) {
	t.Helper()
	method, path, body := spec(n)
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, base+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func webCall(t *testing.T, srv *httptest.Server, client *http.Client, spec func(int) (string, url.Values), n int) {
	t.Helper()
	path, form := spec(n)
	form.Set("csrf_token", renderedCSRFToken(t, client, srv.URL+"/ui/orgs"))
	sameOriginPostForm(t, client, srv.URL+path, form).Body.Close()
}
