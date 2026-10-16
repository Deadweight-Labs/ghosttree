package server

import (
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
)

// Caps of the bootstrap section: what a human can read at the start of a session.
const (
	waitingMaxEach = 5
	waitingMaxText = 120
)

func waitingText(s string) string {
	s = oneLine(s)
	r := []rune(s)
	if len(r) <= waitingMaxText {
		return s
	}
	return string(r[:waitingMaxText-1]) + "…"
}

func waitingLine(t humantask.Task, done bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- TASK-%d [%s] %s", t.ID, t.Kind, oneLine(t.Title))
	if t.Location != "" {
		fmt.Fprintf(&b, " -> %s", oneLine(t.Location))
	}
	fmt.Fprintf(&b, " (REQ-%d)", t.RequestID)
	if done {
		if t.CheckHint != "" {
			fmt.Fprintf(&b, " | check: %s", waitingText(t.CheckHint))
		}
		if t.DoneNote != "" {
			fmt.Fprintf(&b, " | human: %q", waitingText(t.DoneNote))
		}
		if t.Handoff != "" {
			fmt.Fprintf(&b, " | handoff: %q", waitingText(t.Handoff))
		}
		if t.DoneVia == "token" {
			b.WriteString(" | marked via token")
		}
	} else if t.Assignee != "" {
		fmt.Fprintf(&b, ", for %s", oneLine(t.Assignee))
	}
	b.WriteString("\n")
	return b.String()
}

// renderWaiting is the bootstrap section for human tasks: finished ones first,
// because the agent has something to check, then those still open. Empty when
// there is nothing to show.
func renderWaiting(w humantask.Waiting) string {
	if len(w.Done) == 0 && len(w.Open) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Waiting on the human (ghosttree)\n")
	var confirmable, others []humantask.Task
	for _, t := range w.Done {
		if t.CanConfirm {
			confirmable = append(confirmable, t)
		} else {
			others = append(others, t)
		}
	}
	if len(confirmable) > 0 {
		b.WriteString("\nDone by the human, not yet confirmed. Check each against its check line, then human_task_resolve with action confirm, or action reopen with a reason:\n")
		for _, t := range confirmable {
			b.WriteString(waitingLine(t, true))
		}
	}
	if len(others) > 0 {
		b.WriteString("\nDone by the human, waiting for their creator to confirm (nothing for you to do):\n")
		for _, t := range others {
			b.WriteString(waitingLine(t, true))
		}
	}
	if len(w.Open) > 0 {
		b.WriteString("\nStill open. Never mark these done yourself, never ask for the value, only for where it lives:\n")
		for _, t := range w.Open {
			b.WriteString(waitingLine(t, false))
		}
	}
	if more := w.MoreDone + w.MoreOpen; more > 0 {
		fmt.Fprintf(&b, "(+%d more: ctx task list)\n", more)
	}
	return b.String()
}
