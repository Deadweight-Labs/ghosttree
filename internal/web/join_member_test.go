package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A member who opens an invitation that gives them nothing is told so, with a
// way into the project, and the invitation stays open.
func TestJoinPageTellsAMemberTheyAreAlreadyIn(t *testing.T) {
	srv, st, _, org, _ := joinWeb(t)
	st.AddPerson("anna")
	anna := loginInteractive(t, srv, st, "anna")
	if _, err := st.AcceptInvitation("person:2", projectInvite(t, st, org, store.RoleMember)); err != nil {
		t.Fatal(err)
	}
	same := projectInvite(t, st, org, store.RoleMember)
	lower := projectInvite(t, st, org, store.RoleGuest)

	for name, code := range map[string]string{"same role": same, "lower role": lower} {
		resp, _ := anna.Get(srv.URL + "/join/" + code)
		page := body(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", name, resp.StatusCode)
		}
		for _, want := range []string{"You are already a member of", "app", `href="/ui/knowledge?project=` + url.QueryEscape(joinProject) + `"`} {
			if !strings.Contains(page, want) {
				t.Errorf("%s: page lacks %q: %s", name, want, page)
			}
		}
		if strings.Contains(page, `name="confirm_account"`) || strings.Contains(page, "/accept") {
			t.Errorf("%s: a member is offered to join again: %s", name, page)
		}
		if _, err := st.PreviewInvitation(code, true); err != nil {
			t.Errorf("%s: the invitation was consumed: %v", name, err)
		}
	}

	// Posting the accept form (an old tab) says the same and consumes nothing.
	resp := sameOriginPostForm(t, anna, srv.URL+"/join/"+same+"/accept",
		url.Values{"csrf_token": {renderedCSRFToken(t, anna, srv.URL+"/ui/orgs")}, "confirm_account": {"anna"}})
	if page := body(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(page, "You are already a member of") {
		t.Fatalf("accept as a member: %d %s", resp.StatusCode, page)
	}
	if _, err := st.PreviewInvitation(same, true); err != nil {
		t.Fatalf("the invitation was consumed by a member's accept: %v", err)
	}
}

// A guest who is offered the member role still gets the normal page: the
// invitation raises their role.
func TestJoinPageStillOffersAHigherRole(t *testing.T) {
	srv, st, _, org, _ := joinWeb(t)
	st.AddPerson("anna")
	anna := loginInteractive(t, srv, st, "anna")
	if _, err := st.AcceptInvitation("person:2", projectInvite(t, st, org, store.RoleGuest)); err != nil {
		t.Fatal(err)
	}
	resp, _ := anna.Get(srv.URL + "/join/" + projectInvite(t, st, org, store.RoleMember))
	if page := body(t, resp); strings.Contains(page, "You are already a member of") || !strings.Contains(page, `name="confirm_account"`) {
		t.Fatalf("a guest is not offered the member role: %s", page)
	}
}

// Someone who is not in the organization yet gets the normal page.
func TestJoinPageForANewcomerIsUnchanged(t *testing.T) {
	srv, st, _, org, _ := joinWeb(t)
	st.AddPerson("anna")
	anna := loginInteractive(t, srv, st, "anna")
	resp, _ := anna.Get(srv.URL + "/join/" + projectInvite(t, st, org, store.RoleMember))
	if page := body(t, resp); strings.Contains(page, "You are already a member of") || !strings.Contains(page, `name="confirm_account"`) {
		t.Fatalf("newcomer page: %s", page)
	}
}

// Signing in through the invitation page as a member lands on the invitation
// page, which says so; it does not drop the person silently on the overview.
func TestOIDCSignInFromAnInvitationAsAMemberSaysSo(t *testing.T) {
	env, org := inviteEnv(t)
	if _, err := env.store.EnsureProject("person:1", joinProject); err != nil {
		t.Fatal(err)
	}
	invite := func() string {
		code, _, err := env.store.CreateProjectInvitation("person:1", org.ID, joinProject, store.RoleMember, 0)
		if err != nil {
			t.Fatal(err)
		}
		return code
	}
	env.idp.subject, env.idp.username, env.idp.email = "sub-anna", "anna", "anna@example.test"
	b := newBrowser(t)
	env.callback(t, b, env.startFlowWith(t, b, url.Values{"code": {invite()}, "join": {"1"}})).Body.Close()

	again := invite()
	b = newBrowser(t)
	resp := env.callback(t, b, env.startFlowWith(t, b, url.Values{"code": {again}, "join": {"1"}}))
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/join/"+again {
		t.Fatalf("member signing in from an invitation: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	page, err := b.Get(env.web.URL + "/join/" + again)
	if err != nil {
		t.Fatal(err)
	}
	if text := body(t, page); !strings.Contains(text, "You are already a member of") {
		t.Fatalf("the invitation page does not say so: %s", text)
	}
	if _, err := env.store.PreviewInvitation(again, false); err != nil {
		t.Fatalf("the invitation was consumed: %v", err)
	}
}
