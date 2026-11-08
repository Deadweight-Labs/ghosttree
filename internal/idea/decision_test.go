package idea

import (
	"strings"
	"testing"
)

// Die Regel, wer entscheidet: ohne Aspekt der Agent, mit jedem einzelnen ein Mensch.
func TestDecideWhoRule(t *testing.T) {
	if way, why := DecideWho(nil); way != WayAgent || len(why) != 1 {
		t.Fatalf("reversible, local, conventional: %s %v", way, why)
	}
	for _, aspect := range Aspects() {
		way, why := DecideWho([]string{aspect})
		if way != WayHuman || len(why) != 1 {
			t.Errorf("%s: %s %v", aspect, way, why)
		}
	}
	// Die Pflichtfälle der Regel stehen als Aspekte da.
	want := []string{AspectIrreversible, AspectSecurity, AspectCost, AspectExternal, AspectDataModel, AspectProduct, AspectPeople, AspectNonLocal, AspectConvention}
	got := Aspects()
	if len(got) != len(want) {
		t.Fatalf("aspects %v", got)
	}
	if way, why := DecideWho([]string{AspectCost, AspectSecurity}); way != WayHuman || len(why) != 2 {
		t.Fatalf("two reasons: %s %v", way, why)
	}
}

func TestUnknownAspectIsAnErrorNotASilentPass(t *testing.T) {
	if _, err := NormalizeAspects([]string{"secuirty"}); code(err) != "aspect_unknown" {
		t.Fatalf("typo: %v", err)
	}
	got, err := NormalizeAspects([]string{" Security ", "cost", "security", ""})
	if err != nil || strings.Join(got, ",") != "cost,security" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestChooseWay(t *testing.T) {
	type c struct {
		name      string
		requested string
		aspects   []string
		policy    string
		agent     bool
		want      string
		err       string
	}
	for _, tc := range []c{
		{"agent, reversible, nothing set", "", nil, PolicyRule, true, WayAgent, ""},
		{"agent, security", "", []string{AspectSecurity}, PolicyRule, true, WayHuman, ""},
		{"agent may not take a human decision", WayAgent, []string{AspectCost}, PolicyRule, true, "", "way_not_allowed"},
		{"agent may hand it to a person", WayHuman, nil, PolicyRule, true, WayHuman, ""},
		{"person asks, decides themselves", "", nil, PolicyRule, false, WayHuman, ""},
		{"person lets an agent decide", WayAgent, []string{AspectCost}, PolicyRule, false, WayAgent, ""},
		{"project keeps everything with people", "", nil, PolicyHuman, true, WayHuman, ""},
		{"unknown way", "deferred", nil, PolicyRule, false, "", "way_invalid"},
	} {
		way, why, err := ChooseWay(tc.requested, tc.aspects, tc.policy, tc.agent)
		if code(err) != tc.err || way != tc.want || (err == nil && len(why) == 0) {
			t.Errorf("%s: %q %v %v", tc.name, way, why, err)
		}
	}
}

func TestTriggerMatchesBranchPackageAndTitles(t *testing.T) {
	for _, tc := range []struct {
		trigger string
		context []string
		want    bool
	}{
		{"wenn wir die Auth bauen", []string{"feat/auth-login", "Anmeldung"}, true},
		{"wenn wir die Auth bauen", []string{"feat/ui", "Oberfläche"}, false},
		{"wenn wir die Anmeldung bauen", []string{"feat/x", "Anmeldung per OAuth"}, true},
		{"sobald REQ-7 läuft", []string{"REQ-7 Dinge"}, true},
		{"sobald REQ-7 läuft", []string{"REQ-70 Dinge"}, false},
		{"wenn wir X bauen", []string{"alles andere"}, false},
		{"when we build the webhook sync", []string{"Webhook-Anbindung"}, true},
		{"", []string{"x"}, false},
		{"wenn wir das bauen", nil, false},
	} {
		if got := TriggerMatches(tc.trigger, tc.context...); got != tc.want {
			t.Errorf("%q in %v = %v, want %v", tc.trigger, tc.context, got, tc.want)
		}
	}
}

func TestVariantAndQuestionAreCleaned(t *testing.T) {
	if _, err := CleanVariant(VariantInput{Title: "zwei\nZeilen"}); err == nil {
		t.Error("multi-line variant title")
	}
	if _, err := CleanVariant(VariantInput{Title: "ok", Pro: "a‮b"}); err == nil {
		t.Error("bidi override in pro")
	}
	v, err := CleanVariant(VariantInput{Title: " A-Weg ", Text: "mehr\r\nText", Pro: "schnell", Contra: "teuer"})
	if err != nil || v.Title != "A-Weg" || strings.Contains(v.Text, "\r") {
		t.Fatalf("%+v %v", v, err)
	}
	if _, err := CleanQuestion("Wie genau?\nUnd dann?"); err == nil {
		t.Error("multi-line question")
	}
	if _, err := CleanQuestion(" "); code(err) != "question_required" {
		t.Errorf("empty question: %v", err)
	}
	if VariantLabel(0) != "A" || VariantLabel(2) != "C" {
		t.Error("labels")
	}
}

func TestOnlyAnOpenHumanDecisionNeedsAPerson(t *testing.T) {
	for _, tc := range []struct {
		d    Decision
		want bool
	}{
		{Decision{State: DecisionOpen, Way: WayHuman}, true},
		{Decision{State: DecisionOpen, Way: WayAgent}, false},
		{Decision{State: DecisionDecided, Way: WayHuman}, false},
		{Decision{State: DecisionDeferred, Way: WayDeferred}, false},
	} {
		if tc.d.NeedsHuman() != tc.want {
			t.Errorf("%+v", tc.d)
		}
	}
}
