package web

import (
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
)

// "Kurz erklärt" and the steps of a task (REQ-599, REQ-603). An agent that asks
// a person to do something writes a short explanation and, if it helps, a
// walk-through. Both belong to the task and are read where the task is shown: as
// a muted line under the title of its row in the inbox (unfolding to the whole
// text and the steps) and on the hover card of its reference. The text comes from
// what the agent wrote or what the batch generated, never from a request: the page
// does not make the model write while someone looks.

// taskNote is what a task says about itself besides its title.
type taskNote struct {
	// Title is the label "Kurz erklärt"; Mark says where the text comes from.
	Title, Mark, MarkTitle string
	// Lead is the first sentence, Text the whole explanation (empty when there is none).
	Lead, Text string
	Steps      []taskStep
	// StepsMore is how many steps a card leaves out.
	StepsMore int
	// Fold is true when there is more behind the line than the line shows: more text or steps.
	Fold bool
}

// taskStep is one step of the walk-through; URL is an http(s) address or empty.
type taskStep struct{ Text, URL string }

// cardSteps is how many steps the hover card lists.
const cardSteps = 4

// taskNoteFor reads the explanation and the steps of a task. It is nil when the
// task has neither.
func (a *app) taskNoteFor(l lang, id int64, steps []humantask.Step) *taskNote {
	n := &taskNote{}
	if v := a.explainStoredFor(l, explain.KindTask, id); v != nil && v.Text != "" {
		n.Title, n.Mark, n.MarkTitle, n.Text = v.Title, v.Mark, v.MarkTitle, v.Text
		n.Lead = leadSentence(v.Text, 160)
	}
	for _, s := range steps {
		n.Steps = append(n.Steps, taskStep{Text: s.Text, URL: s.URL})
	}
	if n.Text == "" && len(n.Steps) == 0 {
		return nil
	}
	if n.Title == "" {
		n.Title = l.msg("explain.title")
	}
	n.Fold = len(n.Steps) > 0 || utf8.RuneCountInString(oneLineText(n.Text)) > utf8.RuneCountInString(n.Lead)
	return n
}

// forCard is the note shortened for a hover card: the first steps and how many are left.
func (n *taskNote) forCard() *taskNote {
	if n == nil {
		return nil
	}
	c := *n
	if len(c.Steps) > cardSteps {
		c.StepsMore = len(c.Steps) - cardSteps
		c.Steps = c.Steps[:cardSteps]
	}
	return &c
}
