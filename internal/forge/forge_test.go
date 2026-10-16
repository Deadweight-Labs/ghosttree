package forge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNotConfiguredUntilBothCredentialsExist(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	files := map[string]string{}
	read := func(p string) ([]byte, error) {
		if v, ok := files[p]; ok {
			return []byte(v), nil
		}
		return nil, errors.New("no file")
	}
	if NewGitHub(ConfigFromEnv(get, read)).Configured() {
		t.Fatal("empty environment must read as not configured")
	}
	env[EnvClientID] = "Iv1.abc"
	if NewGitHub(ConfigFromEnv(get, read)).Configured() {
		t.Fatal("an id without a secret is not configured")
	}
	env[EnvClientSecretFile] = "/run/secrets/gh"
	if NewGitHub(ConfigFromEnv(get, read)).Configured() {
		t.Fatal("an unreadable secret file is not configured")
	}
	files["/run/secrets/gh"] = "s3cret\n"
	gh := NewGitHub(ConfigFromEnv(get, read))
	if !gh.Configured() {
		t.Fatal("secret from file")
	}
	if _, err := NewGitHub(Config{}).Prove(context.Background(), "x", "http://r"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("prove without credentials: %v", err)
	}
}

func fakeGitHub(t *testing.T, secretSeen *string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		*secretSeen = r.PostForm.Get("client_secret")
		if r.PostForm.Get("code") != "good" {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
	})
	mux.HandleFunc("GET /user/memberships/orgs", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`[
		 {"state":"active","role":"admin","organization":{"login":"Deadweight-Labs"}},
		 {"state":"active","role":"member","organization":{"login":"other"}},
		 {"state":"pending","role":"admin","organization":{"login":"pending"}}]`))
	})
	mux.HandleFunc("GET /orgs/{org}/repos", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"full_name":"Deadweight-Labs/ghosttree"},{"full_name":"Deadweight-Labs/infra"}]`))
	})
	return httptest.NewServer(mux)
}

func TestProveReturnsOnlyOrgsTheHumanAdministers(t *testing.T) {
	var secret string
	srv := fakeGitHub(t, &secret)
	defer srv.Close()
	gh := NewGitHub(Config{ClientID: "id", ClientSecret: "shh", WebBase: srv.URL, APIBase: srv.URL})
	accts, err := gh.Prove(context.Background(), "good", "http://x/cb")
	if err != nil {
		t.Fatal(err)
	}
	if len(accts) != 1 || accts[0].Login != "deadweight-labs" || len(accts[0].Repos) != 2 || accts[0].Repos[0] != "github.com/deadweight-labs/ghosttree" {
		t.Fatalf("accounts: %+v", accts)
	}
	if secret != "shh" {
		t.Fatal("the secret goes to the token endpoint")
	}
	_, err = gh.Prove(context.Background(), "forged", "http://x/cb")
	if err == nil || strings.Contains(err.Error(), "shh") {
		t.Fatalf("a refused code is an error without secrets: %v", err)
	}
}

func TestAuthorizeURLCarriesStateAndNoSecret(t *testing.T) {
	gh := NewGitHub(Config{ClientID: "id", ClientSecret: "shh"})
	u := gh.AuthorizeURL("st8", "https://gt.example/ui/orgs/source/callback")
	if !strings.Contains(u, "state=st8") || !strings.Contains(u, "client_id=id") || strings.Contains(u, "shh") {
		t.Fatalf("url: %s", u)
	}
}
