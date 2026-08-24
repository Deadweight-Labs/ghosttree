package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
)

const reviewUsage = `usage: ctx review [approve|reject] [<id>...]

  ctx review                list knowledge awaiting a decision
  ctx review approve <id>   mark it verified, so it reads as confirmed
  ctx review reject <id>    deprecate it, so it stops being served`

func cmdReview(args []string, stdout io.Writer) int {
	// Arguments are validated before the config is loaded, so a typo reads as
	// a usage error rather than as a missing configuration.
	var action string
	var ids []int64
	if len(args) > 0 {
		action = args[0]
		if action != "approve" && action != "reject" {
			fmt.Fprintln(stdout, reviewUsage)
			return 2
		}
		if len(args) == 1 {
			fmt.Fprintln(stdout, reviewUsage)
			return 2
		}
		for _, raw := range args[1:] {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				fmt.Fprintf(stdout, "not an id: %q\n%s\n", raw, reviewUsage)
				return 2
			}
			ids = append(ids, id)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "no config (%s) - run 'ctx setup'\n", config.Path())
		return 1
	}
	c := client.New(cfg)

	if action == "" {
		return listPending(c, stdout)
	}
	patch := map[string]string{"confidence": "verified", "status": "active"}
	if action == "reject" {
		patch = map[string]string{"status": "deprecated"}
	}
	for _, id := range ids {
		if err := c.PatchKnowledge(id, patch); err != nil {
			fmt.Fprintf(stdout, "#%d: %v\n", id, err)
			return 1
		}
		fmt.Fprintf(stdout, "#%d %sd\n", id, action)
	}
	return 0
}

func listPending(c *client.Client, stdout io.Writer) int {
	pending, err := c.Pending(50)
	if err != nil {
		fmt.Fprintf(stdout, "cannot read pending knowledge: %v\n", err)
		return 1
	}
	if len(pending) == 0 {
		fmt.Fprintln(stdout, "nothing awaiting review")
		return 0
	}
	for _, p := range pending {
		writePendingEntry(stdout, p)
	}
	fmt.Fprintf(stdout, "\n%d awaiting review - approve with 'ctx review approve <id>'\n", len(pending))
	return 0
}

func writePendingEntry(stdout io.Writer, p client.PendingEntry) {
	k := p.Knowledge
	fmt.Fprintf(stdout, "#%-4d %-11s %-8s seen in %d session(s)  %s\n",
		k.ID, k.Confidence, k.Type, p.Recurrence, k.Title)
	var scopeParts []string
	if k.Scope.Project != "" {
		scopeParts = append(scopeParts, "project="+k.Scope.Project)
	}
	if k.Scope.Branch != "" {
		scopeParts = append(scopeParts, "branch="+k.Scope.Branch)
	}
	if k.Scope.Machine != "" {
		scopeParts = append(scopeParts, "machine="+k.Scope.Machine)
	}
	if len(scopeParts) == 0 {
		scopeParts = append(scopeParts, "global")
	}
	fmt.Fprintf(stdout, "       scope: %s\n", strings.Join(scopeParts, ", "))
	if suffix := activationSuffix(k.Activation); suffix != "" {
		fmt.Fprintf(stdout, "       activation: %s\n", strings.TrimPrefix(suffix, "; "))
	}
	for _, e := range p.Evidence {
		fmt.Fprintf(stdout, "       evidence: session %d chunk %d: %s\n", e.SessionID, e.ChunkSeq, e.Quote)
	}
	if e := p.MigrationEvidence; e != nil {
		fmt.Fprintf(stdout, "       migration: %s sha256:%s (run %d, item %s)\n", e.Source, e.Digest, e.RunID, e.ItemKey)
		if e.Quote != "" {
			fmt.Fprintf(stdout, "       source quote: %s\n", e.Quote)
		}
	}
}
