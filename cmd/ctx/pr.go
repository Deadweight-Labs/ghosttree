package main

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const prUsage = `usage: ctx pr <command> [--project P] [--json]

  list [--state open|merged|closed|all] [--branch B] [--author LOGIN] [--limit N] [--offset N]
                      the pull requests GitHub reported for the project, newest change first
  show <number>       one pull request: state, commits, package, feature checklist, what was said about it
  commits <number>    the commits of a pull request, in the order of the pull request

The project is the current one unless --project names another you can read.`

// readProjectArg ist das Projekt für einen lesenden Aufruf: ein ausdrücklich
// genanntes Remote gilt, wie es ist (der Server prüft das Leserecht); ein
// Kurzname wird über die schreibbaren Projekte aufgelöst; ohne Angabe das
// aktuelle.
func readProjectArg(c *client.Client, cfg config.Config, explicit string) (string, error) {
	explicit = strings.TrimSpace(explicit)
	if strings.Contains(explicit, "/") {
		return scope.NormalizeRemote(explicit), nil
	}
	return c.WriteTarget(explicit, requestProjectAxes(cfg.Machine).Project)
}

func cmdPR(args []string, stdout io.Writer) int {
	args, jsonMode := removeBoolArg(args, "--json")
	if len(args) == 0 {
		fmt.Fprintln(stdout, prUsage)
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("pr "+sub, flag.ContinueOnError)
	fs.SetOutput(stdout)
	projectFlag := fs.String("project", "", "project (remote or unique short name); default: the current one")
	state := fs.String("state", "", "open, merged, closed or all")
	branch := fs.String("branch", "", "only pull requests with this head branch")
	author := fs.String("author", "", "only pull requests of this GitHub login")
	limit := fs.Int("limit", 0, "page size, at most 100")
	offset := fs.Int("offset", 0, "entries to skip")
	// Flags dürfen vor und nach der Nummer stehen.
	var pos []string
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		rest = fs.Args()
		if len(rest) > 0 {
			pos = append(pos, rest[0])
			rest = rest[1:]
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	c := anchoredClient(cfg)
	project, err := readProjectArg(c, cfg, *projectFlag)
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	number := func() (int64, bool) {
		if len(pos) != 1 {
			return 0, false
		}
		n, err := strconv.ParseInt(strings.TrimPrefix(pos[0], "#"), 10, 64)
		return n, err == nil && n > 0
	}
	switch sub {
	case "list":
		out, err := c.PullList(project, *state, *branch, *author, *limit, *offset)
		if err == nil && !jsonMode {
			fmt.Fprintf(stdout, "%d pull requests (open %d, merged %d, closed %d)\n", out.Total, out.Counts.Open, out.Counts.Merged, out.Counts.Closed)
			for _, p := range out.Items {
				fmt.Fprintf(stdout, "#%-5d %-7s %s  (%s, %d commits)\n", p.Number, p.State, p.Title, p.Head, p.Commits)
			}
			return 0
		}
		return writeRequestCLIResult(stdout, jsonMode, out, nil, err)
	case "show":
		n, ok := number()
		if !ok {
			fmt.Fprintln(stdout, prUsage)
			return 2
		}
		d, err := c.PullGet(project, n)
		if err == nil && !jsonMode {
			fmt.Fprintf(stdout, "PR #%d [%s] %s\n%s → %s, %d commits\n", d.Number, d.State, d.Title, d.Head, d.Base, d.Commits)
			ck := d.Checklist
			if ck.HasPackage {
				fmt.Fprintf(stdout, "package: %d of %d required requests open, %d human tasks, %d uncovered\n", ck.RequiredOpen, ck.RequiredTotal, ck.HumanTasks, ck.Uncovered)
			}
			for _, f := range d.Features {
				fmt.Fprintf(stdout, "  [%s] %s\n", f.State, f.Text)
			}
			fmt.Fprintf(stdout, "said about it: %d knowledge entries, %d ghost paths\n", len(d.Brief.Knowledge), len(d.Brief.Ghosts))
			cliCard(stdout, d.Card)
			return 0
		}
		return writeRequestCLIResultCard(stdout, jsonMode, d, &d.Card, err)
	case "commits":
		n, ok := number()
		if !ok {
			fmt.Fprintln(stdout, prUsage)
			return 2
		}
		out, err := c.PullCommits(project, n)
		if err == nil && !jsonMode {
			for _, k := range out.Items {
				fmt.Fprintf(stdout, "%s  %s  %s  (%d entries)\n", k.Short, k.Author, k.Title, k.Entries)
			}
			return 0
		}
		return writeRequestCLIResult(stdout, jsonMode, out, nil, err)
	}
	fmt.Fprintln(stdout, prUsage)
	return 2
}
