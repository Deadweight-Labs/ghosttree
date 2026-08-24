package main

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const unbindBranchUsage = `usage: ctx unbind-branch --db <path>            list knowledge filed against a branch
       ctx unbind-branch --db <path> --dry-run <id>...  show what lifting would change
       ctx unbind-branch --db <path> <id>...            lift those entries to project scope

Until 2026-08-24 pitfalls, notes and plans were filed against the branch of the
session that wrote them. Reads match a branch exactly, so those entries are
invisible from every other branch and outlive the branch that named them.

Ids are named one by one on purpose. Whether a branch binding was deliberate is
not visible in the data — an entry on a feature branch may be there because
nobody chose otherwise, or because it stops being true once the branch is gone.
Read the list, then name what should move.`

func cmdUnbindBranch(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("unbind-branch", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	dryRun := fs.Bool("dry-run", false, "report what would change without writing")
	fs.Usage = func() { fmt.Fprintln(stdout, unbindBranchUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" {
		fmt.Fprintln(stdout, unbindBranchUsage)
		return 2
	}
	var ids []int64
	for _, raw := range fs.Args() {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			fmt.Fprintf(stdout, "not an id: %q\n%s\n", raw, unbindBranchUsage)
			return 2
		}
		ids = append(ids, id)
	}

	s, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return 1
	}
	defer s.Close()

	if len(ids) == 0 {
		bound, err := s.BranchBoundKnowledge()
		if err != nil {
			fmt.Fprintf(stdout, "query: %v\n", err)
			return 1
		}
		if len(bound) == 0 {
			fmt.Fprintln(stdout, "no active knowledge is filed against a branch")
			return 0
		}
		fmt.Fprintf(stdout, "%d active entries are filed against a branch:\n", len(bound))
		for _, k := range bound {
			fmt.Fprintf(stdout, "  #%-4d %-8s %s@%s  %s\n", k.ID, k.Type, k.Scope.Project, k.Scope.Branch, k.Title)
		}
		fmt.Fprintf(stdout, "\nlift them with: ctx unbind-branch --db %s %s\n", *dbPath, idList(bound))
		return 0
	}

	affected, err := s.UnbindBranchScope(ids, *dryRun)
	if err != nil {
		fmt.Fprintf(stdout, "unbind: %v\n", err)
		return 1
	}
	verb := "lifted"
	if *dryRun {
		verb = "would lift"
	}
	for _, k := range affected {
		fmt.Fprintf(stdout, "%s #%d from %s@%s to %s\n", verb, k.ID, k.Scope.Project, k.Scope.Branch, k.Scope.Project)
	}
	// Naming an id that carries no branch is not an error, but silence about it
	// would read as success on an entry that was never touched.
	if missing := len(ids) - len(affected); missing > 0 {
		fmt.Fprintf(stdout, "%d of the named ids carry no branch and were skipped\n", missing)
	}
	return 0
}

func idList(ks []store.Knowledge) string {
	parts := make([]string, len(ks))
	for i, k := range ks {
		parts[i] = strconv.FormatInt(k.ID, 10)
	}
	return strings.Join(parts, " ")
}
