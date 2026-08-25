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

const regressionUsage = `usage: ctx regression [--project <remote>]
       ctx regression covered <id> <test>
       ctx regression uncovered <id>
       ctx regression not-applicable <id>
       ctx regression unreviewed [--project <remote>] [--limit 20]

ghosttree is not what keeps a fixed bug from coming back — a regression test is.
What it can do is say which test takes that job, and where none does.

Called without an action it lists the fixes nothing guards, and says how many
pitfalls nobody has judged yet. That second number matters: without it a short
list reads as all-clear.

  covered         name the test that would catch the defect's return
  uncovered       there is no such test, and there could be — this is the gap
  not-applicable  nothing to test here, decided on purpose. A pitfall about a
                  tool's behaviour is not a regression candidate, and leaving it
                  blank would make that decision look like an open task.
  unreviewed      list the pitfalls nobody has judged yet, to work through them`

func cmdRegression(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("regression", flag.ContinueOnError)
	fs.SetOutput(stdout)
	projectFlag := fs.String("project", "", "confine to one project")
	limitFlag := fs.Int("limit", 20, "maximum entries to list")
	fs.Usage = func() { fmt.Fprintln(stdout, regressionUsage) }

	var action string
	rest := make([]string, 0, len(args))
	for _, a := range args {
		switch {
		case action == "" && (a == "covered" || a == "uncovered" || a == "not-applicable" || a == "unreviewed"):
			action = a
		default:
			rest = append(rest, a)
		}
	}
	if fs.Parse(rest) != nil {
		return 2
	}
	positional := fs.Args()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "no config (%s) - run 'ctx setup'\n", config.Path())
		return 1
	}
	c := client.New(cfg)
	ax := scope.Axes{Project: scope.NormalizeRemote(*projectFlag)}

	switch action {
	case "":
		return listRegressionGaps(c, ax, stdout)
	case "unreviewed":
		return listUnjudgedPitfalls(c, ax, *limitFlag, stdout)
	}

	if len(positional) == 0 {
		fmt.Fprintln(stdout, regressionUsage)
		return 2
	}
	id, err := strconv.ParseInt(positional[0], 10, 64)
	if err != nil {
		fmt.Fprintf(stdout, "not an id: %q\n%s\n", positional[0], regressionUsage)
		return 2
	}
	state := strings.ReplaceAll(action, "-", "_")
	test := strings.Join(positional[1:], " ")
	if state == "covered" && test == "" {
		fmt.Fprintf(stdout, "name the test that covers #%d\n%s\n", id, regressionUsage)
		return 2
	}
	if err := c.SetRegressionCover(id, state, test); err != nil {
		fmt.Fprintf(stdout, "set cover: %v\n", err)
		return 1
	}
	if test != "" {
		fmt.Fprintf(stdout, "#%d covered by %s\n", id, test)
	} else {
		fmt.Fprintf(stdout, "#%d %s\n", id, state)
	}
	return 0
}

func listRegressionGaps(c *client.Client, ax scope.Axes, stdout io.Writer) int {
	gaps, unreviewed, err := c.RegressionGaps(ax)
	if err != nil {
		fmt.Fprintf(stdout, "query: %v\n", err)
		return 1
	}
	for _, k := range gaps {
		fmt.Fprintf(stdout, "#%-5d %s\n", k.ID, k.Title)
	}
	if len(gaps) == 0 {
		fmt.Fprintln(stdout, "no fix is recorded as unguarded")
	}
	// Immer mitgesagt, auch bei null: eine leere Lückenliste neben hundert
	// unbeurteilten Einträgen ist keine Entwarnung, sondern eine ungestellte
	// Frage.
	fmt.Fprintf(stdout, "\n%d gaps, %d pitfalls nobody has judged yet\n", len(gaps), unreviewed)
	return 0
}

func listUnjudgedPitfalls(c *client.Client, ax scope.Axes, limit int, stdout io.Writer) int {
	ks, err := c.Knowledge(ax)
	if err != nil {
		fmt.Fprintf(stdout, "query: %v\n", err)
		return 1
	}
	shown := 0
	for _, k := range ks {
		if k.Type != "pitfall" || k.RegressionState != "" {
			continue
		}
		if shown == limit {
			fmt.Fprintf(stdout, "... more than %d, raise --limit\n", limit)
			break
		}
		fmt.Fprintf(stdout, "#%-5d %s\n", k.ID, k.Title)
		shown++
	}
	if shown == 0 {
		fmt.Fprintln(stdout, "every pitfall has been judged")
	}
	return 0
}
