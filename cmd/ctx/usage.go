package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const usageCmdUsage = `usage: ctx usage --db <path> [--days 30] [--limit 50]
       ctx usage --db <path> --tools [--prefix mcp__ghosttree__]

List active knowledge that has not been delivered or hit within the given
window, least used first. This is the basis for deciding whether the bootstrap
needs ranking and whether staleness should follow use rather than age.

--tools answers the other half: how often a tool was actually called, per
project. It counts archived calls, not mentions of a tool's name in prose, and
so only covers sessions collected after calls started being archived.`

func cmdUsage(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	days := fs.Int("days", 30, "window in days")
	limit := fs.Int("limit", 50, "maximum entries to list")
	tools := fs.Bool("tools", false, "report tool calls per project instead of unused knowledge")
	relevance := fs.Bool("relevance", false, "replay archived prompts through the relevance rule")
	sample := fs.Int("sample", 200, "with --relevance: how many archived prompts to replay")
	prefix := fs.String("prefix", "mcp__ghosttree__", "tool name prefix to count with --tools; empty counts every tool")
	fs.Usage = func() { fmt.Fprintln(stdout, usageCmdUsage) }
	if fs.Parse(args) != nil || *dbPath == "" || *days <= 0 {
		fmt.Fprintln(stdout, usageCmdUsage)
		return 2
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return 1
	}
	defer st.Close()

	if *relevance {
		probes, err := st.ProbeRelevance(*sample, 3)
		if err != nil {
			fmt.Fprintf(stdout, "query: %v\n", err)
			return 1
		}
		fired := 0
		for _, p := range probes {
			if len(p.Titles) > 0 {
				fired++
			}
		}
		fmt.Fprintf(stdout, "%d archived prompts replayed, %d delivered something (%.1f%%)\n",
			len(probes), fired, 100*float64(fired)/float64(max(1, len(probes))))
		fmt.Fprintln(stdout, "\nwhat fired, most recent first:")
		shown := 0
		for _, p := range probes {
			if len(p.Titles) == 0 || shown >= 12 {
				continue
			}
			shown++
			fmt.Fprintf(stdout, "  %q\n", shorten(p.Prompt, 100))
			for _, title := range p.Titles {
				fmt.Fprintf(stdout, "      -> %s\n", shorten(title, 96))
			}
		}
		return 0
	}

	if *tools {
		rows, err := st.ToolCallsPerProject(*prefix)
		if err != nil {
			fmt.Fprintf(stdout, "query: %v\n", err)
			return 1
		}
		if len(rows) == 0 {
			fmt.Fprintf(stdout, "no archived calls to %q\n", *prefix)
			return 0
		}
		fmt.Fprintf(stdout, "archived calls to %q, by project:\n", *prefix)
		for _, r := range rows {
			project := r.Project
			if project == "" {
				project = "(no project)"
			}
			fmt.Fprintf(stdout, "  %-44s %5d calls in %d sessions\n", project, r.Calls, r.Sessions)
		}
		return 0
	}
	cutoff := time.Now().AddDate(0, 0, -*days).UTC().Format(time.RFC3339)
	unused, err := st.KnowledgeUnusedSince(cutoff)
	if err != nil {
		fmt.Fprintf(stdout, "query: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%d active entries not used since %s\n", len(unused), cutoff)
	for i, k := range unused {
		if i >= *limit {
			// Say what was withheld: a truncated list that looks complete is
			// how a backlog gets underestimated.
			fmt.Fprintf(stdout, "…and %d more\n", len(unused)-*limit)
			break
		}
		hits, lastUsed, err := st.KnowledgeUsage(k.ID)
		if err != nil {
			fmt.Fprintf(stdout, "usage for %d: %v\n", k.ID, err)
			return 1
		}
		if lastUsed == "" {
			lastUsed = "never"
		}
		fmt.Fprintf(stdout, "  #%d [%s|%s] %s — %d hits, last %s\n",
			k.ID, k.Type, k.Confidence, k.Title, hits, lastUsed)
	}
	return 0
}

// shorten collapses a prompt to one readable line. Cutting on a rune boundary
// matters because the prompts are German.
func shorten(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxRunes {
		return string(r[:maxRunes]) + "…"
	}
	return s
}
