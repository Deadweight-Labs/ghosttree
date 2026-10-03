package server

import (
	"fmt"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Wer die Session-Nummern nicht kennen darf, darf keine Arbeit beenden, deren
// Session ihm verborgen ist, und erfährt auch nicht, dass es sie gibt (#2485).
func TestFinishWorkOfHiddenSessionLooksLikeUnknownId(t *testing.T) {
	f := accessAPI(t, true)
	mk := func(ext string, account int64, project string) int64 {
		id, err := f.st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: ext, AccountID: account, Scope: scope.Axes{Project: project}})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	global, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "global", Person: "robin"}})
	if err != nil {
		t.Fatal(err)
	}
	start := func(rid, sid int64, person string) int64 {
		w, _, err := f.st.StartRequestWork(rid, sid, "primary", person)
		if err != nil {
			t.Fatal(err)
		}
		return w.ID
	}
	hidden := start(global.Request.ID, mk("w-gus", 5, accProject), "gus")
	own := start(global.Request.ID, mk("w-nora", 6, accProject), "nora")
	body := map[string]string{"state": "paused", "summary": "x"}
	path := func(id int64) string { return fmt.Sprintf("/api/request-work/%d", id) }

	code, unknown := f.call(t, "nora", "PATCH", path(99999), body)
	if code != 404 {
		t.Fatalf("unknown id: %d %s", code, unknown)
	}
	code, got := f.call(t, "nora", "PATCH", path(hidden), body)
	if code != 404 || got != unknown {
		t.Fatalf("hidden work: %d %q, want 404 %q", code, got, unknown)
	}
	// Auch ein zweiter Versuch, nachdem sie beendet wurde, bleibt gleich.
	f.expect(t, "gus", 200, "PATCH", path(hidden), body)
	if code, got = f.call(t, "nora", "PATCH", path(hidden), body); code != 404 || got != unknown {
		t.Fatalf("finished hidden work: %d %q", code, got)
	}
	if out := f.expect(t, "nora", 200, "PATCH", path(own), body); strings.Contains(out, "session_id") {
		t.Errorf("session number leaked: %s", out)
	}
}

func TestMemberMayFinishForeignWorkInClaimedProject(t *testing.T) {
	f := accessAPI(t, true)
	sid, err := f.st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "w-rex", AccountID: 4, Scope: scope.Axes{Project: accProject}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "p", Person: "mia", Scope: scope.Axes{Project: accProject}}})
	if err != nil {
		t.Fatal(err)
	}
	w, _, err := f.st.StartRequestWork(d.Request.ID, sid, "primary", "rex")
	if err != nil {
		t.Fatal(err)
	}
	f.expect(t, "mia", 200, "PATCH", fmt.Sprintf("/api/request-work/%d", w.ID), map[string]string{"state": "paused", "summary": "x"})
}
