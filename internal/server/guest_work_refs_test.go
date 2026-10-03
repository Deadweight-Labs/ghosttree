package server

import (
	"fmt"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func workSession(t *testing.T, f *accessAPIFixture, ext string, account int64, project string) int64 {
	t.Helper()
	id, err := f.st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: ext, AccountID: account, Scope: scope.Axes{Project: project}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Die Warnung beim Start zählt nur Arbeit mit einer Session, die der Betrachter
// lesen darf; sonst verriete schon die Zahl verborgene Arbeit.
func TestStartWarningDoesNotCountHiddenWork(t *testing.T) {
	f := accessAPI(t, true)
	g, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "g", Person: "robin"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.StartRequestWork(g.Request.ID, workSession(t, f, "wm-mia", 3, accProject), "primary", "mia"); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/requests/%d/work", g.Request.ID)
	for _, who := range []string{"gus", "nora"} {
		acc := map[string]int64{"gus": 5, "nora": 6}[who]
		out := f.expect(t, who, 201, "POST", path, map[string]any{"session_id": workSession(t, f, "wm-"+who, acc, accProject), "role": "primary"})
		if strings.Contains(out, "already has") || strings.Contains(out, "active primary") {
			t.Errorf("%s: warning reveals hidden work: %s", who, out)
		}
	}
}

// Ist die Arbeit lesbar (geteilte Session), bleibt die Warnung.
func TestStartWarningCountsReadableWork(t *testing.T) {
	f := accessAPI(t, true)
	g, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "g", Person: "robin"}})
	if err != nil {
		t.Fatal(err)
	}
	shared := workSession(t, f, "wr-rex", 4, accProject)
	if err := f.st.SetSessionVisibility(shared, f.st.Access(store.Principal{ID: "person:4", Label: "rex"}), store.VisGuests); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.StartRequestWork(g.Request.ID, shared, "primary", "rex"); err != nil {
		t.Fatal(err)
	}
	out := f.expect(t, "gus", 201, "POST", fmt.Sprintf("/api/requests/%d/work", g.Request.ID), map[string]any{"session_id": workSession(t, f, "wr-gus", 5, accProject), "role": "primary"})
	if !strings.Contains(out, "already has 1 active primary") {
		t.Errorf("readable work not counted: %s", out)
	}
}

// Ein verborgenes Projekt antwortet über jede Route des Auftrags byte-gleich wie
// eine unbekannte Id.
func TestHiddenProjectRequestAnswersLikeUnknownId(t *testing.T) {
	f := accessAPI(t, true)
	d, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "h", Person: "robin", Scope: scope.Axes{Project: accOther}}})
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"session_id": workSession(t, f, "hp-gus", 5, accProject), "role": "related"}
	code, unknown := f.call(t, "gus", "POST", "/api/requests/99999/work", body)
	if code != 404 {
		t.Fatalf("unknown: %d %s", code, unknown)
	}
	if code, got := f.call(t, "gus", "POST", fmt.Sprintf("/api/requests/%d/work", d.Request.ID), body); code != 404 || got != unknown {
		t.Errorf("hidden project: %d %q, want 404 %q", code, got, unknown)
	}
}

// Ohne Nummernsicht startet man nur mit der eigenen Session, auch wenn eine
// fremde lesbar ist, und beendet nur Arbeit der eigenen Session.
func TestWithoutNumberViewOnlyOwnSessionStartsAndEnds(t *testing.T) {
	f := accessAPI(t, true)
	g, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "g", Person: "robin"}})
	if err != nil {
		t.Fatal(err)
	}
	shared := workSession(t, f, "ow-rex", 4, accProject)
	if err := f.st.SetSessionVisibility(shared, f.st.Access(store.Principal{ID: "person:4", Label: "rex"}), store.VisGuests); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/requests/%d/work", g.Request.ID)
	code, unknown := f.call(t, "gus", "POST", path, map[string]any{"session_id": 99999, "role": "related"})
	if code != 404 {
		t.Fatalf("unknown session: %d", code)
	}
	if code, got := f.call(t, "gus", "POST", path, map[string]any{"session_id": shared, "role": "related"}); code != 404 || got != unknown {
		t.Errorf("foreign readable session start: %d %q", code, got)
	}
	w, _, err := f.st.StartRequestWork(g.Request.ID, shared, "related", "rex")
	if err != nil {
		t.Fatal(err)
	}
	_, unknownWork := f.call(t, "gus", "PATCH", "/api/request-work/99999", map[string]string{"state": "paused", "summary": "x"})
	if code, got := f.call(t, "gus", "PATCH", fmt.Sprintf("/api/request-work/%d", w.ID), map[string]string{"state": "paused", "summary": "x"}); code != 404 || got != unknownWork {
		t.Errorf("foreign readable work end: %d %q", code, got)
	}
	// Die eigene Session geht weiter.
	f.expect(t, "gus", 201, "POST", path, map[string]any{"session_id": workSession(t, f, "ow-gus", 5, accProject), "role": "related"})
}

