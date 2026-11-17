package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// S01 (usability run 5): a base address with and without "/v1" reaches the
// same path. Ollama and most OpenAI-compatible services document theirs with
// "/v1"; before, that became "/v1/v1/chat/completions" and a 404.
func TestEndpointTakesBaseWithAndWithoutV1(t *testing.T) {
	for _, tc := range []struct{ base, path, want string }{
		{"http://127.0.0.1:11434", "/v1/chat/completions", "http://127.0.0.1:11434/v1/chat/completions"},
		{"http://127.0.0.1:11434/v1", "/v1/chat/completions", "http://127.0.0.1:11434/v1/chat/completions"},
		{"http://127.0.0.1:11434/v1/", "/v1/chat/completions", "http://127.0.0.1:11434/v1/chat/completions"},
		{" https://api.anthropic.com/V1 ", "/v1/messages", "https://api.anthropic.com/v1/messages"},
		{"https://gw.example/openai", "/v1/chat/completions", "https://gw.example/openai/v1/chat/completions"},
		{"https://gw.example/v1beta", "/v1/messages", "https://gw.example/v1beta/v1/messages"},
	} {
		if got := Endpoint(tc.base, tc.path); got != tc.want {
			t.Errorf("Endpoint(%q, %q) = %q, want %q", tc.base, tc.path, got, tc.want)
		}
	}
}

func TestClientsReachTheProviderWithAndWithoutV1(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/v1/chat/completions":
			w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
		case "/v1/messages":
			w.Write([]byte(`{"content":[{"type":"text","text":"ok"}]}`))
		default:
			http.Error(w, "404 page not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	for _, format := range []string{"openai", "anthropic"} {
		for _, base := range []string{srv.URL, srv.URL + "/v1", srv.URL + "/v1/"} {
			c, err := New(Config{Format: format, BaseURL: base, APIKey: "k", Model: "m"})
			if err != nil {
				t.Fatal(err)
			}
			if out, err := c.Complete(context.Background(), "", []Message{{Role: "user", Content: "hi"}}, 10); err != nil || out != "ok" {
				t.Fatalf("%s with base %q: out=%q err=%v (paths %v)", format, base, out, err, paths)
			}
		}
	}
}
