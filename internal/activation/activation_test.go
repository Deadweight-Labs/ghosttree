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

func TestNormalizeContextRejectsEscapesAndUnknownTask(t *testing.T) {
	for _, ctx := range []Context{{RepoPath: "../x"}, {Paths: []string{"/tmp/x"}}, {Task: "release"}} {
		if _, err := NormalizeContext(ctx); err == nil {
			t.Errorf("NormalizeContext(%+v) succeeded", ctx)
		}
	}
}

func TestUngatedRuleAlwaysMatches(t *testing.T) {
	if !Matches(Rule{}, Context{}) {
		t.Error("ungated instruction must match")
	}
}
