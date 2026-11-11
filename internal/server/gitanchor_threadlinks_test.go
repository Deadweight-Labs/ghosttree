package server

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-597 K9: auch Verknüpfungen und Zusammenfassungen eines Threads tragen den
// Git-Anker des Schreibers; eine Verknüpfung, die es schon gab, ist kein Ereignis.
func TestThreadLinksAndSummariesCarryTheGitAnchor(t *testing.T) {
	srv, st, token, _ := coordinationAccessServer(t)
	room := store.RoomKeyForProject(anchorProject)
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "sess-a", PrincipalID: "person:1", Person: "owner", Provider: "test", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	c := client.New(config.Config{ServerURL: srv.URL, Token: token})
	const commit = "ffffffff6666666666666666666666666666ffff"
	c.SetAnchorSource(func() gitanchor.Anchor { return gitanchor.Anchor{Commit: commit, Branch: "feat/links"} })

	id, err := c.CreateThread(store.Thread{Project: anchorProject, Title: "Wohin damit?"}, "sess-a")
	if err != nil {
		t.Fatal(err)
	}
	link := store.ThreadLink{ThreadID: id, Kind: "knowledge", ID: "7"}
	if err := c.LinkThread(link, "sess-a"); err != nil {
		t.Fatal(err)
	}
	if err := c.LinkThread(link, "sess-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutThreadSummary(store.ThreadSummary{ThreadID: id, Body: "Stand der Dinge"}, "sess-a"); err != nil {
		t.Fatal(err)
	}
	events, err := st.ThreadEvents(id)
	if err != nil {
		t.Fatal(err)
	}
	var linked, summaries int
	for _, e := range events {
		switch e.Kind {
		case store.ThreadEventLinked:
			linked++
		case store.ThreadEventSummary:
			summaries++
		default:
			continue
		}
		if e.GitAnchor == nil || e.GitAnchor.Commit != commit || e.GitAnchor.Branch != "feat/links" {
			t.Errorf("%s without the writer's git state: %+v", e.Kind, e)
		}
	}
	if linked != 1 || summaries != 1 {
		t.Fatalf("link events %d, summary events %d, want 1 and 1 (a repeated link is no event): %+v", linked, summaries, events)
	}

	// Ohne Quelle geht kein Anker mit, es gelingt trotzdem.
	plain := client.New(config.Config{ServerURL: srv.URL, Token: token})
	if err := plain.LinkThread(store.ThreadLink{ThreadID: id, Kind: "knowledge", ID: "8"}, "sess-a"); err != nil {
		t.Fatal(err)
	}
	events, _ = st.ThreadEvents(id)
	if last := events[len(events)-1]; last.Kind != store.ThreadEventLinked || last.GitAnchor != nil {
		t.Errorf("an unanchored link: %+v", last)
	}
}

// Der Anker kommt nur aus dem Header: ein git_anchor im Rumpf wird nicht
// gespeichert, und "web" lässt sich nicht behaupten.
func TestThreadLinkAndSummaryAnchorNeverComesFromTheBody(t *testing.T) {
	srv, st, token, _ := coordinationAccessServer(t)
	room := store.RoomKeyForProject(anchorProject)
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "sess-a", PrincipalID: "person:1", Person: "owner", Provider: "test", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	c := client.New(config.Config{ServerURL: srv.URL, Token: token})
	id, err := c.CreateThread(store.Thread{Project: anchorProject, Title: "Erfunden"}, "sess-a")
	if err != nil {
		t.Fatal(err)
	}
	forged := map[string]any{"commit": "eeeeeeee5555555555555555555555555555eeee", "branch": "erfunden", "via": "web"}
	base := srv.URL + "/api/threads/" + strconv.FormatInt(id, 10)
	for path, body := range map[string]map[string]any{
		"/links":   {"kind": "knowledge", "id": "9", "git_anchor": forged},
		"/summary": {"body": "x", "git_anchor": forged},
	} {
		res := req(t, "POST", base+path+"?agent_external_id=sess-a", token, body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d", path, res.StatusCode)
		}
	}
	events, _ := st.ThreadEvents(id)
	for _, e := range events {
		if e.Kind != store.ThreadEventLinked && e.Kind != store.ThreadEventSummary {
			continue
		}
		if e.GitAnchor != nil || e.Via != "" {
			t.Errorf("%s took anchor or origin from the body: %+v", e.Kind, e)
		}
	}
}

// Die Korrektur eines Requests über die API trägt den Anker des Schreibers.
func TestRequestCorrectionCarriesTheGitAnchorAndNeverTakesItFromTheBody(t *testing.T) {
	srv, token := newTestServer(t)
	c := client.New(config.Config{ServerURL: srv.URL, Token: token})
	const commit = "abababab7777777777777777777777777777abab"
	c.SetAnchorSource(func() gitanchor.Anchor { return gitanchor.Anchor{Commit: commit, Branch: "feat/correct"} })
	d, err := c.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Alt", Scope: scope.Axes{Project: anchorProject}, Origin: "agent"}, Criteria: []string{"eins"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CorrectRequest(d.Request.ID, map[string]string{"title": "Neu"}, "ungenau"); err != nil {
		t.Fatal(err)
	}
	// Ein erfundener Anker im Rumpf ohne Header wird nicht gespeichert.
	res := req(t, "PATCH", srv.URL+"/api/requests/"+strconv.FormatInt(d.Request.ID, 10), token, map[string]any{
		"patch": map[string]string{"title": "Neuer"}, "reason": "nochmal", "git_anchor": map[string]any{"commit": "eeeeeeee5555555555555555555555555555eeee", "via": "web"}})
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch = %d", res.StatusCode)
	}
	got, err := c.GetRequest(d.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	var withAnchor, forged int
	for _, a := range got.Activity {
		if a.Kind != "request.corrected" {
			continue
		}
		if a.GitAnchor != nil && a.GitAnchor.Commit == commit {
			withAnchor++
		} else if a.GitAnchor != nil || a.Via != "" {
			forged++
		}
	}
	if withAnchor != 1 || forged != 0 {
		t.Fatalf("corrections with the anchor %d, with a forged one %d: %+v", withAnchor, forged, got.Activity)
	}
}
