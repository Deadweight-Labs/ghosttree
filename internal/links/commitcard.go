package links

import (
	"fmt"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// CommitCard beschreibt einen Commit: die Titelzeile, wenn der Server sie kennt,
// und die Zahl der Einträge, die dort geschrieben wurden.
func (b Builder) CommitCard(c store.CommitInfo) Card {
	title := c.Title
	if title == "" {
		title = "commit " + c.Short
	}
	next := fmt.Sprintf("%d entries", c.Entries)
	if len(c.PRs) > 0 {
		next += fmt.Sprintf(" | PR #%d", c.PRs[0])
	}
	return Card{Kind: "Commit", Ref: c.Short, Title: title, Next: next, Who: c.Author, URL: b.Commit(c.SHA)}.Sanitized()
}
