package workflow

import (
	"slices"
	"strings"
	"testing"
)

const phHead = "---\non: idea.created\nuses: ghosttree/idea-match\nmodel: mittel\nresults:\n  comment: 1\n---\n"

func phProblems(text string) []Problem {
	_, rep := Check(text, Env{})
	var out []Problem
	for _, p := range append(slices.Clone(rep.Errors), rep.Warnings...) {
		if strings.Contains(p.Msg, "placeholder") {
			out = append(out, p)
		}
	}
	return out
}

// REQ-708: a misspelt placeholder in the instruction is an error on its own
// line of the file, and the message names the nearest input.
func TestMisspeltPlaceholderIsAnErrorWithLineAndSuggestion(t *testing.T) {
	text := phHead + "Compare {{idea.text}} with the rest.\nAlso name {{idea.titel}} and {{requests.offen}}.\n"
	probs := phProblems(text)
	if len(probs) != 2 {
		t.Fatalf("problems: %+v", probs)
	}
	// The front matter has 7 lines (the closing --- is line 7), the text starts on line 8.
	if probs[0].Line != 9 || probs[0].Key != BodyKey || probs[0].Item != "idea.titel" || !strings.Contains(probs[0].Msg, "did you mean {{idea.title}}?") {
		t.Fatalf("first: %+v", probs[0])
	}
	if probs[1].Line != 9 || !strings.Contains(probs[1].Msg, "did you mean {{requests.open}}?") {
		t.Fatalf("second: %+v", probs[1])
	}
}

// A name no input resembles lists the inputs instead.
func TestUnknownPlaceholderWithoutALookalikeListsTheInputs(t *testing.T) {
	probs := phProblems(phHead + "{{completely.else}}\n")
	if len(probs) != 1 || !strings.Contains(probs[0].Msg, "(inputs: ") || strings.Contains(probs[0].Msg, "did you mean") {
		t.Fatalf("problems: %+v", probs)
	}
}

// Known inputs pass when the workflow reads them: declared under inputs: or
// always read by the block; spaces inside the braces do not matter.
func TestKnownPlaceholdersPassWhenRead(t *testing.T) {
	text := "---\non: idea.created\nuses: ghosttree/idea-match\nmodel: mittel\ninputs:\n  - idea.number\nresults:\n  comment: 1\n---\n{{ idea.text }} and {{idea.number}} and {{idea.related}}\n"
	if probs := phProblems(text); len(probs) != 0 {
		t.Fatalf("problems: %+v", probs)
	}
}

// A real input that the workflow does not read is a warning (the model would
// not receive it), one the event does not carry an error.
func TestPlaceholderOfAnInputTheWorkflowDoesNotReadOrTheEventLacks(t *testing.T) {
	probs := phProblems(phHead + "{{idea.title}} {{pr.diff}}\n")
	if len(probs) != 2 {
		t.Fatalf("problems: %+v", probs)
	}
	_, rep := Check(phHead+"{{idea.title}} {{pr.diff}}\n", Env{})
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0].Msg, "add idea.title under inputs:") || rep.Warnings[0].Line != 8 {
		t.Fatalf("not read: %+v", rep.Warnings)
	}
	if len(rep.Errors) != 1 || !strings.Contains(rep.Errors[0].Msg, "not there on idea.created") {
		t.Fatalf("wrong event: %+v", rep.Errors)
	}
}

// The prompt: key of an own prompt is checked as well, on its own line.
func TestPlaceholderInThePromptKey(t *testing.T) {
	text := "---\non: pr.merged\nmodel: mittel\ninputs:\n  - pr.diff\nresults:\n  issue: 1\nprompt: \"Read {{pr.dif}} now\"\n---\n"
	probs := phProblems(text)
	if len(probs) != 1 || probs[0].Key != KeyPrompt || probs[0].Line != 8 || !strings.Contains(probs[0].Msg, "{{pr.diff}}") {
		t.Fatalf("problems: %+v", probs)
	}
}

// The suggestion also helps with the other names of a file.
func TestNearestNameForEventsAndKinds(t *testing.T) {
	_, rep := Check("---\non: idea.creat\nuses: ghosttree/echo\nresults:\n  coment: 1\n---\n", Env{})
	var msgs []string
	for _, p := range rep.Errors {
		msgs = append(msgs, p.Msg)
	}
	all := strings.Join(msgs, "\n")
	if !strings.Contains(all, "did you mean idea.created?") || !strings.Contains(all, "did you mean comment?") {
		t.Fatalf("messages: %s", all)
	}
	_, rep = Check("---\non: idea.created\nuses: ghosttree/echo\nresults:\n  knowledge: 1\n---\n", Env{})
	if rep.OK() || !strings.Contains(rep.Errors[0].Msg, "it creates no results") {
		t.Fatalf("echo: %+v", rep.Errors)
	}
}

func TestEditDistance(t *testing.T) {
	for _, c := range []struct {
		a, b string
		d    int
	}{{"", "abc", 3}, {"open", "open", 0}, {"offen", "open", 2}, {"titel", "title", 2}} {
		if got := editDistance(c.a, c.b); got != c.d {
			t.Fatalf("%q %q: %d, want %d", c.a, c.b, got, c.d)
		}
	}
}

// An event that does not exist is one error, not a second one about the block.
func TestUnknownEventIsReportedOnce(t *testing.T) {
	_, rep := Check("---\non: idee.erstellt\nuses: ghosttree/idea-match\nmodel: mittel\nresults:\n  comment: 1\n---\n", Env{})
	if len(rep.Errors) != 1 || !strings.Contains(rep.Errors[0].Msg, "unknown event idee.erstellt") {
		t.Fatalf("errors: %+v", rep.Errors)
	}
}

// A block that creates nothing says so instead of listing nothing.
func TestBlockWithoutResultsSaysSo(t *testing.T) {
	_, rep := Check("---\non: idea.created\nuses: ghosttree/echo\nresults:\n  comment: 1\n---\n", Env{})
	if rep.OK() || !strings.Contains(rep.Errors[0].Msg, "ghosttree/echo cannot create comment (it creates no results)") {
		t.Fatalf("errors: %+v", rep.Errors)
	}
}

func TestBlockWithResultsListsThem(t *testing.T) {
	_, rep := Check("---\non: idea.created\nuses: ghosttree/idea-match\nmodel: mittel\nresults:\n  knowledge: 1\n---\n", Env{})
	if rep.OK() || !strings.Contains(rep.Errors[0].Msg, "it can create: comment, relation") {
		t.Fatalf("errors: %+v", rep.Errors)
	}
}
