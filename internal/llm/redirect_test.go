package llm

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// Ein Anbieter, der umleitet, bekommt den Schlüssel nicht nachgeschickt: die
// Umleitung wird nicht verfolgt.
func TestProviderClientsDoNotFollowRedirects(t *testing.T) {
	var elsewhereHits int
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhereHits++
		if r.Header.Get("x-api-key") != "" {
			t.Error("the key reached the redirect target")
		}
	}))
	defer elsewhere.Close()
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	}))
	defer provider.Close()
	u, _ := url.Parse(provider.URL)
	client := newHTTPClient(&url.URL{Scheme: "https", Host: u.Host}, time.Second)
	client.Transport = provider.Client().Transport // vertraut dem Testzertifikat
	req, _ := http.NewRequest("POST", provider.URL, nil)
	req.Header.Set("x-api-key", "k-test")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusTemporaryRedirect || elsewhereHits != 0 {
		t.Fatalf("status %d, target hit %d times; the redirect must come back unfollowed", res.StatusCode, elsewhereHits)
	}
}
