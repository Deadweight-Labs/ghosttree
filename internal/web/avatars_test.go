package web

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A fake GitHub: the test decides what each address answers, and counts.
type fakeGitHub struct {
	mu    sync.Mutex
	calls []string
	reply func(r *http.Request) (*http.Response, error)
}

func (f *fakeGitHub) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.calls = append(f.calls, r.URL.String())
	f.mu.Unlock()
	return f.reply(r)
}

func (f *fakeGitHub) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func answer(status int, ctype string, body []byte, req *http.Request) *http.Response {
	h := http.Header{}
	if ctype != "" {
		h.Set("Content-Type", ctype)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(bytes.NewReader(body)), Request: req}
}

type avatarEnv struct {
	srv    *httptest.Server
	st     *store.Store
	app    *app
	gh     *fakeGitHub
	dir    string
	alice  *http.Client
	cache  *avatarCache
	pngRaw []byte
}

// newAvatarEnv is a server whose profile pictures come from a fake GitHub. alice
// is linked to a GitHub account; the first look at a picture waits for the fetch.
func newAvatarEnv(t *testing.T, reply func(r *http.Request) (*http.Response, error)) *avatarEnv {
	t.Helper()
	st := mustStore(t)
	if _, err := st.AddPerson("alice"); err != nil {
		t.Fatal(err)
	}
	e := &avatarEnv{st: st, gh: &fakeGitHub{reply: reply}, dir: t.TempDir(), pngRaw: testPNG(t)}
	h := New(st, withAvatarClient(avatarClient(e.gh)), WithAvatarCache(e.dir))
	e.srv = httptest.NewServer(h)
	t.Cleanup(e.srv.Close)
	e.app = h.(*appHandler).app
	e.cache = e.app.avatars
	e.cache.syncFirst = true
	e.alice = loginInteractive(t, e.srv, st, "alice")
	return e
}

func okPNG(t *testing.T) func(r *http.Request) (*http.Response, error) {
	raw := testPNG(t)
	return func(r *http.Request) (*http.Response, error) { return answer(200, "image/png", raw, r), nil }
}

var foreignImage = regexp.MustCompile(`(?i)<img[^>]+src="(?:https?:)?//`)

func TestProfilePictureComesFromOwnRouteAndNeverFromGitHub(t *testing.T) {
	e := newAvatarEnv(t, okPNG(t))
	if err := e.st.SetAccountGitHub("person:1", "octocat", 583231); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/ui/inbox", "/ui/knowledge", "/ui/profile"} {
		_, page := fetchPage(t, e.alice, e.srv.URL+path)
		if !strings.Contains(page, `src="/ui/avatar/1"`) {
			t.Errorf("%s shows no profile picture of the signed-in person", path)
		}
		if foreignImage.MatchString(page) || strings.Contains(page, "githubusercontent.com") {
			t.Errorf("%s names a foreign picture address", path)
		}
		if strings.Contains(page, `data-av="`) && strings.Contains(page, `class="gav hc-`) && strings.Contains(page, ">AL<") {
			t.Errorf("%s still shows the initials of a person who has a picture", path)
		}
	}
	resp, err := e.alice.Get(e.srv.URL + "/ui/avatar/1")
	if err != nil {
		t.Fatal(err)
	}
	data := body(t, resp)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || resp.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(data, "\x89PNG") {
		t.Errorf("picture route: %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("X-Content-Type-Options"))
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "img-src 'self'") || strings.Contains(csp, "githubusercontent") {
		t.Errorf("CSP lets the browser load pictures from elsewhere: %q", csp)
	}
	// Only the fixed address was asked, and once for all those pages.
	if n := e.gh.count(); n != 1 || e.gh.calls[0] != "https://avatars.githubusercontent.com/u/583231?s=320&v=4" {
		t.Errorf("GitHub was asked %d times: %v", n, e.gh.calls)
	}
}

func TestProfilePictureStaysInitialsWithoutLinkOrOnFailure(t *testing.T) {
	// No link: nothing is fetched.
	e := newAvatarEnv(t, okPNG(t))
	_, page := fetchPage(t, e.alice, e.srv.URL+"/ui/inbox")
	if strings.Contains(page, `/ui/avatar/`) || e.gh.count() != 0 {
		t.Errorf("a person without a GitHub account got a picture or a fetch: %d", e.gh.count())
	}
	if !strings.Contains(page, ">AL<") {
		t.Error("the initials are missing")
	}
	if resp, _ := e.alice.Get(e.srv.URL + "/ui/avatar/1"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("route for an unlinked account = %d, want 404", resp.StatusCode)
	}
	// Linked, but GitHub is down: initials, and the next page does not ask again at once.
	down := newAvatarEnv(t, func(r *http.Request) (*http.Response, error) { return nil, errors.New("connection refused") })
	if err := down.st.SetAccountGitHub("person:1", "octocat", 0); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		_, page := fetchPage(t, down.alice, down.srv.URL+"/ui/inbox")
		if strings.Contains(page, `/ui/avatar/`) || !strings.Contains(page, ">AL<") {
			t.Fatalf("round %d: the failed fetch left a broken picture or no initials", i)
		}
	}
	if n := down.gh.count(); n != 1 {
		t.Errorf("a failed fetch was repeated %d times within the pause", n)
	}
}