// primary_exists nennt keine REQ-Nummer aus einem Projekt, das der Betrachter
// nicht sehen darf.
func TestPrimaryExistsDoesNotNameHiddenRequest(t *testing.T) {
	f := accessAPI(t, true)
	hidden, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "h", Person: "robin", Scope: scope.Axes{Project: accOther}}})
	if err != nil {
		t.Fatal(err)
	}
	sid := workSession(t, f, "pe-nora", 6, accProject)
	if _, _, err := f.st.StartRequestWork(hidden.Request.ID, sid, "primary", "nora"); err != nil {
		t.Fatal(err)
	}
	g, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "g", Person: "robin"}})
	if err != nil {
		t.Fatal(err)
	}
	code, out := f.call(t, "nora", "POST", fmt.Sprintf("/api/requests/%d/work", g.Request.ID), map[string]any{"session_id": sid, "role": "primary"})
	if code != 409 {
		t.Fatalf("status %d %s", code, out)
	}
	if strings.Contains(out, "REQ-") {
		t.Errorf("hidden request named: %s", out)
	}
}

// Wer arbeiten darf, aber keine Nummern sieht, bekommt Aktivitäts-IDs immer neu
// vergeben; sie braucht kein Schreibzugriff.
func TestActivityIDsRenumberedForWorkersWithoutNumbers(t *testing.T) {
	f := accessAPI(t, true)
	for i := 0; i < 3; i++ {
		if _, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: fmt.Sprintf("pad%d", i), Person: "robin"}}); err != nil {
			t.Fatal(err)
		}
	}
	g, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "g", Person: "robin"}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := f.st.RequestByID(g.Request.ID)
	if err != nil || len(d.Activity) == 0 || d.Activity[0].ID <= 1 {
		t.Fatalf("setup: %v %+v", err, d.Activity)
	}
	out := f.expect(t, "nora", 200, "GET", fmt.Sprintf("/api/requests/%d", g.Request.ID), nil)
	if !strings.Contains(out, `"activity":[{"id":1,`) {
		t.Errorf("activity ids not renumbered: %s", out)
	}
}

// Ein Instanz-Admin startet und beendet auch mit fremden Sessions Arbeit an
// globalen Requests; ein Mitglied ohne Nummernsicht dort nur mit der eigenen.
func TestAdminStartsAndEndsForeignWorkOnGlobalRequest(t *testing.T) {
	f := accessAPI(t, true)
	g, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "g", Person: "robin"}})
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/requests/%d/work", g.Request.ID)
	foreign := workSession(t, f, "adm-mia", 3, accProject)
	if code, out := f.call(t, "rex", "POST", path, map[string]any{"session_id": foreign, "role": "related"}); code != 404 {
		t.Errorf("member foreign start: %d %s", code, out)
	}
	f.expect(t, "robin", 201, "POST", path, map[string]any{"session_id": foreign, "role": "related"})
	w, _, err := f.st.StartRequestWork(g.Request.ID, workSession(t, f, "adm-rex", 4, accProject), "related", "rex")
	if err != nil {
		t.Fatal(err)
	}
	wp := fmt.Sprintf("/api/request-work/%d", w.ID)
	if code, out := f.call(t, "mia", "PATCH", wp, map[string]string{"state": "paused", "summary": "x"}); code != 404 {
		t.Errorf("member foreign end: %d %s", code, out)
	}
	f.expect(t, "robin", 200, "PATCH", wp, map[string]string{"state": "paused", "summary": "x"})
}

// Ohne Warnung aus dem Store erscheint keine, auch wenn lesbare Arbeit existiert.
func TestStartWarningOnlyWhenStoreWarned(t *testing.T) {
	f := accessAPI(t, true)
	g, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "g", Person: "robin"}})
	if err != nil {
		t.Fatal(err)
	}
	shared := workSession(t, f, "sw-rex", 4, accProject)
	if err := f.st.SetSessionVisibility(shared, f.st.Access(store.Principal{ID: "person:4", Label: "rex"}), store.VisGuests); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.StartRequestWork(g.Request.ID, shared, "primary", "rex"); err != nil {
		t.Fatal(err)
	}
	out := f.expect(t, "gus", 201, "POST", fmt.Sprintf("/api/requests/%d/work", g.Request.ID), map[string]any{"session_id": workSession(t, f, "sw-gus", 5, accProject), "role": "related"})
	if strings.Contains(out, "already has") {
		t.Errorf("warning without store warning: %s", out)
	}
}
