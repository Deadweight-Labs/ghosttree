package store

import (
	"errors"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Eine Organisation des Instanz-Owners hat den Schalter an, die eines anderen
// Kontos aus; eine, die es vor dem Schalter gab, behält das heutige Verhalten.
func TestNewOrgsStartWithTheModelSwitchByWhoMadeThem(t *testing.T) {
	st := orgStore(t, "robin", "anna")
	mine := mustOrg(t, st, "person:1", "Mine", "mine")
	theirs := mustOrg(t, st, "person:2", "Theirs", "theirs")
	if !st.ExternalModelsFor(mine.ID) {
		t.Error("the org of the instance owner starts with the switch off")
	}
	if st.ExternalModelsFor(theirs.ID) {
		t.Error("the org of another account starts with the switch on")
	}
	// Eine Zeile aus der Zeit vor dem Schalter: die Spalte hat den Wert 1.
	if _, err := st.db.Exec(`INSERT INTO orgs(slug, name, created_at) VALUES('old','Old','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	var on int
	if err := st.db.QueryRow(`SELECT external_models FROM orgs WHERE slug='old'`).Scan(&on); err != nil || on != 1 {
		t.Fatalf("an existing org keeps today's behavior: %d %v", on, err)
	}
	// Die persönliche Namespace-Org folgt derselben Regel.
	ns, err := st.EnsureUserNamespace("person:2")
	if err != nil {
		t.Fatal(err)
	}
	if st.ExternalModelsFor(ns.ID) {
		t.Error("the personal namespace of another account starts with the switch on")
	}
}

func TestOnlyAnOwnerChangesTheModelSwitchAndItIsRecorded(t *testing.T) {
	st := orgStore(t, "robin", "anna")
	o := mustOrg(t, st, "person:2", "Theirs", "theirs")
	if err := st.SetExternalModels("person:1", o.ID, true); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("a stranger (even the instance owner) changed it: %v", err)
	}
	if err := st.SetExternalModels("person:2", o.ID, true); err != nil {
		t.Fatal(err)
	}
	if !st.ExternalModelsFor(o.ID) {
		t.Fatal("the owner could not switch it on")
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM org_events WHERE org_id=? AND action='external_models'`, o.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("events: %d %v", n, err)
	}
}

// Für ein Projekt einer Organisation mit ausgeschaltetem Schalter wird nichts
// erklärt, destilliert oder eingebettet.
func TestModelSwitchOffKeepsTextsFromExplainDistillAndEmbed(t *testing.T) {
	st := orgStore(t, "robin", "anna")
	theirs := mustOrg(t, st, "person:2", "Theirs", "theirs")
	const remote = "github.com/theirs/repo"
	if _, err := st.db.Exec(`INSERT INTO projects(remote, org_id, name, created_at) VALUES(?,?,?,?)`, remote, theirs.ID, "", now()); err != nil {
		t.Fatal(err)
	}
	id, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "t", Body: "b", Confidence: "trusted", Scope: scope.Axes{Project: remote}})
	if err != nil {
		t.Fatal(err)
	}
	src, err := st.ExplainSource("knowledge", id)
	if err != nil || !src.Withheld || src.Generable() {
		t.Fatalf("source of an object of an org that keeps its texts: %+v %v", src, err)
	}
	cands, _ := st.ExplainCandidates(50)
	for _, c := range cands {
		if c.Kind == "knowledge" && c.ID == id {
			t.Fatal("the object is a candidate for explanation")
		}
	}
	sid, err := st.UpsertSession(Session{Harness: "claude-code", ExternalID: "s1", Scope: scope.Axes{Project: remote}, AccountID: 2})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := st.SessionsPendingDistillation(scope.Axes{}, time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "v1", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pending {
		if p.ID == sid {
			t.Fatal("the session is pending for distillation")
		}
	}
	// Eingeschaltet: alles wie vorher.
	if err := st.SetExternalModels("person:2", theirs.ID, true); err != nil {
		t.Fatal(err)
	}
	if src, _ := st.ExplainSource("knowledge", id); src.Withheld {
		t.Fatal("still withheld after the owner switched it on")
	}
	pending, _ = st.SessionsPendingDistillation(scope.Axes{}, time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "v1", 10)
	found := false
	for _, p := range pending {
		found = found || p.ID == sid
	}
	if !found {
		t.Fatal("the session is not pending after the switch is on")
	}
}

func TestModelSwitchOffKeepsObjectsOutOfTheEmbeddingJob(t *testing.T) {
	st := orgStore(t, "robin", "anna")
	theirs := mustOrg(t, st, "person:2", "Theirs", "theirs")
	const remote = "github.com/theirs/repo"
	if _, err := st.db.Exec(`INSERT INTO projects(remote, org_id, name, created_at) VALUES(?,?,?,?)`, remote, theirs.ID, "", now()); err != nil {
		t.Fatal(err)
	}
	id, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "t", Body: "b", Confidence: "trusted", Scope: scope.Axes{Project: remote}})
	if err != nil {
		t.Fatal(err)
	}
	m := EmbedModel{Model: "m", PrefixVersion: "p"}
	has := func() bool {
		due, err := st.EmbeddingDue(KindKnowledge, m, 20, time.Now(), true)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range due.Items {
			if it.ID == id {
				return true
			}
		}
		return false
	}
	if has() {
		t.Fatal("an object of an org that keeps its texts is due for embedding")
	}
	if err := st.SetExternalModels("person:2", theirs.ID, true); err != nil {
		t.Fatal(err)
	}
	if !has() {
		t.Fatal("the object is not due after the switch is on")
	}
}