func TestProfilePictureFetchRefusesWhatIsNotARasterImageFromGitHub(t *testing.T) {
	raw := testPNG(t)
	big := append(append([]byte{}, raw...), make([]byte, avatarMaxBytes)...)
	for name, reply := range map[string]func(r *http.Request) (*http.Response, error){
		"svg": func(r *http.Request) (*http.Response, error) {
			return answer(200, "image/svg+xml", []byte("<svg/>"), r), nil
		},
		"html as png": func(r *http.Request) (*http.Response, error) {
			return answer(200, "image/png", []byte("<html>x</html>"), r), nil
		},
		"text":         func(r *http.Request) (*http.Response, error) { return answer(200, "text/html", raw, r), nil },
		"too large":    func(r *http.Request) (*http.Response, error) { return answer(200, "image/png", big, r), nil },
		"not found":    func(r *http.Request) (*http.Response, error) { return answer(404, "image/png", raw, r), nil },
		"empty":        func(r *http.Request) (*http.Response, error) { return answer(200, "image/png", nil, r), nil },
		"slow failure": func(r *http.Request) (*http.Response, error) { return nil, errors.New("timeout") },
	} {
		t.Run(name, func(t *testing.T) {
			e := newAvatarEnv(t, reply)
			if err := e.st.SetAccountGitHub("person:1", "octocat", 7); err != nil {
				t.Fatal(err)
			}
			if _, ok := e.cache.refresh(1); ok {
				t.Error("the picture was taken")
			}
			if entries, _ := os.ReadDir(e.dir); len(entries) != 0 {
				t.Errorf("something was written to disk: %v", entries)
			}
		})
	}
}

func TestProfilePictureRedirectOnlyToThePictureHost(t *testing.T) {
	raw := testPNG(t)
	redirect := func(to string) func(r *http.Request) (*http.Response, error) {
		return func(r *http.Request) (*http.Response, error) {
			if r.URL.Host == "github.com" {
				resp := answer(302, "", nil, r)
				resp.Header.Set("Location", to)
				return resp, nil
			}
			return answer(200, "image/png", raw, r), nil
		}
	}
	// By login: github.com/<login>.png may lead to the picture host.
	e := newAvatarEnv(t, redirect("https://avatars.githubusercontent.com/u/9?v=4"))
	if err := e.st.SetAccountGitHub("person:1", "octo-cat", 0); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.cache.refresh(1); !ok {
		t.Fatal("the redirect to the picture host was refused")
	}
	if e.gh.calls[0] != "https://github.com/octo-cat.png?size=320" {
		t.Errorf("first address: %s", e.gh.calls[0])
	}
	// Anywhere else is refused: another host, plain http, an internal address.
	for _, to := range []string{"https://evil.example/a.png", "http://avatars.githubusercontent.com/u/9", "https://127.0.0.1/a.png", "https://avatars.githubusercontent.com.evil.example/a.png", "https://avatars.githubusercontent.com:8443/a.png"} {
		e := newAvatarEnv(t, redirect(to))
		if err := e.st.SetAccountGitHub("person:1", "octocat", 0); err != nil {
			t.Fatal(err)
		}
		if _, ok := e.cache.refresh(1); ok {
			t.Errorf("redirect to %s was followed", to)
		}
		for _, c := range e.gh.calls {
			if !strings.HasPrefix(c, "https://github.com/") {
				t.Errorf("redirect to %s was requested: %v", to, e.gh.calls)
			}
		}
	}
}

