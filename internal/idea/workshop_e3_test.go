package idea

import "testing"

// DEC-46: "discussed" sits between discussion and approval, needs a concept,
// and goes back to discussion freely, but not once issues came out of the idea.
func TestTheClosedDiscussionIsAPhaseWithAConcept(t *testing.T) {
	if err := ValidatePhase(PhaseDiscussion, PhaseDiscussed, false, false, false); err == nil {
		t.Fatal("closed without a concept")
	}
	for _, from := range []string{PhaseRaw, PhaseConcept, PhaseDiscussion} {
		if err := ValidatePhase(from, PhaseDiscussed, true, false, false); err != nil {
			t.Fatalf("%s → discussed: %v", from, err)
		}
	}
	for _, to := range []string{PhaseDiscussion, PhaseApproved} {
		if err := ValidatePhase(PhaseDiscussed, to, true, false, false); err != nil {
			t.Fatalf("discussed → %s: %v", to, err)
		}
	}
	if err := ValidatePhase(PhaseApproved, PhaseDiscussed, true, true, true); err == nil {
		t.Fatal("back to discussed after issues came out")
	}
	if _, err := CleanSaid("  "); err == nil {
		t.Fatal("no words")
	}
	if s, err := CleanSaid(" ja, mach zu "); err != nil || s != "ja, mach zu" {
		t.Fatalf("%q %v", s, err)
	}
}
