package profile

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/coretools"
)

func TestParseSetsAProfileOnlyOnPurpose(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Profile
		ok   bool
	}{
		{"", Full, true},
		{"  ", Full, true},
		{"full", Full, true},
		{"lean", Lean, true},
		{" Lean\n", Lean, true},
		{"LEAN", Lean, true},
		// A value nobody defined is not a guess at one: the session is full.
		{"leen", Full, false},
		{"auto", Full, false},
		{"minimal", Full, false},
	} {
		got, ok := Parse(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("Parse(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestFromEnvReadsTheVariableAndDefaultsToFull(t *testing.T) {
	t.Setenv(EnvVar, "")
	if got := FromEnv(); got != Full {
		t.Errorf("without the variable: %q, want full", got)
	}
	t.Setenv(EnvVar, "lean")
	if got := FromEnv(); got != Lean {
		t.Errorf("with %s=lean: %q", EnvVar, got)
	}
	t.Setenv(EnvVar, "nonsense")
	if got := FromEnv(); got != Full {
		t.Errorf("with an unknown value: %q, want full", got)
	}
}

// The tiers are the decision of REQ-643: DEC-13 variant A for lean (the four
// tools of the core tier), DEC-8 variant D for the rare tier.
func TestTiersAreWhatTheDecisionsSay(t *testing.T) {
	names := func(p Profile) string {
		var up []string
		for _, c := range coretools.Table() {
			if p.Preloads(c.Tier) {
				up = append(up, c.Name)
			}
		}
		return strings.Join(up, " ")
	}
	if got, want := names(Lean), "context_search context_get context_remember request"; got != want {
		t.Errorf("lean loads up front: %s, want %s", got, want)
	}
	if got, want := names(Full), "context_search context_get context_remember request pain idea coord human_task package"; got != want {
		t.Errorf("full loads up front: %s, want %s", got, want)
	}
	if got, want := strings.Join(Full.Deferred(), " "), "feature runbook comment"; got != want {
		t.Errorf("full defers: %s, want %s", got, want)
	}
	if got, want := strings.Join(Lean.Deferred(), " "), "pain idea coord human_task package feature runbook comment"; got != want {
		t.Errorf("lean defers: %s, want %s", got, want)
	}
	// The zero value is full, the way a Server built without a profile is.
	if got := names(""); got != names(Full) {
		t.Errorf("the zero profile loads %s, want what full loads", got)
	}
}

func TestEveryCoreToolHasATier(t *testing.T) {
	for _, c := range coretools.Table() {
		if c.Tier < coretools.TierCore || c.Tier > coretools.TierRare {
			t.Errorf("%s has tier %d", c.Name, c.Tier)
		}
	}
	if coretools.TierOf("not_a_tool") != coretools.TierRare {
		t.Errorf("an unknown name is not rare")
	}
}

func TestHookBudgets(t *testing.T) {
	if Full.HookBudget() != 24000 || Lean.HookBudget() != 6000 || Profile("").HookBudget() != 24000 {
		t.Errorf("budgets: full %d, lean %d, zero %d", Full.HookBudget(), Lean.HookBudget(), Profile("").HookBudget())
	}
	if LeanStartChars != 1500 {
		t.Errorf("a lean briefing is %d characters, DEC-13 says 1500", LeanStartChars)
	}
}
