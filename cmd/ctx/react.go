package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const reactUsage = `usage: ctx react <REQ-n|IDEA-n|comment:n> [<reaction>] [--remove] [--json]

  ctx react REQ-12                 show the reactions on an issue
  ctx react comment:7 +1           set your reaction on a comment
  ctx react IDEA-3 heart --remove  take it back

Reactions: +1, -1, laugh, hooray, confused, heart, rocket, eyes (or the emoji).
Setting one twice changes nothing. Members react; guests only read.`

func cmdReact(args []string, stdout io.Writer) int {
	args, jsonMode := removeBoolArg(args, "--json")
	args, remove := removeBoolArg(args, "--remove")
	if len(args) < 1 || len(args) > 2 || (remove && len(args) != 2) {
		fmt.Fprintln(stdout, reactUsage)
		return 2
	}
	kind, id, err := client.ReactionTarget(args[0])
	if err != nil {
		fmt.Fprintln(stdout, reactUsage)
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	c := anchoredClient(cfg)
	ref := strings.ToUpper(strings.TrimSpace(args[0]))
	var summary store.ReactionSummary
	switch {
	case len(args) == 1:
		list, err := c.Reactions(kind, id)
		if err != nil {
			return writeRequestCLIError(stdout, jsonMode, 1, err)
		}
		if jsonMode {
			return writeRequestCLIResult(stdout, true, list, nil, nil)
		}
		fmt.Fprintln(stdout, client.ReactionLine(ref, list.ReactionSummary))
		return 0
	case remove:
		summary, err = c.Unreact(kind, id, args[1])
	default:
		summary, err = c.React(kind, id, args[1])
	}
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	if jsonMode {
		return writeRequestCLIResult(stdout, true, summary, nil, nil)
	}
	fmt.Fprintln(stdout, client.ReactionLine(ref, summary))
	return 0
}
