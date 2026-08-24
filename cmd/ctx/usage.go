package main

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const usageCmdUsage = `usage: ctx usage --db <path> [--days 30] [--limit 50]

List active knowledge that has not been delivered or hit within the given
window, least used first. This is the basis for deciding whether the bootstrap
needs ranking and whether staleness should follow use rather than age.`

func cmdUsage(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	days := fs.Int("days", 30, "window in days")
	limit := fs.Int("limit", 50, "maximum entries to list")
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
