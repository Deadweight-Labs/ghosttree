package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/forge"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// identProver is a GitHub login that can say who logged in. The identity is what
// "GitHub" answers; nothing the browser sends reaches it.
type identProver struct {
	fakeProver
	mu       sync.Mutex
	identity forge.Identity
	err      error
	codes    []string
	redirect []string
}

func (p *identProver) Identify(_ context.Context, code, redirect string) (forge.Identity, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.codes = append(p.codes, code)
	p.redirect = append(p.redirect, redirect)
	if code != "good" {
		return forge.Identity{}, forge.ErrNotConfigured
	}
	return p.identity, p.err
}

func (p *identProver) identifyCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.codes)
}

type linkEnv struct {
	base   string
	st     *store.Store
	alice  *http.Client
	anna   *http.Client
	token  string // alice's pasted token
	srv    *httptest.Server
	dir    string
	gh     *fakeGitHub
	prover *identProver
}

func newLinkEnv(t *testing.T, prover forge.Prover) *linkEnv {
	t.Helper()
	st := mustStore(t)
	token, err := st.AddPerson("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPerson("anna"); err != nil {
		t.Fatal(err)
	}
	e := &linkEnv{st: st, token: token, dir: t.TempDir(), gh: &fakeGitHub{reply: okPNG(t)}}
	e.prover, _ = prover.(*identProver)
	h := New(st, WithSourceProver(prover), withAvatarClient(avatarClient(e.gh)), WithAvatarCache(e.dir))
	e.srv = httptest.NewServer(h)
	t.Cleanup(e.srv.Close)
	h.(*appHandler).app.avatars.syncFirst = true
	e.base = e.srv.URL
	e.alice = loginInteractive(t, e.srv, st, "alice")
	e.anna = loginInteractive(t, e.srv, st, "anna")
	return e
}

func configuredProver() *identProver {
	return &identProver{fakeProver: fakeProver{configured: true}, identity: forge.Identity{Login: "octocat", ID: 583231}}
}

func (e *linkEnv) post(t *testing.T, c *http.Client, path string, form url.Values) *http.Response {
	t.Helper()
	form.Set("csrf_token", renderedCSRFToken(t, c, e.base+"/ui/profile"))
	return sameOriginPostForm(t, c, e.base+path, form)
}

// start is the click on "Mit GitHub verbinden": it returns the state the browser
// carries to GitHub and back.
func (e *linkEnv) start(t *testing.T, c *http.Client) string {
	t.Helper()
	resp := e.post(t, c, "/ui/profile/github/link", url.Values{})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("start: %d", resp.StatusCode)
	}
	u, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || u.Host != "forge.example" {
		t.Fatalf("the browser goes to the forge: %q", resp.Header.Get("Location"))
	}
	if got := u.Query().Get("redirect_uri"); got != e.base+"/ui/orgs/source/callback" {
		t.Fatalf("the callback address is the registered one: %q", got)
	}
	return u.Query().Get("state")
}

func (e *linkEnv) callback(t *testing.T, c *http.Client, query string) string {
	t.Helper()
	resp, err := c.Get(e.base + "/ui/orgs/source/callback?" + query)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.Header.Get("Location")
}

func (e *linkEnv) profile(t *testing.T, c *http.Client) string {
	t.Helper()
	_, page := fetchPage(t, c, e.base+"/ui/profile")
	return page
}

func TestProfileLinksTheGitHubAccountGitHubNamed(t *testing.T) {
	e := newLinkEnv(t, configuredProver())
	page := e.profile(t, e.alice)
	if !strings.Contains(page, `action="/ui/profile/github/link"`) || !strings.Contains(page, "Connect with GitHub") || strings.Contains(page, "/ui/profile/github/unlink") {
		t.Fatalf("an unlinked profile offers to connect: %s", page)
	}

	// The form carries no login, and an extra one is ignored all the same.
	state := e.start(t, e.alice)
	if _, linked := e.st.AccountGitHub("person:1"); linked {
		t.Fatal("nothing is linked before GitHub answered")
	}
	// The return carries a login of the attacker's choice next to the real state.
	loc := e.callback(t, e.alice, "code=good&state="+state+"&login=mallory&github_id=1&account=person:2")
	if loc != "/ui/profile?notice=github_linked" {
		t.Fatalf("callback: %q", loc)
	}
	l, ok := e.st.AccountGitHub("person:1")
	if !ok || l.Login != "octocat" || l.ID != 583231 {
		t.Fatalf("the link says what GitHub answered, not what the request said: %+v %v", l, ok)
	}
	if _, other := e.st.AccountGitHub("person:2"); other {
		t.Fatal("only the account that started the flow is linked")
	}
	if got := e.prover.redirect; len(got) != 1 || got[0] != e.base+"/ui/orgs/source/callback" {
		t.Fatalf("the code is exchanged for the registered callback: %v", got)
	}

	page = e.profile(t, e.alice)
	for _, want := range []string{`href="https://github.com/octocat"`, `rel="noopener noreferrer"`, "@octocat", `action="/ui/profile/github/unlink"`, `src="/ui/avatar/1"`} {
		if !strings.Contains(page, want) {
			t.Errorf("linked profile lacks %q: %s", want, page)
		}
	}
	if strings.Contains(page, "/ui/profile/github/link") {
		t.Error("a linked profile does not offer to connect again")
	}
	_, ownNotice := fetchPage(t, e.alice, e.base+"/ui/profile?notice=github_linked")
	if !strings.Contains(ownNotice, "GitHub account connected") {
		t.Errorf("the notice is shown: %s", ownNotice)
	}
}

