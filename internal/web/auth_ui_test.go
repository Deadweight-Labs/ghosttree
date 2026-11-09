package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestRegisterAsksOnlyForTheInvitationLinkAndLeadsToTheInvitationPage(t *testing.T) {
	srv, st, _, org, _ := joinWeb(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/ui/register")
	if err != nil {
		t.Fatal(err)
	}
	page := body(t, resp)
	for _, want := range []string{`name="link"`, `action="/ui/register"`, "Invitation link", "Create an account", "Privacy"} {
		if !strings.Contains(page, want) {
			t.Errorf("register page lacks %q", want)
		}
	}
	if strings.Contains(page, `name="name"`) || strings.Contains(page, `name="code"`) {
		t.Error("the page asks for more than the link")
	}
	code := projectInvite(t, st, org, store.RoleMember)
	res := sameOriginPostForm(t, client, srv.URL+"/ui/register", url.Values{"link": {"https://ghosttree.example/join/" + strings.ToUpper(code) + "?x=1"}})
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/join/"+code {
		t.Errorf("a link leads to %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	res = sameOriginPostForm(t, client, srv.URL+"/ui/register", url.Values{"link": {"https://example.com/abc"}})
	text := body(t, res)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(text, "That is not an invitation link.") || !strings.Contains(text, "https://example.com/abc") {
		t.Errorf("a wrong link: %d %s", res.StatusCode, text)
	}
}

func TestLoginPageOffersTheTokenAndTheWayToRegisterAndCarriesTheFooter(t *testing.T) {
	srv, _, _ := testWeb(t)
	page := loginPageOf(t, srv)
	for _, want := range []string{`href="/ui/register"`, "Sign in with a token", `class="gt-foot"`, "Help"} {
		if !strings.Contains(page, want) {
			t.Errorf("login page lacks %q", want)
		}
	}
}
