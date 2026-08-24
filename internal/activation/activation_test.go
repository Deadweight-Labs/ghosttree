package activation

import "testing"

func TestMatchesUsesOrWithinAndAcrossDimensions(t *testing.T) {
	rule := Rule{Paths: []string{"core/**", "shared/**"}, Tasks: []string{"code", "review"}}
	cases := []struct {
		ctx  Context
		want bool
	}{
		{Context{RepoPath: "core", Task: "code"}, true},
		{Context{Paths: []string{"shared/x.ex"}, Task: "review"}, true},
		{Context{RepoPath: "core/lib/x.ex", Task: "code"}, true},
		{Context{RepoPath: "ui", Task: "code"}, false},
		{Context{RepoPath: "core", Task: "deploy"}, false},
		{Context{}, false},
	}
	for _, tc := range cases {
		if got := Matches(rule, tc.ctx); got != tc.want {
			t.Errorf("Matches(%+v)=%v want %v", tc.ctx, got, tc.want)
		}
	}
}

func TestValidateRejectsUnsafeRules(t *testing.T) {
	bad := []Rule{{Paths: []string{"/etc/**"}}, {Paths: []string{"../other/**"}}, {Paths: []string{"[broken"}}, {Tasks: []string{"release"}}}
	for _, rule := range bad {
		if err := ValidateRule(rule); err == nil {
			t.Errorf("ValidateRule(%+v) accepted unsafe rule", rule)
		}
	}
}

// Path escapes are a safety matter and stay rejected. The task, however, is
// the agent's own guess about what it is currently doing, and a wrong guess
// must not end the session.
func TestNormalizeContextRejectsEscapes(t *testing.T) {
	for _, ctx := range []Context{{RepoPath: "../x"}, {Paths: []string{"/tmp/x"}}} {
		if _, err := NormalizeContext(ctx); err == nil {
			t.Errorf("NormalizeContext(%+v) succeeded", ctx)
		}
	}
}

// A Codex session died on `unknown activation task "code review"` while no
// stored instruction used task gating at all. An unrecognised task now reads
// as no task, which is an already defined state.
func TestNormalizeContextDropsUnknownTaskInsteadOfFailing(t *testing.T) {
	got, err := NormalizeContext(Context{Task: "code review", RepoPath: "internal"})
	if err != nil {
		t.Fatalf("unknown task must not fail the call: %v", err)
	}
	if got.Task != "" {
		t.Errorf("Task = %q, want it dropped", got.Task)
	}
	if got.RepoPath != "internal" {
		t.Errorf("RepoPath = %q, want the rest of the context kept", got.RepoPath)
	}
}

func TestUngatedRuleAlwaysMatches(t *testing.T) {
	if !Matches(Rule{}, Context{}) {
		t.Error("ungated instruction must match")
	}
}
