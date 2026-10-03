package server

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Arbeit an einem Projekt-Auftrag, deren Session mia gehört: Gast, Fremder und
// eine unbekannte Id bekommen dieselbe Antwort (#2485), Status und Body.
func TestFinishProjectWorkOfHiddenSessionLooksLikeUnknownId(t *testing.T) {
	f := accessAPI(t, true)
	sid, err := f.st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "p-mia", AccountID: 3, Scope: scope.Axes{Project: accProject}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "p", Person: "mia", Scope: scope.Axes{Project: accProject}}})
	if err != nil {
		t.Fatal(err)
	}
	w, _, err := f.st.StartRequestWork(d.Request.ID, sid, "primary", "mia")
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]string{"state": "paused", "summary": "x"}
	path := func(id int64) string { return fmt.Sprintf("/api/request-work/%d", id) }
	code, unknown := f.call(t, "nora", "PATCH", path(99999), body)
	if code != 404 {
		t.Fatalf("unknown id: %d %s", code, unknown)
	}
	for _, who := range []string{"gus", "nora"} {
		if c, got := f.call(t, who, "PATCH", path(w.ID), body); c != 404 || got != unknown {
			t.Errorf("%s: %d %q, want 404 %q", who, c, got, unknown)
		}
		if c, got := f.call(t, who, "PATCH", path(99999), body); c != 404 || got != unknown {
			t.Errorf("%s unknown: %d %q", who, c, got)
		}
	}
	f.expect(t, "mia", 200, "PATCH", path(w.ID), body)
}

// Wissen anlegen: eine numerische session:<n> des Aufrufers wird verworfen, die
// Antwort hängt nicht davon ab, ob N ihm gehört, verborgen ist oder fehlt.
func TestCreateKnowledgeDropsNumericSessionRefForNonMembers(t *testing.T) {
	f := accessAPI(t, true)
	mk := func(ext string, account int64) int64 {
		id, err := f.st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: ext, AccountID: account, Scope: scope.Axes{Project: accProject}})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	own, hidden := mk("k-nora", 6), mk("k-mia", 3)
	norm := regexp.MustCompile(`"(id|created_at|updated_at|observed_at)":("[^"]*"|\d+)`)
	var bodies []string
	for i, n := range []int64{own, hidden, 99999} {
		out := f.expect(t, "nora", 200, "POST", "/api/knowledge", map[string]any{
			"type": "pitfall", "title": "t", "body": "b", "session_ref": fmt.Sprintf("session:%d#0", n),
		})
		if regexp.MustCompile(`session:\d`).MatchString(out) || strings.Contains(out, "session_ref") {
			t.Errorf("case %d: session ref echoed: %s", i, out)
		}
		bodies = append(bodies, norm.ReplaceAllString(out, `"$1":""`))
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Errorf("response depends on the session: %v", bodies)
	}
	// Mitglieder behalten ihre Angabe.
	out := f.expect(t, "mia", 200, "POST", "/api/knowledge", map[string]any{
		"type": "pitfall", "title": "m", "body": "b", "session_ref": fmt.Sprintf("session:%d#0", hidden),
		"scope": map[string]string{"project": accProject},
	})
	if !strings.Contains(out, fmt.Sprintf("session:%d#0", hidden)) {
		t.Errorf("member lost the session ref: %s", out)
	}
}

// Arbeit starten: die Session muss dem Aufrufer gehören oder für ihn lesbar
// sein. Verborgen und nicht vorhanden antworten gleich.
func TestStartWorkNeedsReadableSession(t *testing.T) {
	f := accessAPI(t, true)
	mk := func(ext string, account int64) int64 {
		id, err := f.st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: ext, AccountID: account, Scope: scope.Axes{Project: accProject}})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	own, hidden := mk("s-nora", 6), mk("s-mia", 3)
	g, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "g", Person: "robin"}})
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/requests/%d/work", g.Request.ID)
	start := func(sid int64) (int, string) {
		return f.call(t, "nora", "POST", path, map[string]any{"session_id": sid, "role": "related"})
	}
	c1, b1 := start(hidden)
	c2, b2 := start(99999)
	if c1 != c2 || b1 != b2 || c1 != 404 {
		t.Errorf("hidden %d %q vs missing %d %q", c1, b1, c2, b2)
	}
	if c, b := start(own); c != 201 {
		t.Errorf("own session: %d %s", c, b)
	}
}

// Die Prüfliste über HTTP: ein Gast sieht keine Nummer; wer Prüfer ist, die
// echte Nummer. Der Weg läuft über die Route, nicht nur über die Sicht.
func TestPendingKnowledgeOverHTTPHidesSessionNumbers(t *testing.T) {
	f := oracleFixture(t, 2)
	shared := f.id["o-shared"]
	id, err := f.st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "wartend", Body: "b", Scope: scope.Axes{Project: accProject}, Person: "rex", Confidence: "quarantined", Origin: "distilled", SessionRef: fmt.Sprintf("session:%d#1", shared)})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.AddEvidence(id, []store.Evidence{{SessionID: shared, ChunkSeq: 0, Quote: "SICHTBAR"}, {SessionID: shared + 1, ChunkSeq: 0, Quote: "GEHEIMES-ZITAT"}}); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"gus", "nora", "mia"} {
		code, out := f.call(t, who, "GET", "/api/knowledge/pending?project="+accProject, nil)
		if who != "nora" && code != 200 {
			t.Errorf("%s: pending list = %d", who, code)
		}
		if code != 200 {
			continue // diese Rolle sieht die Liste gar nicht
		}
		if who != "mia" && regexp.MustCompile(`"session_id":[1-9]|session:\d`).MatchString(out) {
			t.Errorf("%s sees a session number: %s", who, out)
		}
		if who != "mia" && strings.Contains(out, "GEHEIMES-ZITAT") {
			t.Errorf("%s sees hidden evidence: %s", who, out)
		}
	}
}
