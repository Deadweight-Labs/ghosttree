package store

import (
	"errors"
	"testing"
)

// Eine gespeicherte Suche darf keine mitgelieferte verdecken, und die Suche eines
// anderen überschreibt nur, wer sie auch löschen dürfte.
func TestSavedSearchCannotTakeABuiltinName(t *testing.T) {
	w := newPainWorld(t)
	for _, scopeKind := range []string{"user", "project", "org"} {
		pa := w.pa["robin"]
		for _, name := range []string{"pr-ergebnis", "fuer-mich", "ordner"} {
			if _, err := pa.SavePreset(roleProject, scopeKind, name, "", []string{"is:open"}); ruleCode(err) != "preset_invalid" {
				t.Errorf("%s search named %s: %v, want preset_invalid", scopeKind, name, err)
			}
		}
	}
}

func TestSavedSearchOfAnotherIsOverwrittenOnlyByWhoMayDeleteIt(t *testing.T) {
	w := newPainWorld(t)
	first, err := w.pa["mia"].SavePreset(roleProject, "project", "team-sicht", "", []string{"is:open"})
	if err != nil {
		t.Fatal(err)
	}
	// Ein anderes Mitglied darf sie nicht überschreiben, und sie bleibt, wie sie war.
	if _, err := w.pa["rex"].SavePreset(roleProject, "project", "team-sicht", "", []string{"is:done"}); !errors.Is(err, ErrPresetConflict) {
		t.Fatalf("member overwrites a project search of another: %v", err)
	}
	if got, _ := w.st.PresetByID(first.ID); got.Queries[0] != "is:open" {
		t.Fatalf("the search changed: %+v", got)
	}
	// Der Anleger darf seine ändern.
	if _, err := w.pa["mia"].SavePreset(roleProject, "project", "team-sicht", "", []string{"is:closed"}); err != nil {
		t.Fatalf("the author overwrites: %v", err)
	}
	// Ein Lead darf sie ersetzen; der Anleger bleibt, der Ersetzende steht in updated_by.
	if _, err := w.pa["lena"].SavePreset(roleProject, "project", "team-sicht", "", []string{"is:done"}); err != nil {
		t.Fatalf("the lead overwrites: %v", err)
	}
	got, _ := w.st.PresetByID(first.ID)
	if got.Queries[0] != "is:done" || got.CreatedBy != "mia" || got.UpdatedBy != "lena" {
		t.Fatalf("after the lead: %+v", got)
	}
	// Persönliche Suchen mit gleichem Namen sind eigene Zeilen: kein Konflikt.
	if _, err := w.pa["rex"].SavePreset(roleProject, "user", "team-sicht", "", []string{"is:done"}); err != nil {
		t.Fatalf("a personal search of the same name: %v", err)
	}
	// Die Org-Suche eines Owners ersetzt nur ein Owner der Org; ein Mitglied darf gar nicht speichern.
	if _, err := w.pa["robin"].SavePreset(roleProject, "org", "org-sicht", "", []string{"is:open"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pa["mia"].SavePreset(roleProject, "org", "org-sicht", "", []string{"is:done"}); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("member overwrites an org search: %v", err)
	}
}
