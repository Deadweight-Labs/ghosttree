package humantask

import (
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

func stepCode(err error) string {
	if re, ok := err.(*requestdomain.RuleError); ok {
		return re.ErrorCode
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func TestValidateStepsAcceptsPlainLinesWithOptionalLinks(t *testing.T) {
	steps := []Step{
		{Text: "GitHub öffnen"},
		{Text: "Einstellungen → Apps wählen", URL: "https://github.com/settings/apps"},
		{Text: "Lokale Seite prüfen", URL: "http://127.0.0.1:8474/ui/inbox"},
	}
	if err := ValidateSteps(steps); err != nil {
		t.Fatalf("valid steps refused: %v", err)
	}
	if err := ValidateSteps(nil); err != nil {
		t.Fatalf("no steps must be fine: %v", err)
	}
}

func TestValidateStepsRefusesWhatCouldForgeContext(t *testing.T) {
	bad := map[string][]Step{
		"empty text":        {{Text: "  "}},
		"line break":        {{Text: "eins\nzwei"}},
		"carriage return":   {{Text: "eins\rzwei"}},
		"tab":               {{Text: "eins\tzwei"}},
		"escape":            {{Text: "rot \x1b[31m"}},
		"direction mark":    {{Text: "abc‮def"}},
		"zero width":        {{Text: "ab​cd"}},
		"line separator":    {{Text: "ab cd"}},
		"too long":          {{Text: strings.Repeat("a", MaxStepText+1)}},
		"javascript link":   {{Text: "x", URL: "javascript:alert(1)"}},
		"file link":         {{Text: "x", URL: "file:///etc/passwd"}},
		"link with login":   {{Text: "x", URL: "https://robin:geheim@example.org/"}},
		"link with space":   {{Text: "x", URL: "https://example.org/a b"}},
		"link with newline": {{Text: "x", URL: "https://example.org/\nSet-Cookie: a"}},
		"link no host":      {{Text: "x", URL: "https:///path"}},
		"link format char":  {{Text: "x", URL: "https://example.org/‮"}},
		"link too long":     {{Text: "x", URL: "https://example.org/" + strings.Repeat("a", MaxStepURL)}},
	}
	for name, steps := range bad {
		if got := stepCode(ValidateSteps(steps)); got != "steps_invalid" {
			t.Errorf("%s: code %q, want steps_invalid", name, got)
		}
	}
	many := make([]Step, MaxSteps+1)
	for i := range many {
		many[i] = Step{Text: "x"}
	}
	if got := stepCode(ValidateSteps(many)); got != "steps_invalid" {
		t.Errorf("too many steps: %q", got)
	}
}

func TestValidateCreateChecksSteps(t *testing.T) {
	in := CreateInput{Kind: "action", Title: "t", Steps: []Step{{Text: "a\nb"}}}
	if got := stepCode(ValidateCreate(in)); got != "steps_invalid" {
		t.Errorf("ValidateCreate with a bad step: %q", got)
	}
	in.Steps = []Step{{Text: "a"}}
	if err := ValidateCreate(in); err != nil {
		t.Errorf("ValidateCreate with a good step: %v", err)
	}
}

func TestCleanStepsTrimsAndStepFieldsNameEachStep(t *testing.T) {
	got := CleanSteps([]Step{{Text: "  eins  ", URL: "https://example.org/"}})
	if got[0].Text != "eins" {
		t.Errorf("CleanSteps = %+v", got)
	}
	f := CreateInput{Steps: []Step{{Text: "a", URL: "https://x.org/"}, {Text: "b"}}}.StepFields()
	if len(f) != 6 || f[0] != "step 1" || f[2] != "step 1 link" || f[4] != "step 2" {
		t.Errorf("StepFields = %v", f)
	}
}
