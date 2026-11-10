package client

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
)

// A read-only client (GHOSTTREE_HOOKS=off in ctx mcp) reads and never sends a
// write (security review 12).
func TestReadOnlyClientSendsNoWrite(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := New(config.Config{ServerURL: srv.URL, Token: "t"})
	c.SetReadOnly()
	var out map[string]any
	if err := c.do("GET", "/api/whoami", nil, nil, &out); err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		if err := c.do(m, "/api/knowledge", nil, map[string]any{"x": 1}, &out); !errors.Is(err, ErrReadOnly) {
			t.Errorf("%s: %v", m, err)
		}
	}
	if len(methods) != 1 || methods[0] != "GET" {
		t.Fatalf("reached the server: %v", methods)
	}
}
