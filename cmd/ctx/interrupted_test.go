package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Ohne die eigene Kennung meldet der Bootstrap einer wiederaufgenommenen
// Sitzung ihren eigenen Faden als unterbrochen — die Auskunft, die am
// wenigsten hilft.
func TestSessionStartTellsTheServerWhichSessionIsAsking(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query().Get("session")
		w.Write([]byte("## Known context\n"))
	}))
	defer srv.Close()
	withConfig(t, srv.URL)

	var out bytes.Buffer
	stdin := strings.NewReader(`{"cwd":"/tmp","session_id":"abc-123"}`)
	if code := cmdHookWith(stdin, []string{"session-start"}, &out); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if asked != "abc-123" {
		t.Fatalf("session = %q, want the harness session id", asked)
	}
}
