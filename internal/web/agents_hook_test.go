package web

import (
	"regexp"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestAgentCardIsActiveWhileItsHooksFireAndIdleOtherwise(t *testing.T) {
	e := seedSessions(t)
	room := store.RoomKeyForProject(shellProject)
	for id, hook := range map[string]bool{"sess-hooked-live": true, "sess-registered-only": false} {
		if _, err := e.St.RegisterCoordAgent(store.CoordAgent{ExternalID: id, PrincipalID: "person:1", Person: "alice", Provider: "claude",
			RoomKey: room, Branch: "main", HookActivity: hook}); err != nil {
			t.Fatal(err)
		}
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/agents")
	stateOf := func(id string) string {
		m := regexp.MustCompile(`<li class="ag-card is-(\w+)" title="` + regexp.QuoteMeta(id) + `"`).FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("no card for %s", id)
		}
		return m[1]
	}
	if got := stateOf("sess-hooked-live"); got != "active" {
		t.Errorf("a session whose hooks fire is shown as %q, want active", got)
	}
	if got := stateOf("sess-registered-only"); got != "idle" {
		t.Errorf("a session that only registered is shown as %q, want idle", got)
	}
}
