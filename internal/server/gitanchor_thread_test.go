package server

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func threadEventsByKind(t *testing.T, st *store.Store, id int64) map[string]store.ThreadEvent {
	t.Helper()
	events, err := st.ThreadEvents(id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.ThreadEvent{}
	for _, e := range events {
		out[e.Kind] = e
	}
	return out
}

// Thread-Aktionen außer Nachrichten (öffnen, Zustand, Ergebnis vorschlagen)
// tragen den Git-Anker des Schreibers (REQ-597, Kriterium 9).
func TestThreadActionsCarryTheGitAnchor(t *testing.T) {
	srv, st, token, _ := coordinationAccessServer(t)
	room := store.RoomKeyForProject(anchorProject)
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "sess-a", PrincipalID: "person:1", Person: "owner", Provider: "test", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	c := client.New(config.Config{ServerURL: srv.URL, Token: token})
	const commit = "dddddddd4444444444444444444444444444dddd"
	c.SetAnchorSource(func() gitanchor.Anchor { return gitanchor.Anchor{Commit: commit, Branch: "feat/threads"} })

	id, err := c.CreateThread(store.Thread{Project: anchorProject, Title: "Welche Form?"}, "sess-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.PutThreadOutcome(store.ThreadOutcome{ThreadID: id, Kind: "decision", RefID: "d1", Note: "so"}, "sess-a"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetThreadState(id, "resolved", "sess-a"); err != nil {
		t.Fatal(err)
	}
	events := threadEventsByKind(t, st, id)
	for _, kind := range []string{store.ThreadEventOpened, store.ThreadEventOutcome, store.ThreadEventState} {
		e, ok := events[kind]
		if !ok || e.GitAnchor == nil || e.GitAnchor.Commit != commit || e.GitAnchor.Branch != "feat/threads" {
			t.Errorf("%s: %+v", kind, e)
		}
	}
	if e := events[store.ThreadEventState]; e.Data != "resolved" {
		t.Errorf("the state event names the new state: %+v", e)
	}

	// Ein Aufruf, der den Zustand nicht ändert, schreibt nichts und ist kein
	// Ereignis; Archivieren auch nicht.
	if err := c.SetThreadState(id, "resolved", "sess-a"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetThreadArchived(id, true, "sess-a"); err != nil {
		t.Fatal(err)
	}
	all, _ := st.ThreadEvents(id)
	if len(all) != 3 {
		t.Fatalf("events = %d, want 3 (opened, outcome, state): %+v", len(all), all)
	}

	// Ohne Quelle (Hooks, Verwaltungsbefehle) geht kein Anker mit, es gelingt trotzdem.
	plain := client.New(config.Config{ServerURL: srv.URL, Token: token})
	id2, err := plain.CreateThread(store.Thread{Project: anchorProject, Title: "Ohne Anker"}, "sess-a")
	if err != nil {
		t.Fatal(err)
	}
	if e := threadEventsByKind(t, st, id2)[store.ThreadEventOpened]; e.ID == 0 || e.GitAnchor != nil {
		t.Fatalf("an unanchored thread: %+v", e)
	}
}

// Der Body ist nie die Quelle des Ankers: ein erfundenes git_anchor im JSON wird
// nicht gespeichert, auch nicht an Thread und Ergebnis.
func TestThreadAnchorNeverComesFromTheBody(t *testing.T) {
	srv, st, token, _ := coordinationAccessServer(t)
	room := store.RoomKeyForProject(anchorProject)
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "sess-a", PrincipalID: "person:1", Person: "owner", Provider: "test", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	forged := map[string]any{"commit": "eeeeeeee5555555555555555555555555555eeee", "branch": "erfunden"}
	res := req(t, "POST", srv.URL+"/api/threads?agent_external_id=sess-a", token, map[string]any{
		"project": anchorProject, "title": "Erfunden", "git_anchor": forged})
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create = %d", res.StatusCode)
	}
	threads, err := st.SearchThreads(anchorProject, "Erfunden", false, 5)
	if err != nil || len(threads) != 1 {
		t.Fatalf("threads %v %v", threads, err)
	}
	id := threads[0].ID
	res = req(t, "POST", srv.URL+"/api/threads/"+strconv.FormatInt(id, 10)+"/outcomes?agent_external_id=sess-a", token, map[string]any{
		"kind": "decision", "ref_id": "d9", "git_anchor": forged})
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("outcome = %d", res.StatusCode)
	}
	events, _ := st.ThreadEvents(id)
	if len(events) != 2 {
		t.Fatalf("events %+v", events)
	}
	for _, e := range events {
		if e.GitAnchor != nil {
			t.Errorf("%s took an anchor from the body: %+v", e.Kind, e.GitAnchor)
		}
	}
}
