package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type inboxItem struct {
	Kind  string `json:"kind"`
	Ref   string `json:"ref"`
	State string `json:"state"`
	How   string `json:"how"`
	Title string `json:"title"`
	Link  string `json:"link"`
}

func inboxOf(t *testing.T, f *accessAPIFixture, who, query string) (int, []inboxItem, string) {
	t.Helper()
	code, body := f.call(t, who, "GET", "/api/inbox"+query, nil)
	var list []inboxItem
	if code == 200 {
		if err := json.Unmarshal([]byte(body), &list); err != nil {
			t.Fatalf("decode %q: %v", body, err)
		}
	}
	return code, list, body
}

func TestInboxRouteKeepsFinishedEntriesAndAnswersByRights(t *testing.T) {
	f := accessAPI(t, true)
	req := f.id["r-mia"]
	mk := func(title string) int64 {
		return f.post(t, "mia", "/api/tasks", map[string]any{"project": accProject, "kind": "action", "title": title, "location": "x", "everyone": true, "request_id": req}, "id")
	}
	openID, doneID, dropID := mk("offene Aufgabe"), mk("erledigte Aufgabe"), mk("verworfene Aufgabe")
	mia := f.st.Access(store.Principal{ID: "person:3", Label: "mia"})
	actor := humantask.Actor{AccountID: 3, Person: "mia"}
	if _, err := mia.MarkTasksDone([]int64{doneID}, "fertig", actor); err != nil {
		t.Fatal(err)
	}
	if _, err := mia.DropHumanTask(dropID, "unnötig", actor); err != nil {
		t.Fatal(err)
	}

	code, open, _ := inboxOf(t, f, "mia", "")
	if code != 200 || len(open) != 1 || open[0].Ref != fmt.Sprintf("TASK-%d", openID) || open[0].State != "open" || !strings.HasSuffix(open[0].Link, fmt.Sprintf("/tasks/%d", openID)) {
		t.Fatalf("open: %d %+v", code, open)
	}
	_, done, _ := inboxOf(t, f, "mia", "?state=done")
	hows := map[string]string{}
	for _, e := range done {
		hows[e.Ref] = e.How
	}
	if len(done) != 2 || hows[fmt.Sprintf("TASK-%d", doneID)] != "done" || hows[fmt.Sprintf("TASK-%d", dropID)] != "dropped" {
		t.Fatalf("done: %+v", done)
	}
	if _, all, _ := inboxOf(t, f, "mia", "?state=all&project="+accProject); len(all) != 3 || all[0].State != "open" {
		t.Fatalf("all: %+v", all)
	}
	if _, mine, _ := inboxOf(t, f, "rex", "?state=done&mine=1"); len(mine) != 0 {
		t.Fatalf("rex finished nothing: %+v", mine)
	}
	// Fenster: days wird gedeckelt, 0 heißt Standard.
	if code, list, _ := inboxOf(t, f, "mia", "?state=done&days=1000000"); code != 200 || len(list) != 2 {
		t.Fatalf("days beyond the maximum: %d %+v", code, list)
	}

	// Fremder und Gast; ein Projekt, das es nicht gibt, antwortet wie eines, das der Fremde nicht sieht.
	for _, q := range []string{"?state=all", "?state=all&project=" + accProject, "?state=all&project=github.com/nobody/nothing"} {
		code, list, body := inboxOf(t, f, "nora", q)
		if code != 200 || len(list) != 0 || strings.TrimSpace(body) != "[]" {
			t.Errorf("stranger %s: %d %s", q, code, body)
		}
	}
	if _, list, _ := inboxOf(t, f, "gus", "?state=all&project="+accProject); len(list) != 0 {
		t.Errorf("a guest sees tasks of nobody: %+v", list)
	}
	// Ehemaliges Mitglied.
	if err := f.st.RemoveProjectRole("person:1", accProject, "person:3", store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	p, _ := f.st.ProjectByRemote(accProject)
	if err := f.st.RemoveOrgMember("person:1", p.OrgID, "person:3"); err != nil {
		t.Fatal(err)
	}
	if code, list, body := inboxOf(t, f, "mia", "?state=all"); code != 200 || len(list) != 0 {
		t.Errorf("former member: %d %s", code, body)
	}
	f.expect(t, "robin", 400, "GET", "/api/inbox?state=weg", nil)
	if code, _ := f.call(t, "", "GET", "/api/inbox", nil); code != 401 {
		t.Errorf("without a token: %d", code)
	}
}
