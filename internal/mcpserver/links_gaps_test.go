package mcpserver

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-603 Kriterium 1: auch die Antworten über eine Raum-Nachricht, eine Session
// und eine gespeicherte Suche tragen Link und Karte.
func TestRoomMessageSessionAndPresetAnswersCarryLinkAndCard(t *testing.T) {
	s, st, base, _ := linkWorld(t)
	root := strings.TrimSuffix(base, "/x/y")

	got := wireText(t, s, "coord_send", map[string]any{"body": "Der Rollout ist durch", "room": "project"})
	mustHave(t, "coord_send", got, "stored as message 1", "Message #1: Der Rollout ist durch", "state: in project:"+linkProject,
		"link: "+root+"/coord?room=project%3A"+strings.ReplaceAll(linkProject, "/", "%2F"))

	sid, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "sess-card", Scope: scope.Axes{Project: linkProject}, Title: "Karten bauen"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendChunks(sid, []store.Chunk{{Seq: 0, Role: "user", Text: "Karten bauen", Raw: promptRaw("claude-code", "Karten bauen")}}); err != nil {
		t.Fatal(err)
	}
	listed, err := st.ListSessions(scope.Axes{Project: linkProject}, 10)
	if err != nil || len(listed) != 1 || listed[0].PublicID == "" {
		t.Fatalf("sessions %v: %v", listed, err)
	}
	got = wireText(t, s, "context_sessions", map[string]any{"session_id": sid})
	mustHave(t, "context_sessions session_id", got, "Karten bauen", "Session "+listed[0].PublicID, "link: "+base+"/sessions/"+listed[0].PublicID)

	got = wireText(t, s, "search_preset", map[string]any{"action": "run", "name": "branch-offen", "branch": "feat/x"})
	mustHave(t, "search_preset run", got, "Search preset:branch-offen", "link: "+root+"/presets/repo/branch-offen?branch=feat%2Fx&project=x%2Fy")
	got = wireText(t, s, "search_preset", map[string]any{"action": "save", "name": "pr-bugs", "queries": []string{"type:bug pr:{pr}"}, "description": "Bugs an einem PR"})
	mustHave(t, "search_preset save", got, "Search preset:pr-bugs", "link: "+root+"/presets/me/pr-bugs")
}

func TestThreadProposalAnswerCarriesLinkAndCard(t *testing.T) {
	s, _, base, _ := linkWorld(t)
	wireText(t, s, "thread_open", map[string]any{"title": "Frage", "question": "Wie weiter?"})
	got := wireText(t, s, "thread_propose", map[string]any{"id": 1, "kind": "knowledge", "note": "Das gehört ins Wissen"})
	mustHave(t, "thread_propose", got, "recorded as a proposal on thread 1", "Room thread 1: Frage", "link: "+base+"/rooms/1")
}