func TestGitHubLinkStateBelongsToTheAccountThatStartedIt(t *testing.T) {
	e := newLinkEnv(t, configuredProver())
	annasState := e.start(t, e.anna)
	loc := e.callback(t, e.alice, "code=good&state="+annasState)
	if strings.Contains(loc, "github_linked") || e.prover.identifyCalls() != 0 {
		t.Fatalf("alice used anna's state: %q (identify calls %d)", loc, e.prover.identifyCalls())
	}
	if _, linked := e.st.AccountGitHub("person:1"); linked {
		t.Fatal("alice got linked through anna's state")
	}
	// The refused try did not burn anna's state.
	if loc := e.callback(t, e.anna, "code=good&state="+annasState); loc != "/ui/profile?notice=github_linked" {
		t.Fatalf("anna's own return: %q", loc)
	}
	if l, ok := e.st.AccountGitHub("person:2"); !ok || l.Login != "octocat" {
		t.Fatalf("anna: %+v %v", l, ok)
	}
}

func TestGitHubLinkStateWorksOnce(t *testing.T) {
	e := newLinkEnv(t, configuredProver())
	state := e.start(t, e.alice)
	if loc := e.callback(t, e.alice, "code=good&state="+state); !strings.Contains(loc, "github_linked") {
		t.Fatalf("first return: %q", loc)
	}
	if err := e.st.ClearAccountGitHub("person:1"); err != nil {
		t.Fatal(err)
	}
	if loc := e.callback(t, e.alice, "code=good&state="+state); strings.Contains(loc, "github_linked") {
		t.Fatalf("a used state linked again: %q", loc)
	}
	if _, linked := e.st.AccountGitHub("person:1"); linked || e.prover.identifyCalls() != 1 {
		t.Fatalf("replay: linked=%v identify calls=%d", linked, e.prover.identifyCalls())
	}
	for _, state := range []string{"", "nonsense"} {
		if loc := e.callback(t, e.alice, "code=good&state="+state); strings.Contains(loc, "github_linked") {
			t.Fatalf("state %q: %q", state, loc)
		}
	}
}

func TestGitHubLinkFailureLeavesNothingBehind(t *testing.T) {
	cases := map[string]struct {
		identity forge.Identity
		err      error
		query    string
	}{
		"github refuses":      {forge.Identity{Login: "octocat", ID: 1}, errors.New("refused"), "code=good"},
		"no code":             {forge.Identity{Login: "octocat", ID: 1}, nil, "error=access_denied"},
		"bad login":           {forge.Identity{Login: "../etc", ID: 1}, nil, "code=good"},
		"no number":           {forge.Identity{Login: "octocat", ID: 0}, nil, "code=good"},
		"forged code":         {forge.Identity{Login: "octocat", ID: 1}, nil, "code=forged"},
		"empty login of ghub": {forge.Identity{Login: "", ID: 1}, nil, "code=good"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := configuredProver()
			p.identity, p.err = c.identity, c.err
			e := newLinkEnv(t, p)
			state := e.start(t, e.alice)
			loc := e.callback(t, e.alice, c.query+"&state="+state)
			if loc != "/ui/profile?notice=github_failed" {
				t.Fatalf("callback: %q", loc)
			}
			if _, linked := e.st.AccountGitHub("person:1"); linked {
				t.Fatal("a failed proof linked the account")
			}
			if _, page := fetchPage(t, e.alice, e.base+loc); !strings.Contains(page, "GitHub could not confirm your account") {
				t.Fatalf("the failure is shown: %s", page)
			}
		})
	}
}

