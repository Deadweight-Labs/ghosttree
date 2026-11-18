package server

import (
	"strings"
	"testing"
)

// Eine Freigabe (verified) gilt für den Text, den ein Prüfer gesehen hat: wer
// ihn ändert, ohne der Bestätiger zu sein, setzt den Eintrag auf staged zurück.
func TestEditingVerifiedKnowledgeNeedsANewReview(t *testing.T) {
	f := accessAPI(t, true)
	id := f.id["k-mia"]
	verify := map[string]string{"confidence": "verified"}
	state := func() (string, string) {
		k, err := f.st.KnowledgeByID(id)
		if err != nil {
			t.Fatal(err)
		}
		return k.Confidence, k.ConfirmedBy
	}
	// rex (can_review) bestätigt; die Autorin ändert danach ihren Text.
	f.expect(t, "rex", 204, "PATCH", idPath("/api/knowledge/%d", id), verify)
	f.expect(t, "mia", 204, "PATCH", idPath("/api/knowledge/%d", id), map[string]string{"body": "a different text nobody reviewed"})
	if conf, by := state(); conf != "staged" || by != "" {
		t.Errorf("author edit of a verified entry: confidence=%q confirmed_by=%q, want staged and empty", conf, by)
	}
	// Ein Lead bestätigt und ändert den Text selbst: die Freigabe bleibt.
	f.expect(t, "lena", 204, "PATCH", idPath("/api/knowledge/%d", id), verify)
	f.expect(t, "lena", 204, "PATCH", idPath("/api/knowledge/%d", id), map[string]string{"body": "the confirmer's own fix"})
	if conf, by := state(); conf != "verified" || by != "lena" {
		t.Errorf("edit by the confirmer: confidence=%q confirmed_by=%q, want verified by lena", conf, by)
	}
	// Ein anderer Lead ändert den Text: neue Prüfung.
	f.expect(t, "robin", 204, "PATCH", idPath("/api/knowledge/%d", id), map[string]string{"title": "retitled by somebody else"})
	if conf, by := state(); conf != "staged" || by != "" {
		t.Errorf("edit by another lead: confidence=%q confirmed_by=%q, want staged and empty", conf, by)
	}
}

// PATCH ändert Text, Typ und Beurteilung; Herkunft und Ersetzung setzt der
// Server, ersetzt wird über /supersede.
func TestKnowledgePatchDoesNotSetOriginSupersessionOrStatus(t *testing.T) {
	f := accessAPI(t, true)
	id, other := f.id["k-mia"], f.id["k-robin-staged"]
	for _, patch := range []map[string]string{
		{"origin": "human"},
		{"origin": "human", "body": "x"},
		{"superseded_by": itoa64(other)},
		{"status": "active"},
	} {
		f.expect(t, "mia", 400, "PATCH", idPath("/api/knowledge/%d", id), patch)
	}
	k, _ := f.st.KnowledgeByID(id)
	if k.Origin == "human" || k.SupersededBy != 0 || k.Status != "active" {
		t.Errorf("a refused patch changed the entry: %+v", k)
	}
	// Zurückziehen als deprecated bleibt dem Autor.
	f.expect(t, "mia", 204, "PATCH", idPath("/api/knowledge/%d", id), map[string]string{"status": "deprecated"})
}

func itoa64(n int64) string { return idPath("%d", n) }

// Das Ziel einer Relation ist ein Auftrag oder Wissen desselben Projekts, das
// der Aufrufer lesen darf; fehlend, fremd und verborgen antworten gleich.
func TestRequestRelationChecksItsTarget(t *testing.T) {
	f := accessAPI(t, true)
	own := f.id["r-mia"]
	path := idPath("/api/requests/%d/relations", own)
	f.expect(t, "mia", 201, "POST", path, map[string]any{"kind": "related", "other_request_id": f.id["r-robin"]})
	f.expect(t, "mia", 201, "POST", path, map[string]any{"kind": "knowledge", "knowledge_id": f.id["k-mia"]})
	f.expect(t, "mia", 201, "POST", path, map[string]any{"kind": "external", "external_ref": "https://example.test/x"})
	// What is not there, what lies in another project and what the caller may not
	// read all answer the same.
	for name, body := range map[string]map[string]any{
		"unknown request":              {"kind": "related", "other_request_id": 99999},
		"request of another project":   {"kind": "related", "other_request_id": f.id["r-other"]},
		"unknown knowledge":            {"kind": "knowledge", "knowledge_id": 99999},
		"knowledge of another project": {"kind": "knowledge", "knowledge_id": f.id["k-other"]},
		"global knowledge of another":  {"kind": "knowledge", "knowledge_id": f.id["k-global"]},
	} {
		if out := f.expect(t, "mia", 404, "POST", path, body); !strings.Contains(out, "origin_not_found") {
			t.Errorf("%s: %s", name, out)
		}
	}
	if detail, err := f.st.RequestByID(own); err != nil || len(detail.Relations) != 3 {
		t.Errorf("relations kept: %d %v", len(detail.Relations), err)
	}
}

// "Gibt es nicht" und "darfst du nicht sehen" antworten Byte für Byte gleich,
// damit fortlaufende Nummern von Wissen und Dokumenten nichts verraten.
func TestUnknownAndHiddenObjectsAnswerAlike(t *testing.T) {
	f := accessAPI(t, true)
	body := func(who, method, path string, payload any) string {
		code, out := f.call(t, who, method, path, payload)
		if code != 404 {
			t.Errorf("%s %s %s = %d, want 404", who, method, path, code)
		}
		return out
	}
	for _, c := range []struct {
		method, format string
		hidden         int64
		payload        any
	}{
		{"GET", "/api/knowledge/%d", f.id["k-mia"], nil},
		{"GET", "/api/knowledge/%d/history", f.id["k-mia"], nil},
		{"PATCH", "/api/knowledge/%d", f.id["k-mia"], map[string]string{"body": "x"}},
		{"GET", "/api/documents/%d", f.id["d-mia"], nil},
		{"PATCH", "/api/documents/%d", f.id["d-mia"], map[string]string{"title": "x"}},
		{"GET", "/api/documents/%d/revisions", f.id["d-mia"], nil},
	} {
		if hidden, unknown := body("nora", c.method, idPath(c.format, c.hidden), c.payload), body("nora", c.method, idPath(c.format, 99999), c.payload); hidden != unknown {
			t.Errorf("%s %s: hidden answers %q, unknown %q", c.method, c.format, hidden, unknown)
		}
	}
}
