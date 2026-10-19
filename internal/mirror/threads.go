package mirror

import (
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// ThreadView ist ein Thema samt dem, was der Spiegel davon zeigt.
type ThreadView struct {
	Thread  store.Thread
	Summary store.ThreadSummary
	// HasSummary trennt "keine Karte" von "leere Karte". Ohne das läse sich
	// ein Thema ohne Zusammenfassung wie eines, dessen Stand leer ist.
	HasSummary bool
	Links      []store.ThreadLink
	Outcomes   []store.ThreadOutcome
	NewPosts   int
}

// threadDocs schreibt die Themen als lesbare Dateien.
//
// Was hier NICHT hinkommt und warum, Spec §11: Live-Zustand höchstens als
// datierte Beobachtung. Eine fünf Minuten alte Markdown-Datei darf nicht
// behaupten, jemand arbeite GERADE an einem Pfad — deshalb spiegelt der Baum
// Threads und keine Presence.
//
// Und beschränkte Themen kommen gar nicht vor. Der Spiegel liegt im Repo und
// wird von jedem gelesen, der es auscheckt; ein aus einem privaten Gespräch
// übernommenes Thema hätte hier seinen sichersten Weg nach draußen.
func threadDocs(threads []ThreadView) []Doc {
	if len(threads) == 0 {
		return nil
	}
	docs := []Doc{{Path: "threads/INDEX.md", Body: threadIndex(threads)}}
	for _, v := range threads {
		docs = append(docs, Doc{Path: threadPath(v.Thread), Body: threadBody(v)})
	}
	return docs
}

func threadPath(t store.Thread) string {
	return fmt.Sprintf("threads/THR-%d-%s.md", t.ID, slug(t.Title))
}

func threadIndex(threads []ThreadView) string {
	var b strings.Builder
	b.WriteString("# Discussions\n\nWhat is being investigated here, as opposed to what is settled.\n")
	b.WriteString("Knowledge says what holds; a thread says what is still open and how a result came about.\n\n")

	var open, dormant, done []ThreadView
	for _, v := range threads {
		switch {
		case v.Thread.State != store.ThreadOpen:
			done = append(done, v)
		case v.Thread.Dormant:
			dormant = append(dormant, v)
		default:
			open = append(open, v)
		}
	}
	section := func(title, note string, list []ThreadView) {
		if len(list) == 0 {
			return
		}
		fmt.Fprintf(&b, "## %s\n\n", title)
		if note != "" {
			b.WriteString(note + "\n\n")
		}
		for _, v := range list {
			fmt.Fprintf(&b, "- [THR-%d %s](%s)", v.Thread.ID, v.Thread.Title,
				strings.TrimPrefix(threadPath(v.Thread), "threads/"))
			if v.Thread.Question != "" {
				fmt.Fprintf(&b, " — %s", oneLineish(v.Thread.Question))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	section("Open", "", open)
	// Ruhend ist NICHT gelöst, und der Spiegel sagt das, statt es der
	// Überschrift zu überlassen.
	section("Dormant", "No activity for a while. Dormant is not resolved — the question is still open.", dormant)
	section("Settled", "The discussion ended. That is not the same as work being done.", done)

	b.WriteString(footer())
	return b.String()
}

func threadBody(v ThreadView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# THR-%d %s\n\n", v.Thread.ID, v.Thread.Title)
	fmt.Fprintf(&b, "%s | %s", v.Thread.Project, v.Thread.State)
	if v.Thread.Dormant {
		b.WriteString(" | dormant — still open, just quiet")
	}
	fmt.Fprintf(&b, " | last activity %s\n\n", v.Thread.UpdatedAt)

	if v.Thread.Question != "" {
		fmt.Fprintf(&b, "**Question:** %s\n\n", v.Thread.Question)
	}

	if v.HasSummary {
		// Der Quellenstand gehört an die Karte, nicht in eine Fußnote: ohne
		// ihn liest sich eine alte Zusammenfassung wie der heutige Stand.
		fmt.Fprintf(&b, "## Working state\n\n_Summary revision %d, covers through post %d",
			v.Summary.Revision, v.Summary.CoversThrough)
		if v.NewPosts > 0 {
			fmt.Fprintf(&b, "; %d post%s since_\n\n", v.NewPosts, plural(v.NewPosts, "", "s"))
		} else {
			b.WriteString("_\n\n")
		}
		b.WriteString(v.Summary.Body + "\n\n")
		if v.Summary.OpenQuestions != "" {
			fmt.Fprintf(&b, "**Still open:** %s\n\n", v.Summary.OpenQuestions)
		}
	} else {
		b.WriteString("## Working state\n\nNo summary yet. The discussion is readable in full with `thread_read`.\n\n")
	}

	if len(v.Links) > 0 {
		b.WriteString("## Attached to\n\n")
		for _, l := range v.Links {
			fmt.Fprintf(&b, "- %s %s", l.Kind, l.ID)
			if l.Revision != "" {
				fmt.Fprintf(&b, " rev %s", l.Revision)
			} else {
				b.WriteString(" (current head — not a record of what it said then)")
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(v.Outcomes) > 0 {
		b.WriteString("## Outcomes\n\n")
		for _, o := range v.Outcomes {
			fmt.Fprintf(&b, "- **%s** %s [%s]", o.Kind, o.RefID, o.State)
			if o.Note != "" {
				fmt.Fprintf(&b, " — %s", oneLineish(o.Note))
			}
			b.WriteString("\n")
		}
		b.WriteString("\nA proposal is not an accepted rule, and a settled discussion is not implemented work.\n\n")
	}

	b.WriteString("Full history and new posts: `thread_read` with id " +
		fmt.Sprint(v.Thread.ID) + ".\n")
	b.WriteString(footer())
	return b.String()
}

// oneLineish hält eine Angabe auf einer Zeile. Der Index ist eine Liste, und
// ein Absatz darin verdrängt die nächsten fünf Themen.
func oneLineish(s string) string {
	line := strings.Join(strings.Fields(s), " ")
	const max = 120
	if len([]rune(line)) <= max {
		return line
	}
	return string([]rune(line)[:max]) + "…"
}
