package forge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// fakeUserAPI answers the code exchange and GET /user. user is the body of the
// /user answer; the exchange accepts only code "good" and the given client.
type fakeUserAPI struct {
	srv    *httptest.Server
	mu     sync.Mutex
	user   string
	status int
	auth   string
	paths  []string
}

func newFakeUserAPI(t *testing.T, clientID string) *fakeUserAPI {
	t.Helper()
	f := &fakeUserAPI{status: http.StatusOK, user: `{"login":"Octo-Cat","id":583231}`}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("code") != "good" || r.PostForm.Get("client_id") != clientID {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "usertok"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		f.auth = r.Header.Get("Authorization")
		user, status := f.user, f.status
		f.mu.Unlock()
		if r.Method != http.MethodGet || r.URL.Path != "/user" {
			http.NotFound(w, r)
			return
		}
		if status != http.StatusOK {
			http.Error(w, "no", status)
			return
		}
		_, _ = w.Write([]byte(user))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func TestIdentifyReturnsLoginAndNumberFromGitHubForBothProvers(t *testing.T) {
	for name, build := range map[string]func(f *fakeUserAPI, fa *fakeApp) Identifier{
		"oauth": func(f *fakeUserAPI, _ *fakeApp) Identifier {
			return NewGitHub(Config{ClientID: "Iv1.app", ClientSecret: "shh", WebBase: f.srv.URL, APIBase: f.srv.URL})
		},
		"app": func(f *fakeUserAPI, fa *fakeApp) Identifier {
			a := fa.app()
			a.cfg.WebBase, a.cfg.APIBase = f.srv.URL, f.srv.URL
			return a
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeUserAPI(t, "Iv1.app")
			id := build(f, newFakeApp(t))
			got, err := id.Identify(context.Background(), "good", "http://x/cb")
			if err != nil {
				t.Fatal(err)
			}
			if got.Login != "Octo-Cat" || got.ID != 583231 {
				t.Fatalf("identity: %+v", got)
			}
			if f.auth != "Bearer usertok" {
				t.Fatalf("/user is read with the user token, got %q", f.auth)
			}
			if len(f.paths) != 1 || f.paths[0] != "GET /user" {
				t.Fatalf("only /user is read: %v", f.paths)
			}

			if _, err := id.Identify(context.Background(), "forged", "http://x/cb"); err == nil || strings.Contains(err.Error(), "usertok") || strings.Contains(err.Error(), "shh") {
				t.Fatalf("a refused code is an error without secrets: %v", err)
			}
			f.status = http.StatusUnauthorized
			if _, err := id.Identify(context.Background(), "good", "http://x/cb"); err == nil || strings.Contains(err.Error(), "usertok") {
				t.Fatalf("a refused /user is an error without the token: %v", err)
			}
		})
	}
}

func TestIdentifyRefusesAnAnswerThatIsNotAGitHubAccount(t *testing.T) {
	for _, body := range []string{
		`{"login":"","id":5}`,
		`{"login":"octocat","id":0}`,
		`{"login":"octocat","id":-3}`,
		`{"login":"octocat"}`,
		`{"id":5}`,
		`{"login":"-lead","id":5}`,
		`{"login":"double--hyphen","id":5}`,
		`{"login":"under_score","id":5}`,
		`{"login":"../../etc","id":5}`,
		`{"login":"` + strings.Repeat("a", 40) + `","id":5}`,
		`not json`,
	} {
		f := newFakeUserAPI(t, "Iv1.app")
		f.user = body
		gh := NewGitHub(Config{ClientID: "Iv1.app", ClientSecret: "shh", WebBase: f.srv.URL, APIBase: f.srv.URL})
		if got, err := gh.Identify(context.Background(), "good", "http://x/cb"); err == nil {
			t.Errorf("%s was accepted as %+v", body, got)
		}
	}
	f := newFakeUserAPI(t, "Iv1.app")
	f.user = `{"login":"` + strings.Repeat("a", 39) + `","id":5}`
	gh := NewGitHub(Config{ClientID: "Iv1.app", ClientSecret: "shh", WebBase: f.srv.URL, APIBase: f.srv.URL})
	if _, err := gh.Identify(context.Background(), "good", "http://x/cb"); err != nil {
		t.Fatalf("39 characters is the longest login: %v", err)
	}
}

func TestIdentifyNeedsCredentials(t *testing.T) {
	if _, err := NewGitHub(Config{}).Identify(context.Background(), "x", "http://r"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("oauth without credentials: %v", err)
	}
	if _, err := NewGitHubApp(AppConfig{}).Identify(context.Background(), "x", "http://r"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("app without credentials: %v", err)
	}
}

func TestLoginSyntaxMatchesTheStore(t *testing.T) {
	for _, login := range []string{"a", "octocat", "Octo-Cat", "a-b-c", "-a", "a-", "a--b", "a_b", "", "a b", "é", strings.Repeat("x", 39), strings.Repeat("x", 40)} {
		_, err := parseIdentity(login, 1)
		if (err == nil) != store.ValidGitHubLogin(login) {
			t.Errorf("login %q: forge accepts=%v, store accepts=%v", login, err == nil, store.ValidGitHubLogin(login))
		}
	}
}
