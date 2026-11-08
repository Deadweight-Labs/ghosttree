package store

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
)

const threadEventProject = "github.com/dw/threads"

func threadEventFixture(t *testing.T) (*Store, CoordAccess) {
	t.Helper()
	st := openAnchorStore(t)
	if _, err := st.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RegisterCoordAgent(CoordAgent{ExternalID: "sess-a", PrincipalID: "person:1", Person: "robin", Provider: "test", RoomKey: RoomKeyForProject(threadEventProject)}); err != nil {
		t.Fatal(err)
	}
	return st, st.CoordinationFor(Principal{ID: "person:1", Label: "robin"}, "sess-a")
}

func TestThreadEventsKeepAnchorPerWriteAndMarkWebWithoutOne(t *testing.T) {
	st, access := threadEventFixture(t)
	id, err := access.CreateThread(Thread{Project: threadEventProject, Title: "Frage", GitAnchor: anchorAt(commitA, "main")})
	if err != nil {
		t.Fatal(err)
	}
	// Aus dem Terminal gelöst: Anker des Schreibers. Danach aus dem Browser
	// wieder geöffnet: kein Anker, aber Web.
	if err := access.SetThreadState(id, ThreadResolved, anchorAt(commitB, "feat/x")); err != nil {
		t.Fatal(err)
	}
	if err := access.SetThreadState(id, ThreadOpen, gitanchor.Web()); err != nil {
		t.Fatal(err)
	}
	events, err := st.ThreadEvents(id)
	if err != nil || len(events) != 3 {
		t.Fatalf("events %+v %v", events, err)
	}
	opened, resolved, reopened := events[0], events[1], events[2]
	if opened.GitAnchor == nil || opened.GitAnchor.Commit != commitA || opened.Via != "" {
		t.Errorf("opened: %+v", opened)
	}
	if resolved.GitAnchor == nil || resolved.GitAnchor.Commit != commitB || resolved.Data != ThreadResolved {
		t.Errorf("resolved: %+v", resolved)
	}
	if reopened.GitAnchor != nil || reopened.Via != "web" || reopened.Data != ThreadOpen {
		t.Errorf("reopened from the web: %+v", reopened)
	}
	// Der Anker eines Ereignisses ändert sich nicht, wenn ein späteres geschrieben wird.
	if again, _ := st.ThreadEvents(id); again[0].GitAnchor.Commit != commitA {
		t.Errorf("the first anchor must stay: %+v", again[0].GitAnchor)
	}
}

func TestThreadOpenedByRoleCarriesTheAnchor(t *testing.T) {
	st, _ := threadEventFixture(t)
	id, err := st.CreateThreadByRole(Principal{ID: "person:1", Label: "robin"}, Thread{Project: threadEventProject, Title: "Andere Rolle", GitAnchor: anchorAt(commitA, "main")})
	if err != nil {
		t.Fatal(err)
	}
	events, err := st.ThreadEvents(id)
	if err != nil || len(events) != 1 || events[0].GitAnchor == nil || events[0].GitAnchor.Commit != commitA {
		t.Fatalf("events %+v %v", events, err)
	}
}
