package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const promptWithSecret = "deploy with password=hunter2hunter2 and key sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA now"

// The prompt of a person goes in the body of a POST, redacted, never in the
// address a proxy writes to its log.
func TestRelevantSendsTheRedactedPromptInTheBody(t *testing.T) {
	var method, uri, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, uri = r.Method, r.URL.String()
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = w.Write([]byte("text"))
	}))
	defer srv.Close()
	c := New(config.Config{ServerURL: srv.URL, Token: "t"})
	d, err := c.RelevantDelivered(promptWithSecret, scope.Axes{Project: "github.com/x/y"}, 0, "s1")
	if err != nil || d.Text != "text" {
		t.Fatalf("%q %v", d.Text, err)
	}
	if method != "POST" {
		t.Fatalf("method %s", method)
	}
	if strings.Contains(uri, "deploy") || strings.Contains(uri, "hunter2") || strings.Contains(uri, "q=") {
		t.Fatalf("the prompt is in the address: %s", uri)
	}
	if !strings.Contains(body, "deploy") || strings.Contains(body, "hunter2") || strings.Contains(body, "sk-ant-") {
		t.Fatalf("body not redacted: %s", body)
	}
}

// Against an older server (no POST route) the GET form still works, with the
// redacted text.
func TestRelevantFallsBackToGetOnAnOlderServer(t *testing.T) {
	var uris []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uris = append(uris, r.Method+" "+r.URL.String())
		if r.Method == "POST" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write([]byte("alt"))
	}))
	defer srv.Close()
	c := New(config.Config{ServerURL: srv.URL, Token: "t"})
	d, err := c.RelevantDelivered(promptWithSecret, scope.Axes{}, 0, "")
	if err != nil || d.Text != "alt" {
		t.Fatalf("%q %v", d.Text, err)
	}
	if len(uris) != 2 || !strings.HasPrefix(uris[1], "GET ") || strings.Contains(uris[1], "hunter2") || strings.Contains(uris[1], "sk-ant") {
		t.Fatalf("%v", uris)
	}
}

func TestRunbookTriggerIsRedactedBeforeItLeaves(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}))
	defer srv.Close()
	c := New(config.Config{ServerURL: srv.URL, Token: "t"})
	if _, err := c.TriggeredRunbooksDelivered(RunbookTrigger{Prompt: promptWithSecret, Command: "curl -H 'Authorization: Bearer ghp_" + strings.Repeat("a", 36) + "' x"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "hunter2") || strings.Contains(body, "ghp_") || strings.Contains(body, "sk-ant-") {
		t.Fatalf("not redacted: %s", body)
	}
}

func TestForServerCutsLongText(t *testing.T) {
	got := ForServer(strings.Repeat("ä", maxPromptBytes))
	if len(got) > maxPromptBytes || strings.ContainsRune(got, '�') {
		t.Fatalf("%d bytes", len(got))
	}
}
