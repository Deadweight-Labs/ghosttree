package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestProfileRenamingIsRateLimitedAndRefusesMixedScripts(t *testing.T) {
	srv, st, _ := testWeb(t)
	c := loginInteractive(t, srv, st, "alice")
	csrf := renderedCSRFToken(t, c, srv.URL+"/ui/profile")
	post := func(name string) int {
		return sameOriginPostForm(t, c, srv.URL+"/ui/profile", url.Values{"name": {name}, "csrf_token": {csrf}}).StatusCode
	}
	if got := post("Рeter"); got != http.StatusBadRequest {
		t.Fatalf("mixed script: %d", got)
	}
	for _, n := range []string{"Al1", "Al2", "Al3"} {
		if got := post(n); got != http.StatusSeeOther {
			t.Fatalf("%s: %d", n, got)
		}
	}
	resp := sameOriginPostForm(t, c, srv.URL+"/ui/profile", url.Values{"name": {"Al4"}, "csrf_token": {csrf}})
	if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(body(t, resp), msg("profile.err.rate")) {
		t.Fatalf("fourth change: %d", resp.StatusCode)
	}
}

func TestGuestProfileHasNoNameForm(t *testing.T) {
	srv, st, _ := testWeb(t)
	if _, err := st.AddPerson("gus"); err != nil {
		t.Fatal(err)
	}
	org, err := st.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	const project = "github.com/dw/guestname"
	if _, err := st.EnsureProject("person:1", project); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectRole("person:1", project, "person:2", store.RoleGuest, false, store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	c := loginInteractive(t, srv, st, "gus")
	resp, _ := c.Get(srv.URL + "/ui/profile")
	if page := body(t, resp); strings.Contains(page, `name="name"`) || !strings.Contains(page, msg("profile.err.locked")) {
		t.Fatalf("guest sees the form: %s", page)
	}
	resp = sameOriginPostForm(t, c, srv.URL+"/ui/profile", url.Values{"name": {"Gustav"}, "csrf_token": {renderedCSRFToken(t, c, srv.URL+"/ui/profile")}})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("guest rename: %d", resp.StatusCode)
	}
	if a, _ := st.AccountByPrincipalID("person:2"); a.Name != "gus" {
		t.Fatalf("name changed: %q", a.Name)
	}
}

func TestOrgInviteFormOffersNoOwnerRole(t *testing.T) {
	srv, st, _ := testWeb(t)
	if _, err := st.CreateOrg("person:1", "Alpha", "alpha"); err != nil {
		t.Fatal(err)
	}
	c := loginInteractive(t, srv, st, "alice")
	resp, _ := c.Get(srv.URL + "/ui/orgs?org=alpha")
	page := body(t, resp)
	if !strings.Contains(page, `action="/ui/orgs/invite"`) || strings.Contains(page, `<option value="owner">`) {
		t.Fatalf("invite form: owner offered or form missing")
	}
	resp = sameOriginPostForm(t, c, srv.URL+"/ui/orgs/invite", url.Values{"org": {"alpha"}, "role": {"owner"}, "csrf_token": {renderedCSRFToken(t, c, srv.URL+"/ui/orgs?org=alpha")}})
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatal("an owner link was issued")
	}
}
