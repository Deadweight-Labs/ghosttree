package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenAIFormat(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"choices":[{"message":{"content":"hello"}}]}`))
	}))
	defer srv.Close()
	c, err := New(Config{Format: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Complete(context.Background(), "sys", []Message{{Role: "user", Content: "hi"}}, 100)
	if err != nil || out != "hello" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if got["model"] != "m" {
		t.Errorf("model not sent: %v", got)
	}
	if msgs, _ := got["messages"].([]any); len(msgs) != 2 {
		t.Errorf("system message missing: %v", msgs)
	}
}

func TestAnthropicFormat(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "k" {
			t.Errorf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Error("anthropic-version missing")
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"content":[{"type":"text","text":"hello"}]}`))
	}))
	defer srv.Close()
	c, _ := New(Config{Format: "anthropic", BaseURL: srv.URL, APIKey: "k", Model: "m"})
	out, err := c.Complete(context.Background(), "sys", []Message{{Role: "user", Content: "hi"}}, 100)
	if err != nil || out != "hello" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if got["system"] != "sys" {
		t.Errorf("system=%v", got["system"])
	}
}

func TestUnknownFormatRejected(t *testing.T) {
	if _, err := New(Config{Format: "gemini"}); err == nil {
		t.Error("unknown format accepted")
	}
}

func TestOpenAIJSONModeRequestsJSONObject(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer srv.Close()
	c, _ := New(Config{Format: "openai", BaseURL: srv.URL, Model: "m"})
	if _, err := c.(JSONClient).CompleteJSON(context.Background(), "sys", []Message{{Role: "user", Content: "x"}}, 10); err != nil {
		t.Fatal(err)
	}
	format, _ := got["response_format"].(map[string]any)
	if format["type"] != "json_object" {
		t.Errorf("response_format=%v", got["response_format"])
	}
}

// The distiller runs as a systemd DynamicUser, which has no home directory,
// so a configuration path that only resolves under os.UserConfigDir can never
// be reached from the unit that needs it.
func TestLoadConfigUsesExplicitPathWhenSet(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "llm.json")
	if err := os.WriteFile(cfgPath, []byte(`{"format":"openai","base_url":"https://llm.example.invalid/v1","model":"test-model","api_key_file":"openai-key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "openai-key"), []byte("sk-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Isolate the real configuration: without this the lookup falls back to
	// the operator's own home and a failure prints their live API key.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "empty"))
	t.Setenv("GHOSTTREE_LLM_CONFIG", cfgPath)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig with explicit path: %v", err)
	}
	if cfg.Model != "test-model" {
		t.Errorf("Model = %q, want the model from the explicit path", cfg.Model)
	}
	if cfg.APIKey != "sk-test" {
		t.Errorf("APIKey did not come from the explicit path (length %d)", len(cfg.APIKey))
	}
}

// systemd passes secrets through $CREDENTIALS_DIRECTORY rather than a file the
// service can read directly, which keeps the key out of the state directory.
func TestLoadConfigReadsKeyFromSystemdCredentials(t *testing.T) {
	dir := t.TempDir()
	creds := filepath.Join(dir, "creds")
	if err := os.MkdirAll(creds, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "llm.json")
	if err := os.WriteFile(cfgPath, []byte(`{"format":"openai","base_url":"https://llm.example.invalid/v1","model":"test-model","credential":"llm-key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(creds, "llm-key"), []byte("sk-cred\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "empty"))
	t.Setenv("GHOSTTREE_LLM_CONFIG", cfgPath)
	t.Setenv("CREDENTIALS_DIRECTORY", creds)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig with credentials: %v", err)
	}
	if cfg.APIKey != "sk-cred" {
		t.Errorf("APIKey did not come from $CREDENTIALS_DIRECTORY (length %d)", len(cfg.APIKey))
	}
}

// The same configuration has to serve both callers. The service receives the
// key as a systemd credential; an operator running the identical command by
// hand has no $CREDENTIALS_DIRECTORY, and forcing a second configuration file
// for that case invites the two to drift apart and the manual run to use a key
// nobody remembers writing.
func TestLoadConfigFallsBackFromCredentialToFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "llm.json")
	if err := os.WriteFile(cfgPath, []byte(`{"format":"openai","base_url":"https://llm.example.invalid/v1","model":"test-model","credential":"llm-key","api_key_file":"llm-key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "llm-key"), []byte("sk-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "empty"))
	t.Setenv("GHOSTTREE_LLM_CONFIG", cfgPath)
	t.Setenv("CREDENTIALS_DIRECTORY", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig outside a unit: %v", err)
	}
	if cfg.APIKey != "sk-file" {
		t.Errorf("APIKey did not fall back to the file (length %d)", len(cfg.APIKey))
	}
}

// Without a named fallback there is nothing to fall back to, and guessing a
// path is how the wrong key gets used silently.
func TestLoadConfigFailsWhenCredentialIsUnavailableAndNoFileNamed(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "llm.json")
	if err := os.WriteFile(cfgPath, []byte(`{"format":"openai","base_url":"https://llm.example.invalid/v1","model":"test-model","credential":"llm-key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "empty"))
	t.Setenv("GHOSTTREE_LLM_CONFIG", cfgPath)
	t.Setenv("CREDENTIALS_DIRECTORY", "")

	if _, err := LoadConfig(); err == nil {
		t.Fatal("LoadConfig accepted an unreachable credential with no named fallback")
	}
}
