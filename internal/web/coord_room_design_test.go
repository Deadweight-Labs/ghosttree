package web

import (
	"regexp"
	"strings"
	"testing"
)

// The policy forbids inline styles, inline script and event handlers.
func TestCoordTemplateHasNoInlineStyleScriptOrHandlers(t *testing.T) {
	tpl := coordTemplate(t)
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?i)\sstyle\s*=`),
		regexp.MustCompile(`(?i)\son[a-z]+\s*=`),
		regexp.MustCompile(`(?i)<script(?:[^>]*\ssrc=)?>`),
	} {
		if re.MatchString(tpl) {
			t.Errorf("room template contains %s", re)
		}
	}
}

func TestCoordScriptSendsOnEnterAndKeepsShiftEnterForNewLines(t *testing.T) {
	js := string(mustReadEmbedded(t, "static/ui-coord.js"))
	for _, want := range []string{
		`event.key !== "Enter"`, "event.shiftKey", "event.isComposing", "form.requestSubmit(button)",
		`=== "directive"`, `submitter?.hasAttribute("formaction")`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("composer script missing %q", want)
		}
	}
}
