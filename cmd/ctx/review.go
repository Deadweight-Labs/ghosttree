package main

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
)

const reviewUsage = `usage: ctx review [--project <remote>] [--limit <n>]
       ctx review approve|reject <id>...

  ctx review                list knowledge awaiting a decision
  ctx review approve <id>   mark it verified, so it reads as confirmed
  ctx review reject <id>    deprecate it, so it stops being served

--project narrows the queue to one repository. A distiller run produces
several hundred findings, and judging them is easier one repository at a time
than in a stream that jumps between them.

--all releases the whole queue at once. It records the entries as trusted
rather than verified, because releasing a batch and judging a finding are
different statements and must not leave the same mark.`

// allReviewLimit bounds one --all pass. It is far above any realistic queue and
// exists so a runaway backlog cannot be released in a single unattended call.
const allReviewLimit = 1000

func cmdReview(args []string, stdout io.Writer) int {
	// Arguments are validated before the config is loaded, so a typo reads as
	// a usage error rather than as a missing configuration.
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(stdout)
	projectFlag := fs.String("project", "", "only list findings of one project")
	limitFlag := fs.Int("limit", 50, "maximum entries to list")
	allFlag := fs.Bool("all", false, "with approve or reject: act on the whole queue instead of named ids")
	// The flags only apply to listing; approve and reject take bare ids, so
	// The action is a bare word and the ids are bare numbers, so the flags can
	// sit anywhere around them. Taking the action out first is what lets
	// `review approve --all` and `review --all approve` both work; leaving it in
	// would end flag parsing at the first non-flag argument.
	var action string
	rest := make([]string, 0, len(args))
	for _, a := range args {
		if action == "" && !strings.HasPrefix(a, "-") && (a == "approve" || a == "reject") {
			action = a
			continue
		}
		rest = append(rest, a)
	}
	if fs.Parse(rest) != nil {
		return 2
	}
	args = fs.Args()
	project, limit, all := *projectFlag, *limitFlag, *allFlag

	// Whatever is left is ids. A bare word here is a misspelled action, and
	// saying so beats listing the queue as if nothing had been asked for.
	var ids []int64
	for _, raw := range args {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			fmt.Fprintf(stdout, "not an id: %q\n%s\n", raw, reviewUsage)
			return 2
		}
		ids = append(ids, id)
	}
	if action == "" && len(ids) > 0 {
		fmt.Fprintln(stdout, reviewUsage)
		return 2
	}
	if action != "" && len(ids) == 0 && !all {
		fmt.Fprintln(stdout, reviewUsage)
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "no config (%s) - run 'ctx setup'\n", config.Path())
		return 1
	}
	c := client.New(cfg)

	if action == "" {
		return listPending(c, project, limit, stdout)
	}
	// Releasing a whole queue and judging one finding are different statements,
	// so they must not produce the same confidence. A batch says "this material
	// may be delivered"; naming an id says "I read this and it is right". If
	// both wrote `verified`, the tier would mean whichever was used last — which
	// is what happened on 2026-08-24, when a batch of 136 outranked every
	// hand-written entry in the archive.
	confidence := "verified"
	if all {
		confidence = "trusted"
		queued, err := c.Pending(project, allReviewLimit)
		if err != nil {
			fmt.Fprintf(stdout, "cannot read pending knowledge: %v\n", err)
			return 1
		}
		if len(ids) > 0 {
			fmt.Fprintln(stdout, "--all takes no ids: it acts on the whole queue")
			return 2
		}
		for _, p := range queued {
			ids = append(ids, p.Knowledge.ID)
		}
		if len(ids) == 0 {
			fmt.Fprintln(stdout, "nothing awaiting review")
			return 0
		}
		scope := "every project"
		if project != "" {
			scope = project
		}
		verb := "approving"
		if action == "reject" {
			verb = "rejecting"
		}
		fmt.Fprintf(stdout, "%s %d entries of %s as %s\n", verb, len(ids), scope, confidence)
	}
	patch := map[string]string{"confidence": confidence, "status": "active"}
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

func listPending(c *client.Client, project string, limit int, stdout io.Writer) int {
	pending, err := c.Pending(project, limit)
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
	// The claim, not just its source. Deciding whether a quote supports a
	// finding is impossible while only one of the two is on screen.
	for line := range strings.SplitSeq(strings.TrimSpace(k.Body), "\n") {
		fmt.Fprintf(stdout, "       %s\n", line)
	}
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
