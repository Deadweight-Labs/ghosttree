package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Markup with placeholders is made in one place (sentenceOf), whose arguments are
// markup that is already safe. A fresh template.HTML(fmt.Sprintf(...)) anywhere else
// would take plain strings again, which is how a text of a person becomes markup.
func TestNoHandBuiltMarkupWithSprintf(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	pattern := regexp.MustCompile(`template\.HTML\(\s*fmt\.Sprintf`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "sentence.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if loc := pattern.FindIndex(src); loc != nil {
			line := strings.Count(string(src[:loc[0]]), "\n") + 1
			t.Errorf("%s:%d builds markup with fmt.Sprintf: use sentenceOf (arguments as template.HTML) or concatenate escaped parts", f, line)
		}
	}
}

// What a person wrote reaches a sentence only through escHTML, so a payload stays text.
func TestSentenceEscapesWhatAPersonWrote(t *testing.T) {
	l := lang("en")
	payload := `"><img src=x onerror=alert(1)>`
	got := string(sentenceOf(l, "issue.ev.task_created_for", escHTML(payload), numHTML(7)))
	if strings.Contains(got, "<img") || !strings.Contains(got, "&lt;img") {
		t.Fatalf("a payload became markup: %s", got)
	}
}
