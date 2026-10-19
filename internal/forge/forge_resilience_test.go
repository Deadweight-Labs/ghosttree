package forge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOneOrgWithUnreadableReposDoesNotDropTheOthers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"tok"}`))
	})
	mux.HandleFunc("GET /user/memberships/orgs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
		 {"state":"active","role":"admin","organization":{"login":"Broken"}},
		 {"state":"active","role":"admin","organization":{"login":"Fine"}}]`))
	})
	mux.HandleFunc("GET /orgs/{org}/repos", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("org") == "Broken" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`[{"full_name":"Fine/one"}]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	gh := NewGitHub(Config{ClientID: "id", ClientSecret: "shh", WebBase: srv.URL, APIBase: srv.URL})
	accts, err := gh.Prove(context.Background(), "good", "http://x/cb")
	if err != nil {
		t.Fatalf("one unreadable org must not fail the proof: %v", err)
	}
	if len(accts) != 2 || accts[0].Login != "broken" || len(accts[0].Repos) != 0 || len(accts[1].Repos) != 1 {
		t.Fatalf("accounts: %+v", accts)
	}
}

func TestTransportErrorsCarryNeitherSecretNorCode(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close() // der Port ist danach geschlossen
	gh := NewGitHub(Config{ClientID: "id", ClientSecret: "topsecret", WebBase: base, APIBase: base})
	_, err := gh.Prove(context.Background(), "the-code-123", "http://x/cb")
	if err == nil {
		t.Fatal("an unreachable GitHub must fail")
	}
	for _, leak := range []string{"topsecret", "the-code-123", "client_secret", base} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error leaks %q: %v", leak, err)
		}
	}
}
