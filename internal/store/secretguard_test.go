package store

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// An invented value behind a label; no value in these tests is real.
const leakedValue = "Hx7!kQ2pzWm"

func withSecret(topic string) string { return topic + ": Mail Passwort: " + leakedValue + " gilt" }

func TestRefuseSecretsNamesPlaceAndLabelNeverTheValue(t *testing.T) {
	if err := refuseSecrets("title", "Mail-Passwort liegt im Vault", "body", ""); err != nil {
		t.Fatalf("harmless text: %v", err)
	}
	err := refuseSecrets("title", "ok", "body", "erste Zeile\nMail Passwort: "+leakedValue)
	if ruleCode(err) != "secret_detected" {
		t.Fatalf("code = %q (%v)", ruleCode(err), err)
	}
	msg := err.Error()
	for _, want := range []string{"nothing was stored", "body, line 2, column 6", `after the label "Passwort"`, "Do not repeat the value", "(labeled)"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, leakedValue) {
		t.Errorf("message repeats the value: %s", msg)
	}
	var rule *requestdomain.RuleError
	if e, ok := err.(*requestdomain.RuleError); ok {
		rule = e
	}
	if rule == nil || !strings.Contains(rule.Resolution, "Do not repeat the value") {
		t.Errorf("resolution: %+v", rule)
	}

	// Several passages are counted, the first few named.
	var many []string
	for i := 0; i < 7; i++ {
		many = append(many, indexed("criteria", i), "Passwort: Hx7!kQ2pz"+string(rune('a'+i)))
	}
	msg = refuseSecrets(many...).Error()
	if !strings.Contains(msg, "7 possible secrets") || !strings.Contains(msg, "criteria[1]") || !strings.Contains(msg, "criteria[5]") ||
		strings.Contains(msg, "criteria[6]") || !strings.Contains(msg, "2 more") {
		t.Errorf("several: %s", msg)
	}
}

func TestKnowledgeWithASecretIsNotStoredAndAnEditChecksOnlyWhatChanged(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	count := func() (n int) {
		_ = st.db.QueryRow(`SELECT COUNT(*) FROM knowledge`).Scan(&n)
		return n
	}
	if _, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "Zugang", Body: withSecret("Mail")}); ruleCode(err) != "secret_detected" || count() != 0 {
		t.Fatalf("insert with a secret: %v, %d rows", err, count())
	}
	id, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "Zugang", Body: "liegt im Vault"})
	if err != nil {
		t.Fatal(err)
	}
	// An entry written before the check existed keeps a secret in its body.
	old := withSecret("alt")
	if _, err := st.db.Exec(`UPDATE knowledge SET body=? WHERE id=?`, old, id); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateKnowledge(id, map[string]string{"title": "Neuer Titel", "body": old}); err != nil {
		t.Errorf("the unchanged body is not looked at: %v", err)
	}
	if err := st.UpdateKnowledge(id, map[string]string{"body": withSecret("neu")}); ruleCode(err) != "secret_detected" {
		t.Errorf("a changed body is: %v", err)
	}
	if err := st.UpdateKnowledge(id, map[string]string{"title": withSecret("Titel")}); ruleCode(err) != "secret_detected" {
		t.Errorf("a changed title is: %v", err)
	}
	if k, _ := st.KnowledgeByID(id); k.Title != "Neuer Titel" || k.Body != old {
		t.Errorf("a refused edit changed the entry: %q / %q", k.Title, k.Body)
	}
	if err := st.UpdateKnowledge(id, map[string]string{"body": "jetzt sauber"}); err != nil {
		t.Errorf("clean text over the old secret: %v", err)
	}
	// Reasons are free text too.
	other, _ := st.InsertKnowledge(Knowledge{Type: "note", Title: "Zweiter", Body: "x"})
	if err := st.RetireKnowledge(other, withSecret("Grund"), "robin"); ruleCode(err) != "secret_detected" {
		t.Errorf("retire reason: %v", err)
	}
	if _, err := st.InsertMigrated(MigratedEntry{Knowledge: Knowledge{Type: "note", Title: "Aus Datei", Body: withSecret("Datei")}, ItemKey: "k1"}); ruleCode(err) != "secret_detected" {
		t.Errorf("migrated entry: %v", err)
	}
}

// A decision closes into knowledge, an idea gets a concept, a pain a correction:
// the texts that reach the store through ProjectAccess are checked as well.
func TestProjectAccessWritesRefuseASecret(t *testing.T) {
	w := newPainWorld(t)
	mia := w.pa["mia"]
	i := w.workshopIdea(t, "Zugang")
	d := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Wie melden wir uns an", w.agent("mia"))
	if _, err := mia.CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDecided, Answer: "per Link", Reason: withSecret("Grund"), Actor: w.agent("mia")}); ruleCode(err) != "secret_detected" {
		t.Errorf("decision reason: %v", err)
	}
	if got, err := mia.CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDecided, Answer: "per Link", Reason: "das Passwort entfällt", Actor: w.agent("mia")}); err != nil || got.State != idea.DecisionDecided {
		t.Errorf("harmless decision: %+v %v", got, err)
	}
	if _, _, err := mia.PutIdeaConcept(i.ID, PutIdeaConceptInput{Patch: idea.ConceptPatch{How: ptr(withSecret("Wie"))}, Actor: w.agent("mia")}); ruleCode(err) != "secret_detected" {
		t.Errorf("concept: %v", err)
	}
	if _, _, err := mia.PutIdeaConcept(i.ID, PutIdeaConceptInput{Patch: idea.ConceptPatch{How: ptr("per Link, ohne Passwort")}, Actor: w.agent("mia")}); err != nil {
		t.Errorf("harmless concept: %v", err)
	}
	created, err := mia.CreatePain(CreatePainInput{Project: roleProject, Title: "Zugang", Body: "Das Passwort wird nirgends gespeichert", Actor: pain.Actor{Person: "mia"}})
	if err != nil {
		t.Fatalf("harmless pain: %v", err)
	}
	if _, err := mia.CreatePain(CreatePainInput{Project: roleProject, Title: "Zugang", Body: withSecret("Pain"), Actor: pain.Actor{Person: "mia"}}); ruleCode(err) != "secret_detected" {
		t.Errorf("pain: %v", err)
	}
	// A pain written before the check keeps its old text when only the title is corrected.
	old := withSecret("alt")
	if _, err := w.st.db.Exec(`UPDATE pains SET body=? WHERE id=?`, old, created.Pain.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := mia.CorrectPain(created.Pain.ID, ptr("Neuer Titel"), nil, "klarer", pain.Actor{Person: "mia"}); err != nil {
		t.Errorf("correcting the title over an old body: %v", err)
	}
	if _, err := mia.CorrectPain(created.Pain.ID, nil, ptr(withSecret("neu")), "mehr", pain.Actor{Person: "mia"}); ruleCode(err) != "secret_detected" {
		t.Errorf("a new body: %v", err)
	}
}
