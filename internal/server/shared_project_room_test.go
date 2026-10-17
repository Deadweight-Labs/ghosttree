package server

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Ein Projekt, das mit der Org geteilt ist, öffnet seinen Raum jedem Org-Mitglied
// mit der Standardrolle: kein 404 und kein 403 mehr für das Mitglied ohne eigene
// Projektrolle. Ein Gast bleibt bei seinen Projekten.
func TestSharedProjectOpensTheRoomForEveryOrgMember(t *testing.T) {
	f := roomGateFixture(t, true)
	room := store.RoomKeyForProject(accProject)
	if got := registerAgent(t, f, "nora", room); got != 404 {
		t.Fatalf("private project: nora registers: %d, want 404", got)
	}
	if err := f.st.SetProjectVisibility("person:1", accProject, true); err != nil {
		t.Fatal(err)
	}
	if got := registerAgent(t, f, "nora", room); got != 200 {
		t.Fatalf("shared project: nora registers: %d, want 200", got)
	}
	found := false
	for _, r := range roomsOf(t, f, "nora") {
		found = found || r.Key == room
	}
	if !found {
		t.Fatal("the shared project room is not listed for nora")
	}
	// Ein Projekt, das nicht geteilt ist, bleibt zu.
	if got := registerAgent(t, f, "nora2", store.RoomKeyForProject(accOther)); got == 200 {
		t.Fatalf("private project stays closed: %d", got)
	}
	// Zurück auf privat: der Raum schließt sich wieder.
	if err := f.st.SetProjectVisibility("person:1", accProject, false); err != nil {
		t.Fatal(err)
	}
	for _, r := range roomsOf(t, f, "nora") {
		if r.Key == room {
			t.Fatal("private again, but the room is still listed")
		}
	}
}
