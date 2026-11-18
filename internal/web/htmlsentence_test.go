package web

import (
	"html/template"
	"strings"
	"testing"
)

func TestHtmlSentenceEscapesStringArgumentsAndKeepsHTML(t *testing.T) {
	const payload = `"><img src=x onerror=alert(1)>`
	got := string(htmlSentence(langEN, "issue.ev.task_created_for", payload, template.HTML(`<a href="#task-3">TASK-3</a>`)))
	if strings.Contains(got, "<img") || strings.Contains(got, `"><`) {
		t.Fatalf("a string argument reached the page unescaped: %s", got)
	}
	if !strings.Contains(got, `&lt;img src=x onerror=alert(1)&gt;`) {
		t.Errorf("string argument is not shown as text: %s", got)
	}
	if !strings.Contains(got, `<a href="#task-3">TASK-3</a>`) {
		t.Errorf("template.HTML argument was escaped: %s", got)
	}
}
