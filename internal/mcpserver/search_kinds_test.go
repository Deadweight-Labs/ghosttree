package mcpserver

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestContextSearchQualifiersReachRoomMessagesAndSessions(t *testing.T) {
	c, st := newTestClient(t)
	project := "github.com/x/y"
	room := store.RoomKeyForProject(project)
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "agent-1", PrincipalID: "person:1", Person: "owner", Provider: "test", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CoordinationFor(store.Principal{ID: "person:1"}, "agent-1").Send(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: "m-1", Body: "Die Abnahme von Marker laeuft",
	}); err != nil {
		t.Fatal(err)
	}
	sid, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "sess-1", Scope: scope.Axes{Project: project}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendChunks(sid, []store.Chunk{{Seq: 0, Role: "user", Text: "Marker im Transkript", Raw: promptRaw("claude-code", "Marker im Transkript")}}); err != nil {
		t.Fatal(err)
	}
	session := connect(t, &Server{client: c, sessionRef: "agent-1", ctxAxes: scope.Axes{Project: project}})
	got, failed := callTool(t, session, "context_search", map[string]any{"query": "marker kind:message repo:x/y"})
	if failed || !strings.Contains(got, "## room messages") || !strings.Contains(got, "Abnahme von Marker") || !strings.Contains(got, "[message|") {
		t.Errorf("room message hit: %v %s", failed, got)
	}
	if got, _ := callTool(t, session, "context_search", map[string]any{"query": "marker kind:message author:someone-else"}); !strings.Contains(got, "no results") {
		t.Errorf("author: filters messages: %s", got)
	}
	got, failed = callTool(t, session, "context_search", map[string]any{"query": "marker kind:session repo:x/y"})
	if failed || !strings.Contains(got, "## sessions") || !strings.Contains(got, "Marker im Transkript") {
		t.Errorf("session hit: %v %s", failed, got)
	}
	if got, _ := callTool(t, session, "context_search", map[string]any{"query": "marker kind:session is:open"}); !strings.Contains(got, "no results") {
		t.Errorf("a session has no state: %s", got)
	}
}
