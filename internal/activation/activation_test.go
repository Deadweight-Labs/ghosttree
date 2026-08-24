package activation

import "testing"

func TestMatchesAcceptsAnyOfTheGatedPaths(t *testing.T) {
	rule := Rule{Paths: []string{"core/**", "shared/**"}}
	cases := []struct {
		ctx  Context
		want bool
	}{
		{Context{RepoPath: "core"}, true},
		{Context{Paths: []string{"shared/x.ex"}}, true},
		{Context{RepoPath: "core/lib/x.ex"}, true},
		{Context{RepoPath: "ui"}, false},
		{Context{}, false},
	}
	for _, tc := range cases {
		if got := Matches(rule, tc.ctx); got != tc.want {
			t.Errorf("Matches(%+v)=%v want %v", tc.ctx, got, tc.want)
		}
	}
}

// An instruction with no gate at all is unconditional, which is what an empty
// rule has always meant.
func TestUngatedRuleMatchesEverything(t *testing.T) {
	if !Matches(Rule{}, Context{}) {
		t.Error("an ungated instruction stopped applying")
	}
}

func TestValidateRejectsUnsafeRules(t *testing.T) {
	bad := []Rule{{Paths: []string{"/etc/**"}}, {Paths: []string{"../other/**"}}, {Paths: []string{"[broken"}}}
	for _, rule := range bad {
		if err := ValidateRule(rule); err == nil {
			t.Errorf("ValidateRule(%+v) accepted unsafe rule", rule)
		}
	}
}

// Path escapes are a safety matter and stay rejected.
func TestNormalizeContextRejectsEscapes(t *testing.T) {
	for _, ctx := range []Context{{RepoPath: "../x"}, {Paths: []string{"/tmp/x"}}} {
		if _, err := NormalizeContext(ctx); err == nil {
			t.Errorf("NormalizeContext(%+v) succeeded", ctx)
		}
	}
}

// The task gate is gone, so a caller that still sends one cannot be harmed by
// it. This is the case that killed a Codex session on `unknown activation task
// "code review"` when no stored instruction used task gating at all.
func TestContextWithoutTaskGateNormalizesCleanly(t *testing.T) {
	got, err := NormalizeContext(Context{RepoPath: "internal"})
	if err != nil {
		t.Fatalf("normalizing a plain context failed: %v", err)
	}
	if got.RepoPath != "internal" {
		t.Errorf("RepoPath = %q, want it kept", got.RepoPath)
	}
}
