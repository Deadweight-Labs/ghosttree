package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
)

func TestTokenDoesNotTravelOverPlainHttpToAnotherMachine(t *testing.T) {
	t.Setenv(AllowInsecureEnv, "")
	for _, u := range []string{"http://ghosttree.example.com", "http://10.1.2.3:8080", "ftp://x", "ghosttree.example.com", ""} {
		c := New(config.Config{ServerURL: u, Token: "secret"})
		if err := c.checkServerURL(); err == nil {
			t.Errorf("%q must be refused", u)
		}
	}
	for _, u := range []string{"https://ghosttree.example.com", "http://127.0.0.1:1", "http://localhost:8080", "http://[::1]:80"} {
		if err := New(config.Config{ServerURL: u, Token: "secret"}).checkServerURL(); err != nil {
			t.Errorf("%q: %v", u, err)
		}
	}
	// Without a token (the first contact of a login) nothing secret is sent.
	if err := New(config.Config{ServerURL: "http://ghosttree.example.com"}).checkServerURL(); err != nil {
		t.Error(err)
	}
	t.Setenv(AllowInsecureEnv, "1")
	if err := New(config.Config{ServerURL: "http://10.1.2.3", Token: "secret"}).checkServerURL(); err != nil {
		t.Error(err)
	}
}

func TestAnswerOverTheLimitIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := []byte(strings.Repeat("x", 1<<20))
		for i := 0; i < maxResponseBytes>>20+2; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	var out string
	err := New(config.Config{ServerURL: srv.URL, Token: "t"}).do("GET", "/api/x", nil, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "over") {
		t.Fatalf("an endless answer was read: %v (%d bytes)", err, len(out))
	}
}
