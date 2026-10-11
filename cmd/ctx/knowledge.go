package main

import (
	"flag"
	"fmt"
	"io"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
)

const knowledgeUsage = `usage: ctx knowledge move <id> --project P | --machine M | --global

Moves a knowledge entry that was filed in the wrong place. You need write access
to where it is and to where it goes; the move is recorded.

  --project P   a remote (github.com/owner/repo) or a short name that is unique
                among the projects you can write to
  --machine M   any machine name, including one no agent ever ran on
  --global      applies everywhere`

func cmdKnowledge(args []string, stdout io.Writer) int {
	if len(args) == 0 || args[0] != "move" {
		fmt.Fprintln(stdout, knowledgeUsage)
		return 2
	}
	return cmdKnowledgeMove(args[1:], stdout)
}

func cmdKnowledgeMove(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, knowledgeUsage)
		return 2
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintln(stdout, knowledgeUsage)
		return 2
	}
	fs := flag.NewFlagSet("knowledge move", flag.ContinueOnError)
	fs.SetOutput(stdout)
	project := fs.String("project", "", "move to this project")
	machine := fs.String("machine", "", "move to this machine")
	global := fs.Bool("global", false, "move to global")
	if fs.Parse(args[1:]) != nil {
		return 2
	}
	chosen := 0
	for _, set := range []bool{*project != "", *machine != "", *global} {
		if set {
			chosen++
		}
	}
	if chosen != 1 {
		fmt.Fprintln(stdout, "name exactly one target: --project, --machine or --global")
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "no config (%s) - run 'ctx setup'\n", config.Path())
		return 1
	}
	return moveKnowledge(client.New(cfg), id, *project, *machine, *global, stdout)
}

func moveKnowledge(c *client.Client, id int64, project, machine string, global bool, stdout io.Writer) int {
	if project != "" {
		var err error
		if project, err = c.WriteTarget(project, ""); err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
	}
	saved, err := c.MoveKnowledge(id, project, machine, global)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	fmt.Fprintf(stdout, "moved #%d to %s\n", saved.ID, saved.Scope.Label())
	return 0
}
