package installer

import (
	"strings"
	"testing"
)

// The rule text asks agents for the explanation on everything people read.
func TestRuleTextAsksForAnExplanationWhereHumansReadWhatAgentsWrite(t *testing.T) {
	for _, want := range []string{"`explanation`", "`context_remember`", "`request_create`", "`pain_record`", "`human_task_create`", "`coord_send`", "intent question", "recommendation", "No markdown, no secret"} {
		if !strings.Contains(ruleText, want) {
			t.Errorf("the rule text does not mention %q", want)
		}
	}
}