func TestUnlinkClearsTheLinkAndTheKeptPicture(t *testing.T) {
	e := newLinkEnv(t, configuredProver())
	if err := e.st.SetAccountGitHub("person:1", "octocat", 583231); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetAccountGitHub("person:2", "hubot", 9); err != nil {
		t.Fatal(err)
	}
	if page := e.profile(t, e.alice); !strings.Contains(page, `src="/ui/avatar/1"`) {
		t.Fatalf("the picture shows before: %s", page)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "1.img")); err != nil {
		t.Fatalf("the picture is kept on disk: %v", err)
	}

	// An account in the form is not looked at: only the signed-in one is unlinked.
	resp := e.post(t, e.alice, "/ui/profile/github/unlink", url.Values{"account": {"person:2"}, "login": {"hubot"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/profile?notice=github_unlinked" {
		t.Fatalf("unlink: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if _, linked := e.st.AccountGitHub("person:1"); linked {
		t.Fatal("the link is still there")
	}
	if l, ok := e.st.AccountGitHub("person:2"); !ok || l.Login != "hubot" {
		t.Fatalf("somebody else's link was touched: %+v %v", l, ok)
	}
	for _, f := range []string{"1.img", "1.json"} {
		if _, err := os.Stat(filepath.Join(e.dir, f)); !os.IsNotExist(err) {
			t.Errorf("%s stays on disk after unlinking: %v", f, err)
		}
	}
	if r, err := e.alice.Get(e.base + "/ui/avatar/1"); err != nil || r.StatusCode != http.StatusNotFound {
		t.Fatalf("the picture route answers %v %v after unlinking", r, err)
	}
	page := e.profile(t, e.alice)
	if strings.Contains(page, "/ui/avatar/1") || strings.Contains(page, "@octocat") || !strings.Contains(page, "Connect with GitHub") {
		t.Fatalf("after unlinking the profile is back to the initials and the button: %s", page)
	}
	// Unlinking twice is harmless.
	if resp := e.post(t, e.alice, "/ui/profile/github/unlink", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("second unlink: %d", resp.StatusCode)
	}
}

func TestRelinkingShowsThePictureOfTheNewAccount(t *testing.T) {
	e := newLinkEnv(t, configuredProver())
	if err := e.st.SetAccountGitHub("person:1", "old-login", 11); err != nil {
		t.Fatal(err)
	}
	e.profile(t, e.alice)
	state := e.start(t, e.alice)
	if loc := e.callback(t, e.alice, "code=good&state="+state); !strings.Contains(loc, "github_linked") {
		t.Fatal(loc)
	}
	e.profile(t, e.alice)
	last := e.gh.calls[len(e.gh.calls)-1]
	if last != "https://avatars.githubusercontent.com/u/583231?s=96&v=4" {
		t.Fatalf("the picture of the new account was fetched, not the old one kept: %v", e.gh.calls)
	}
}

func TestProfileOffersNoButtonWhenGitHubCannotBeAsked(t *testing.T) {
	for name, prover := range map[string]forge.Prover{
		"credentials missing": &identProver{fakeProver: fakeProver{configured: false}},
		"cannot identify":     &fakeProver{configured: true},
		"no prover":           nil,
	} {
		t.Run(name, func(t *testing.T) {
			e := newLinkEnv(t, prover)
			page := e.profile(t, e.alice)
			if strings.Contains(page, "/ui/profile/github/") || strings.Contains(page, "Connect with GitHub") || strings.Contains(page, ">GitHub<") {
				t.Fatalf("a button that cannot work: %s", page)
			}
			resp := e.post(t, e.alice, "/ui/profile/github/link", url.Values{})
			if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/profile?notice=github_failed" {
				t.Fatalf("starting anyway: %d %q", resp.StatusCode, resp.Header.Get("Location"))
			}
		})
	}
}

func TestProfileKeepsShowingALinkWhenGitHubIsNotConfigured(t *testing.T) {
	e := newLinkEnv(t, &identProver{fakeProver: fakeProver{configured: false}})
	if err := e.st.SetAccountGitHub("person:1", "octocat", 5); err != nil {
		t.Fatal(err)
	}
	page := e.profile(t, e.alice)
	if !strings.Contains(page, "@octocat") || !strings.Contains(page, "/ui/profile/github/unlink") || strings.Contains(page, "/ui/profile/github/link") {
		t.Fatalf("a set link can be seen and removed: %s", page)
	}
}

func TestGitHubLinkRoutesNeedCSRFOriginAndAnInteractiveSession(t *testing.T) {
	e := newLinkEnv(t, configuredProver())
	if err := e.st.SetAccountGitHub("person:1", "octocat", 5); err != nil {
		t.Fatal(err)
	}
	good := renderedCSRFToken(t, e.alice, e.base+"/ui/profile")
	for _, path := range []string{"/ui/profile/github/link", "/ui/profile/github/unlink"} {
		for name, tok := range map[string]string{"missing": "", "wrong": "nope"} {
			if resp := sameOriginPostForm(t, e.alice, e.base+path, url.Values{"csrf_token": {tok}}); resp.StatusCode == http.StatusSeeOther {
				t.Errorf("%s: %s csrf accepted", path, name)
			}
		}
		req, _ := http.NewRequest(http.MethodPost, e.base+path, strings.NewReader(url.Values{"csrf_token": {good}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "https://evil.example")
		if resp, err := e.alice.Do(req); err != nil || resp.StatusCode == http.StatusSeeOther {
			t.Errorf("%s: foreign origin accepted: %v %v", path, resp, err)
		}
		// A GET does nothing: the routes are POST only.
		if resp, err := e.alice.Get(e.base + path); err != nil || resp.StatusCode == http.StatusSeeOther {
			t.Errorf("%s: GET answers %v %v", path, resp, err)
		}
		// Not signed in: sent to the login, nothing happens.
		anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp := sameOriginPostForm(t, anon, e.base+path, url.Values{"csrf_token": {good}})
		if loc := resp.Header.Get("Location"); !strings.Contains(loc, "/ui/login") {
			t.Errorf("%s: anonymous answers %d %q", path, resp.StatusCode, loc)
		}
	}
	if _, linked := e.st.AccountGitHub("person:1"); !linked {
		t.Fatal("a refused request unlinked the account")
	}

	// A pasted token is a read-only session: it neither links nor unlinks.
	paste := login(t, e.srv, e.token)
	tok := renderedCSRFToken(t, paste, e.base+"/ui/overview")
	for _, path := range []string{"/ui/profile/github/link", "/ui/profile/github/unlink"} {
		if resp := sameOriginPostForm(t, paste, e.base+path, url.Values{"csrf_token": {tok}}); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s with a pasted token answers %d", path, resp.StatusCode)
		}
	}
	if page := e.profile(t, paste); strings.Contains(page, "/ui/profile/github/") {
		t.Errorf("a read-only session sees the buttons: %s", page)
	}
	if _, linked := e.st.AccountGitHub("person:1"); !linked {
		t.Fatal("a token session unlinked the account")
	}
}

func TestGuestsCannotLinkOrUnlinkGitHub(t *testing.T) {
	e := newLinkEnv(t, configuredProver())
	guest := guestSession(t, e)
	for _, path := range []string{"/ui/profile/github/link", "/ui/profile/github/unlink"} {
		resp := sameOriginPostForm(t, guest, e.base+path, url.Values{"csrf_token": {renderedCSRFToken(t, guest, e.base+"/ui/profile")}})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s as guest answers %d", path, resp.StatusCode)
		}
	}
	if page := e.profile(t, guest); strings.Contains(page, "/ui/profile/github/") {
		t.Errorf("a guest sees the buttons: %s", page)
	}
}

func TestNoGitHubLinkRouteReadsALoginFromTheRequest(t *testing.T) {
	// The handlers of the profile link never name a request value for the login.
	src, err := os.ReadFile("profile_github.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"FormValue(", "PostFormValue(", "Form.Get(", "PostForm.Get("} {
		if strings.Contains(string(src), banned) {
			t.Errorf("profile_github.go reads a form value (%s)", banned)
		}
	}
	if n := strings.Count(string(src), "URL.Query()"); n != 1 {
		t.Errorf("only the code of the callback is read from the query, found %d reads", n)
	}
}

// guestSession is a signed-in guest: gina has only a guest role in a project.
func guestSession(t *testing.T, e *linkEnv) *http.Client {
	t.Helper()
	if _, err := e.st.AddPerson("gina"); err != nil {
		t.Fatal(err)
	}
	org, err := e.st.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	const remote = "github.com/x/one"
	if _, err := e.st.ClaimProject("person:1", remote, "alpha"); err != nil {
		t.Fatal(err)
	}
	code, _, _ := e.st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	if _, err := e.st.AcceptInvitation("person:3", code); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetProjectRole("person:1", remote, "person:3", store.RoleGuest, false, store.RoleViaCLI); err != nil {
		t.Fatal(err)
	}
	if !e.st.NameLocked("person:3") {
		t.Fatal("gina must be a guest")
	}
	return loginInteractive(t, e.srv, e.st, "gina")
}
