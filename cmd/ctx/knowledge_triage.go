package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/knref"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const triageUsage = `usage: ctx knowledge triage [--project P] [--limit N]

Lists machine and global entries for review. It changes nothing; a person
decides what to do with each line.

  move    arose in exactly one project: probably knowledge about that project on
          a host. 'ctx knowledge move <id> --project P' files it there (add the
          machine with 'ctx knowledge add --machine' next time).
  unused  not delivered on a word of a prompt for 90 days and never found by a
          search: keep it, retire it, or flag it with 'ctx knowledge everywhere'.

  --project P  only the entries that arose in this project (default: all)
  --limit N    at most N lines (default 50)`

const everywhereUsage = `usage: ctx knowledge everywhere <id> [--off]

Flags a machine or global entry as holding in every project. The briefing of a
project carries machine and global knowledge only when it arose or was
evidenced in that project or carries this flag. --off takes the flag back.`

func cmdKnowledgeTriage(args []string, stdout io.Writer) int {
	if helpRequested(args) {
		fmt.Fprintln(stdout, triageUsage)
		return 0
	}
	fs := flag.NewFlagSet("knowledge triage", flag.ContinueOnError)
	project := fs.String("project", "", "only entries that arose in this project")
	limit := fs.Int("limit", 50, "at most this many lines")
	if code, ok := parseKnowledgeFlags(fs, triageUsage, args, stdout); !ok {
		return code
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "no config (%s) - run 'ctx setup'\n", config.Path())
		return 1
	}
	items, err := client.New(cfg).KnowledgeTriage(scope.NormalizeRemote(*project), *limit)
	if err != nil {
		fmt.Fprintf(stdout, "triage: %v\n", err)
		return 1
	}
	printTriage(stdout, items)
	return 0
}

func printTriage(stdout io.Writer, items []client.TriageEntry) {
	groups := []struct {
		kind  store.TriageKind
		title string
	}{
		{store.TriageMove, "arose in exactly one project (move?)"},
		{store.TriageUnused, "not picked up for 90 days (keep, retire or flag?)"},
	}
	for _, g := range groups {
		n := 0
		for _, it := range items {
			if it.Kind != g.kind {
				continue
			}
			if n == 0 {
				fmt.Fprintf(stdout, "%s\n", g.title)
			}
			n++
			k := it.Knowledge
			extra := ""
			if it.Project != "" {
				extra = " -> " + it.Project
			}
			if it.Deliveries > 0 {
				extra += fmt.Sprintf(" (%d× in the briefing)", it.Deliveries)
			}
			fmt.Fprintf(stdout, "  %-8s [%s] %s%s\n", knref.Format(k.ID), k.Scope.Label(), k.Title, extra)
		}
		if n > 0 {
			fmt.Fprintln(stdout)
		}
	}
	if len(items) == 0 {
		fmt.Fprintln(stdout, "nothing to review")
	}
}

func cmdKnowledgeEverywhere(args []string, stdout io.Writer) int {
	id, code, ok := parseKnowledgeID(args, everywhereUsage, stdout)
	if !ok {
		return code
	}
	fs := flag.NewFlagSet("knowledge everywhere", flag.ContinueOnError)
	off := fs.Bool("off", false, "take the flag back")
	if code, ok := parseKnowledgeFlags(fs, everywhereUsage, args[1:], stdout); !ok {
		return code
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "no config (%s) - run 'ctx setup'\n", config.Path())
		return 1
	}
	if err := client.New(cfg).SetKnowledgeEverywhere(id, !*off); err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	if *off {
		fmt.Fprintf(stdout, "%s no longer applies everywhere\n", knref.Format(id))
	} else {
		fmt.Fprintf(stdout, "%s applies everywhere\n", knref.Format(id))
	}
	return 0
}
