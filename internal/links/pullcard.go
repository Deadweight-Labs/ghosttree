package links

import (
	"fmt"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// PullCard beschreibt einen PR für Listen und Antworten über genau ihn: Titel,
// Zustand, Zweige und die Zahl der Commits.
func (b Builder) PullCard(p store.PullRequestInfo, commits int) Card {
	next := p.Head
	if p.Base != "" {
		next += " → " + p.Base
	}
	if commits > 0 {
		next += fmt.Sprintf(" | %d commits", commits)
	}
	return Card{Kind: "Pull request", Ref: "#" + num(p.Number), Title: p.Title, Status: p.State, Next: next, Who: p.Author, URL: b.Pull(p.Number)}.Sanitized()
}
