package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Kein Ziel aus dem Formular: ein back-Feld lenkt nirgendwohin um, ohne Raum geht
// es zurück auf die Seite des Agenten, gebaut aus seiner Kennung.
func TestAgentControlRedirectsOnlyToBuiltTargets(t *testing.T) {
	a := &app{}
	for _, c := range []struct{ room, back, want string }{
		{"", "/ui/agents/../../evil", "/ui/agents/a%2Fb"},
		{"", "https://evil.example/", "/ui/agents/a%2Fb"},
		{"project:x", "/ui/agents/a", "/ui/coord?room=project%3Ax"},
	} {
		form := url.Values{"agent": {"a/b"}, "room": {c.room}, "back": {c.back}}
		r := httptest.NewRequest(http.MethodPost, "/ui/coord/agent/control", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		a.redirectAfterControl(w, r, "a/b")
		if got := w.Header().Get("Location"); got != c.want {
			t.Errorf("room %q back %q: redirect %q, want %q", c.room, c.back, got, c.want)
		}
	}
}
