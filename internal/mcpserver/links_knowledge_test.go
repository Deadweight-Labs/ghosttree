package mcpserver

import (
	"fmt"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-603 K1: auch context_retire, context_supersede und context_move
// antworten über genau einen Wissenseintrag und tragen deshalb Link und Karte.
func TestRetireSupersedeAndMoveAnswersCarryLinkAndCard(t *testing.T) {
	s, st, base, _ := linkWorld(t)
	add := func(title string) int64 {
		t.Helper()
		id, err := st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: title, Body: "b", Person: "alice", Scope: scope.Axes{Project: linkProject}})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	gone, old, fresh, moved := add("Der Tunnel ist zu"), add("Alter Port"), add("Neuer Port"), add("Gehört auf den Build-Server")

	got := wireText(t, s, "context_retire", map[string]any{"id": gone, "reason": "der Tunnel wurde geschlossen"})
	mustHave(t, "context_retire", got, "retired #", "Knowledge #"+fmt.Sprint(gone)+": Der Tunnel ist zu", "link: "+base+"/knowledge/"+fmt.Sprint(gone))

	got = wireText(t, s, "context_supersede", map[string]any{"id": fresh, "supersedes": []int64{old}, "reason": "der Port wurde geändert"})
	mustHave(t, "context_supersede", got, fmt.Sprintf("#%d now supersedes #%d", fresh, old), "Knowledge #"+fmt.Sprint(fresh)+": Neuer Port", "link: "+base+"/knowledge/"+fmt.Sprint(fresh))

	// Auf eine Maschine verschoben hat der Eintrag kein Projekt mehr; die Karte
	// trägt trotzdem den Link auf den Eintrag.
	got = wireText(t, s, "context_move", map[string]any{"id": moved, "machine": "build-1"})
	mustHave(t, "context_move", got, "moved #"+fmt.Sprint(moved), "Knowledge #"+fmt.Sprint(moved)+": Gehört auf den Build-Server", "/knowledge/"+fmt.Sprint(moved))
}
