package idea

import (
	"strings"
	"testing"
)

func TestConceptHasSixFixedSectionsAndPatchesKeepTheRest(t *testing.T) {
	about, how := "Worum es geht", "So"
	c, err := Concept{}.Apply(ConceptPatch{About: &about, How: &how})
	if err != nil || c.About != about || c.How != how || c.What != "" || c.Requirements == nil {
		t.Fatalf("%+v %v", c, err)
	}
	what := "Das"
	reqs := []string{"Läuft ohne Rückfrage", "läuft ohne rückfrage", "  ", "Zweites Kriterium"}
	c2, err := c.Apply(ConceptPatch{What: &what, Requirements: &reqs})
	if err != nil || c2.About != about || c2.What != what || len(c2.Requirements) != 2 {
		t.Fatalf("patch keeps the other sections and drops duplicates: %+v %v", c2, err)
	}
	if len(SectionNames) != 6 {
		t.Fatalf("sections: %v", SectionNames)
	}
}

func TestConceptRefusesWhatCannotGoIntoABriefing(t *testing.T) {
	for _, bad := range [][]string{{"zwei\nZeilen"}, {"a‮b"}, {strings.Repeat("x", MaxRequirement+1)}} {
		if _, err := CleanRequirements(bad); err == nil {
			t.Errorf("requirement %q accepted", bad)
		}
	}
	many := make([]string, MaxRequirements+1)
	for i := range many {
		many[i] = strings.Repeat("r", i+1)
	}
	if _, err := CleanRequirements(many); code(err) != "requirements_too_many" {
		t.Fatalf("too many: %v", err)
	}
	if _, err := CleanSection("a\x00b", "about"); err == nil {
		t.Error("NUL in a section")
	}
	if _, err := CleanSection("a​b", "about"); err == nil {
		t.Error("zero-width space in a section")
	}
	if s, err := CleanSection("Zeile 1\r\nZeile 2\n\tEingerückt", "about"); err != nil || strings.Contains(s, "\r") {
		t.Fatalf("multi-line text: %q %v", s, err)
	}
	if _, err := CleanSection(strings.Repeat("x", MaxSummary+1), "summary"); err == nil {
		t.Error("summary limit")
	}
}

func TestRawIsKeptByteForByte(t *testing.T) {
	if err := CleanRaw("  Transkript\r\nmit\ttab \n"); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]string{"empty": " \n", "nul": "a\x00b", "latin1": "caf\xe9", "huge": strings.Repeat("x", MaxRawBytes+1)} {
		if CleanRaw(bad) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestPhaseTransitions(t *testing.T) {
	type c struct {
		from, to                      string
		concept, requests, openIssues bool
		want                          string
	}
	for _, tc := range []c{
		{PhaseRaw, PhaseConcept, true, false, false, ""},
		{PhaseRaw, PhaseConcept, false, false, false, "concept_required"},
		{PhaseConcept, PhaseDiscussion, true, false, false, ""},
		{PhaseDiscussion, PhaseApproved, true, false, false, ""},
		{PhaseRaw, PhaseApproved, false, false, false, "concept_required"},
		{PhaseApproved, PhaseDiscussion, true, false, false, ""},
		{PhaseApproved, PhaseBuilding, true, true, true, ""},
		{PhaseConcept, PhaseBuilding, true, false, false, "not_approved"},
		{PhaseRaw, PhaseBuilding, true, true, false, "not_approved"},
		{PhaseDiscussion, PhaseBuilding, true, true, false, "not_approved"},
		{PhaseApproved, PhaseBuilding, true, false, false, "requests_required"},
		{PhaseBuilding, PhaseDone, true, true, true, "requests_open"},
		{PhaseBuilding, PhaseDone, true, true, false, ""},
		{PhaseBuilding, PhaseDone, true, false, false, "requests_required"},
		{PhaseBuilding, PhaseApproved, true, true, false, "invalid_phase"},
		{PhaseDone, PhaseConcept, true, true, false, "invalid_phase"},
		{PhaseApproved, PhaseDone, true, false, false, "invalid_phase"},
		{PhaseRaw, PhaseRaw, true, false, false, "invalid_phase"},
		{PhaseRaw, PhaseDismissed, true, false, false, "invalid_phase"},
		{PhaseRaw, "bogus", true, false, false, "invalid_phase"},
	} {
		err := ValidatePhase(tc.from, tc.to, tc.concept, tc.requests, tc.openIssues)
		if code(err) != tc.want {
			t.Errorf("%s -> %s: %v, want %q", tc.from, tc.to, err, tc.want)
		}
	}
	if EffectivePhase(Dismissed, PhaseApproved) != PhaseDismissed || EffectivePhase(Open, "") != PhaseRaw || EffectivePhase(Accepted, PhaseBuilding) != PhaseBuilding {
		t.Fatal("effective phase")
	}
}

func TestLooksLargeOnlyDecidesWhetherToSuggest(t *testing.T) {
	if LooksLarge("Dunkles Thema", "ein Satz") {
		t.Error("a small wish looks large")
	}
	for _, title := range []string{"OAuth-Anmeldung planen", "Anbindung an Granola", "Migration der Datenbank"} {
		if !LooksLarge(title, "") {
			t.Errorf("%q should look large", title)
		}
	}
	if !LooksLarge("x", strings.Repeat("lang ", 100)) {
		t.Error("a long text looks large")
	}
}
