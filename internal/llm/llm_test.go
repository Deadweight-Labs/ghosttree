package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
