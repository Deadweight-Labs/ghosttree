package server

import (
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
