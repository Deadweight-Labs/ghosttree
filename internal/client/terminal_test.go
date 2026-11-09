package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Die Terminal-Markierung geht nur bei Schreibzugriffen mit, und nie, wenn der
// Client eine Agenten-Session kennt.
func TestTerminalPersonMarksWritesOnly(t *testing.T) {
	got := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got[r.Method] = r.Header.Get(actor.ViaHeader)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()
	old := TerminalPerson
	t.Cleanup(func() { TerminalPerson = old })
	TerminalPerson = true
	c := New(config.Config{ServerURL: srv.URL, Token: "t"})
	ax := scope.Axes{Project: "github.com/x/y"}
	_, _ = c.Remember(store.Knowledge{Type: "note", Title: "x", Body: "y", Scope: ax}, ax)
	_, _ = c.Sessions(ax, 1)
	if got["POST"] != actor.ViaTerminal || got["GET"] != "" {
		t.Errorf("headers: %+v", got)
	}
	got = map[string]string{}
	c.SetOriginSession("agent-session")
	_, _ = c.Remember(store.Knowledge{Type: "note", Title: "x", Body: "y", Scope: ax}, ax)
	if got["POST"] != "" {
		t.Errorf("a client with an agent session claims a person: %q", got["POST"])
	}
}
