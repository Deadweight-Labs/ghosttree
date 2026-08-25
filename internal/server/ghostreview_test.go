package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestGhostReviewRoundTrip(t *testing.T) {
	srv, token := newTestServer(t)

	res := req(t, "POST", srv.URL+"/api/ghosts/reviews", token,
		store.GhostReview{Project: "p", Path: "internal/store/foo.go", GitBlob: "blob1"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST status %d", res.StatusCode)
	}

	res = req(t, "GET", srv.URL+"/api/ghosts/reviews?project=p&prefix=internal", token, nil)
	defer res.Body.Close()
	var got []store.GhostReview
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].GitBlob != "blob1" {
		t.Fatalf("want one review at blob1, got %+v", got)
	}
	// Wer etwas behauptet, steht am Eintrag — abgeleitet aus dem Token, nicht
	// aus den Nutzdaten, wie bei putGhost.
	if got[0].Person != "test" {
		t.Errorf("person should come from the token, got %q", got[0].Person)
	}
}

func TestGhostReviewRejectsMissingProjectOrBlob(t *testing.T) {
	srv, token := newTestServer(t)
	for _, in := range []store.GhostReview{
		// Ohne Projekt landet das Review in einem Scope, den niemand liest, und
		// der Pfad bleibt für immer Kandidat, ohne dass es auffällt.
		{Path: "a.go", GitBlob: "b"},
		// Ohne Blob gilt die Entscheidung keiner Fassung und damit allen —
		// genau die Entscheidung für immer, die der Entwurf vermeidet.
		{Project: "p", Path: "a.go"},
	} {
		res := req(t, "POST", srv.URL+"/api/ghosts/reviews", token, in)
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("want 400 for %+v, got %d", in, res.StatusCode)
		}
	}
}

func TestGhostReviewsRequiresProject(t *testing.T) {
	srv, token := newTestServer(t)
	res := req(t, "GET", srv.URL+"/api/ghosts/reviews", token, nil)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("want 400 without project, got %d", res.StatusCode)
	}
}

// Eine leere Antwort muss [] sein und nicht null: der Aufrufer iteriert
// darüber, und null liest sich in JSON wie ein Fehler statt wie "keine".
func TestGhostReviewsReturnsEmptyArrayNotNull(t *testing.T) {
	srv, token := newTestServer(t)
	res := req(t, "GET", srv.URL+"/api/ghosts/reviews?project=nothing", token, nil)
	defer res.Body.Close()
	var raw json.RawMessage
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if string(raw) != "[]" {
		t.Errorf("want [], got %s", raw)
	}
}
