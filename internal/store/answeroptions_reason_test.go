package store

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
)

// Die Begründung einer Schnellantwort auf eine Entscheidung steht so im Wissen, wie
// der Mensch sie schrieb; ohne sie bleibt der Standardtext, zu lang wird abgewiesen.
func TestQuickAnswerOnADecisionKeepsTheReasonOfThePerson(t *testing.T) {
	w := newPainWorld(t)
	i := w.workshopIdea(t, "Idee")
	open := func(q string) idea.Decision {
		return w.openOn(t, "mia", idea.SubjectIdea, i.ID, q, w.actor("mia"), withAspects(idea.AspectCost),
			withVariants(idea.VariantInput{Title: "Plan A"}, idea.VariantInput{Title: "Plan B"}))
	}
	d := open("Welche Richtung?")
	long := strings.Repeat("ä", idea.MaxReasonLine+1)
	if _, err := w.web("lena").QuickAnswer(CoordAccess{}, QuickAnswerInput{Kind: AnswerOnDecision, ID: d.ID, Option: "A", Reason: long}); ruleCode(err) != "reason_too_long" {
		t.Fatalf("a reason that is too long: %v", err)
	}
	if got, _ := w.pa["lena"].DecisionView(d.ID); got.State != idea.DecisionOpen {
		t.Fatalf("a refused reason closed the decision: %+v", got)
	}
	exact := strings.Repeat("ä", idea.MaxReasonLine)
	if _, err := w.web("lena").QuickAnswer(CoordAccess{}, QuickAnswerInput{Kind: AnswerOnDecision, ID: d.ID, Option: "A", Reason: "  " + exact + "\n"}); err != nil {
		t.Fatalf("a reason of the longest size: %v", err)
	}
	if got, _ := w.pa["lena"].DecisionView(d.ID); got.Reason != exact || got.ChosenVariant != "A" {
		t.Fatalf("reason in the decision: %q", got.Reason)
	}
	d2 := open("Noch eine?")
	if _, err := w.web("lena").QuickAnswer(CoordAccess{}, QuickAnswerInput{Kind: AnswerOnDecision, ID: d2.ID, Option: "B", Reason: "Weil\n\tdie Kosten‮ zählen"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.pa["lena"].DecisionView(d2.ID); got.Reason != "Weil die Kosten zählen" {
		t.Fatalf("the reason is kept as one clean line: %q", got.Reason)
	}
}

func TestCanCloseDecisionIsTheRightToEditItsSubject(t *testing.T) {
	w := newPainWorld(t)
	i := w.workshopIdea(t, "Idee")
	d := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Welche Richtung?", w.actor("mia"), withAspects(idea.AspectCost),
		withVariants(idea.VariantInput{Title: "Plan A"}, idea.VariantInput{Title: "Plan B"}))
	if !w.pa["lena"].CanCloseDecision(d) {
		t.Error("the person who may close a decision is not offered it")
	}
	for _, who := range []string{"gus", "nora"} {
		if w.pa[who].CanCloseDecision(d) {
			t.Errorf("%s may close a decision they cannot edit", who)
		}
	}
}
