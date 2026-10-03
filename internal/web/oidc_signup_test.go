package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// signupEnv is an instance with an owner, a project and an open project
// invitation; the IdP advertises prompt=create when prompts is not nil.
func signupEnv(t *testing.T, prompts []string, signup string) (*oidcEnv, string) {
	t.Helper()
	idp := newFakeIdP(t)
	idp.promptValues = prompts
	idp.userinfo = map[string]any{"sub": "sub-new", "name": "Nina Neu"}
	idp.subject, idp.username, idp.email = "sub-new", "", ""
	env := newOIDCEnvWith(t, idp, true, signup)
	org, err := env.store.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.EnsureProject("person:1", "github.com/dw/signup"); err != nil {
		t.Fatal(err)
	}
	code, _, err := env.store.CreateProjectInvitation("person:1", org.ID, "github.com/dw/signup", store.RoleMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	return env, code
}

func joinPageOf(t *testing.T, env *oidcEnv, code string) string {
	t.Helper()
	resp, err := newBrowser(t).Get(env.web.URL + "/join/" + code)
	if err != nil {
		t.Fatal(err)
	}
	return body(t, resp)
}

func TestJoinOffersAnAccountWhenTheIdPCanCreateOne(t *testing.T) {
	env, code := signupEnv(t, []string{"login", "create"}, "")
	page := joinPageOf(t, env, code)
	if !strings.Contains(page, "Create an account") || !strings.Contains(page, `name="signup" value="1"`) {
		t.Fatalf("no way to create an account: %s", page)
	}
	// The sign-in path stays next to it.
	if !strings.Contains(page, "Continue and join") {
		t.Fatalf("sign-in path gone: %s", page)
	}
}

func TestJoinOffersNoSignupWhenTheIdPCannotCreateOneOrItIsSwitchedOff(t *testing.T) {
	for name, tc := range map[string]struct {
		prompts []string
		signup  string
	}{
		"no prompt values":   {nil, ""},
		"other prompts only": {[]string{"login", "consent"}, ""},
		"switched off":       {[]string{"create"}, "0"},
	} {
		t.Run(name, func(t *testing.T) {
			env, code := signupEnv(t, tc.prompts, tc.signup)
			if page := joinPageOf(t, env, code); strings.Contains(page, "Create an account") || strings.Contains(page, `name="signup"`) {
				t.Fatalf("signup offered: %s", page)
			}
			// A hand-made signup=1 does not smuggle the prompt in either.
			b := newBrowser(t)
			env.startFlowForm(t, b, "/ui/login/oidc", url.Values{"code": {code}, "join": {"1"}, "signup": {"1"}})
			if env.idp.lastPrompt != "" {
				t.Fatalf("prompt %q sent", env.idp.lastPrompt)
			}
		})
	}
}

func TestSwitchingSignupOnForcesTheOfferWithoutDiscovery(t *testing.T) {
	env, code := signupEnv(t, nil, "1")
	if page := joinPageOf(t, env, code); !strings.Contains(page, "Create an account") {
		t.Fatalf("forced signup not offered: %s", page)
	}
}

func TestSignupStartsTheFlowWithPromptCreateAndJoinsOnReturn(t *testing.T) {
	env, code := signupEnv(t, []string{"create"}, "")
	b := newBrowser(t)
	cb := env.startFlowForm(t, b, "/ui/login/oidc", url.Values{"code": {code}, "join": {"1"}, "signup": {"1"}})
	if env.idp.lastPrompt != "create" {
		t.Fatalf("prompt %q, want create", env.idp.lastPrompt)
	}
	resp := env.callback(t, b, cb)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/join/pair") || !env.signedIn(t, b) {
		t.Fatalf("after registration: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	nina, err := env.store.AccountByName("Nina Neu")
	if err != nil || env.store.ProjectRole("github.com/dw/signup", nina.ID).Role != store.RoleMember {
		t.Fatalf("the invitation was not redeemed with the IdP's name: %+v %v", nina, err)
	}
	// Plain sign-in sends no prompt.
	env.idp.lastPrompt = "x"
	env.idp.subject = "sub-other"
	env.startFlowForm(t, newBrowser(t), "/ui/login/oidc", url.Values{"code": {code}, "join": {"1"}})
	if env.idp.lastPrompt != "" {
		t.Fatalf("sign-in sent prompt %q", env.idp.lastPrompt)
	}
}

func TestLoginPagesSayAnInvitationLinkCreatesAnAccount(t *testing.T) {
	env, _ := signupEnv(t, []string{"create"}, "")
	for _, path := range []string{"/ui/login", "/ui/login/code"} {
		resp, err := newBrowser(t).Get(env.web.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		if page := body(t, resp); !strings.Contains(page, "No account yet?") {
			t.Errorf("%s lacks the hint: %s", path, page)
		}
	}
}
