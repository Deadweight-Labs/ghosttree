package migrate

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
)

func TestBudgetAllowsMutuallyExclusiveSubtrees(t *testing.T) {
	long := strings.Repeat("x", 900)
	candidates := []InstructionCandidate{
		{Title: "core", Body: long, Activation: activation.Rule{Paths: []string{"core/**"}}},
		{Title: "ui", Body: long, Activation: activation.Rule{Paths: []string{"ui/**"}}},
	}
	conflict, err := CheckInstructionBudget(nil, candidates, 1500)
	if err != nil || conflict != nil {
		t.Fatalf("conflict=%+v err=%v", conflict, err)
	}
}

func TestBudgetReportsSimultaneouslyActiveRules(t *testing.T) {
	candidates := []InstructionCandidate{
		{Title: "root", Body: strings.Repeat("r", 800)},
		{Title: "core", Body: strings.Repeat("c", 800), Activation: activation.Rule{Paths: []string{"core/**"}}},
	}
	conflict, err := CheckInstructionBudget(nil, candidates, 1500)
	if err != nil {
		t.Fatal(err)
	}
	if conflict == nil || conflict.Context.RepoPath != "core" || conflict.Chars != 1600 {
		t.Fatalf("conflict=%+v", conflict)
	}
}

func TestBudgetFindsOverlappingGlobs(t *testing.T) {
	candidates := []InstructionCandidate{
		{Title: "left", Body: strings.Repeat("l", 800), Activation: activation.Rule{Paths: []string{"a/*/c"}}},
		{Title: "right", Body: strings.Repeat("r", 800), Activation: activation.Rule{Paths: []string{"a/b/*"}}},
	}
	conflict, err := CheckInstructionBudget(nil, candidates, 1500)
	if err != nil {
		t.Fatal(err)
	}
	if conflict == nil || conflict.Chars != 1600 {
		t.Fatalf("conflict=%+v", conflict)
	}
}

func TestBudgetFindsOverlapWithinGlobSegment(t *testing.T) {
	candidates := []InstructionCandidate{
		{Title: "left", Body: strings.Repeat("l", 800), Activation: activation.Rule{Paths: []string{"a*b*"}}},
		{Title: "right", Body: strings.Repeat("r", 800), Activation: activation.Rule{Paths: []string{"*bc"}}},
	}
	conflict, err := CheckInstructionBudget(nil, candidates, 1500)
	if err != nil || conflict == nil || conflict.Chars != 1600 {
		t.Fatalf("conflict=%+v err=%v", conflict, err)
	}
}