func TestProfilePictureIsKeptADayOnDiskAndFetchedAgainAfterwards(t *testing.T) {
	e := newAvatarEnv(t, okPNG(t))
	if err := e.st.SetAccountGitHub("person:1", "octocat", 7); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e.cache.now = func() time.Time { return now }
	fetchPage(t, e.alice, e.srv.URL+"/ui/inbox")
	if e.gh.count() != 1 {
		t.Fatalf("first page fetched %d times", e.gh.count())
	}
	if _, err := os.Stat(filepath.Join(e.dir, "1-320.img")); err != nil {
		t.Errorf("the picture is not on disk: %v", err)
	}
	// A new server on the same directory (a restart) does not fetch.
	e2 := &avatarEnv{st: e.st, gh: &fakeGitHub{reply: okPNG(t)}, dir: e.dir}
	c2 := newAvatarCache(e.st, avatarClient(e2.gh), e.dir)
	c2.now = func() time.Time { return now.Add(time.Hour) }
	if !c2.ready(1) || e2.gh.count() != 0 {
		t.Errorf("after a restart within the day: ready=%v fetches=%d", c2.ready(1), e2.gh.count())
	}
	// A day later it is fetched again, and meanwhile the old one still shows.
	e.cache.now = func() time.Time { return now.Add(avatarTTL + time.Minute) }
	fetchPage(t, e.alice, e.srv.URL+"/ui/inbox")
	if e.gh.count() != 2 {
		t.Errorf("after the day the picture was fetched %d times in all, want 2", e.gh.count())
	}
}

func TestLinkingAGitHubAccountNeedsAGitHubLogin(t *testing.T) {
	st := mustStore(t)
	if _, err := st.AddPerson("alice"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "-lead", "trail-", "dou--ble", "has space", "slash/x", "a.b", strings.Repeat("a", 40), "evil.example/x?y", "ünï"} {
		if err := st.SetAccountGitHub("person:1", bad, 0); err == nil {
			t.Errorf("login %q was accepted", bad)
		}
	}
	for _, good := range []string{"a", "octocat", "octo-cat", "A1-b2", strings.Repeat("a", 39)} {
		if err := st.SetAccountGitHub("person:1", good, 0); err != nil {
			t.Errorf("login %q refused: %v", good, err)
		}
	}
	if err := st.SetAccountGitHub("person:99", "octocat", 0); err == nil {
		t.Error("an account that does not exist was linked")
	}
	if l, ok := st.AccountGitHub("person:1"); !ok || l.Login != strings.Repeat("a", 39) {
		t.Errorf("link = %+v %v", l, ok)
	}
}

// The picture route obeys the same pause and the same single fetch as the pages:
// asking it again and again does not make the server ask GitHub again and again.
func TestProfilePictureRouteDoesNotAmplifyRequestsToGitHub(t *testing.T) {
	down := newAvatarEnv(t, func(r *http.Request) (*http.Response, error) { return nil, errors.New("connection refused") })
	if err := down.st.SetAccountGitHub("person:1", "octocat", 7); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		resp, err := down.alice.Get(down.srv.URL + "/ui/avatar/1")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("round %d: %d, want 404", i, resp.StatusCode)
		}
	}
	if n := down.gh.count(); n != 1 {
		t.Errorf("five requests to the route made %d requests to GitHub, want 1", n)
	}
	// While a fetch of the account runs, another one does not start.
	if !down.cache.claim(1) {
		t.Fatal("claim failed")
	}
	if _, ok := down.cache.refreshOnce(1); ok {
		t.Error("a second fetch ran next to the first")
	}
	down.cache.release(1)
}

// The picture route answers like an unknown account for somebody the viewer
// shares neither an organisation nor a project with (alice is the admin and
// sees everyone, so carol is the viewer).
func TestProfilePictureOfAStrangerIsNotFound(t *testing.T) {
	e := newAvatarEnv(t, okPNG(t))
	for _, n := range []string{"bob", "carol"} {
		if _, err := e.st.AddPerson(n); err != nil {
			t.Fatal(err)
		}
	}
	for id, login := range map[string]string{"person:2": "bob-gh", "person:3": "carol-gh"} {
		if err := e.st.SetAccountGitHub(id, login, 100); err != nil {
			t.Fatal(err)
		}
	}
	e.st.SetAccessMode(store.AccessMode{Enforce: true})
	carol := loginInteractive(t, e.srv, e.st, "carol")
	status := func(path string) int {
		resp, err := carol.Get(e.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body(t, resp)
		return resp.StatusCode
	}
	if got := status("/ui/avatar/3"); got != http.StatusOK {
		t.Errorf("own picture = %d, want 200", got)
	}
	if got, unknown := status("/ui/avatar/2"), status("/ui/avatar/999"); got != http.StatusNotFound || unknown != http.StatusNotFound {
		t.Errorf("a stranger's picture = %d, an unknown one = %d, want 404 for both", got, unknown)
	}
	if n := e.gh.count(); n != 1 {
		t.Errorf("GitHub was asked %d times, want once for carol's own picture: %v", n, e.gh.calls)
	}
	// Once they share an organisation, the picture is there.
	org, err := e.st.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"person:2", "person:3"} {
		code, _, err := e.st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.AcceptInvitation(who, code); err != nil {
			t.Fatal(err)
		}
	}
	if got := status("/ui/avatar/2"); got != http.StatusOK {
		t.Errorf("a colleague's picture = %d, want 200", got)
	}
}
