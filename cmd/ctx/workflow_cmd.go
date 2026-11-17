package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const workflowUsage = `usage: ctx workflow trigger --db <path> --project <remote> --workflow <name> --on <event>
         [--branch <name>|*] [--every <minutes>] [--chain] [--off]

Switches a workflow on (or with --off off) for a kind of event in a project,
directly on the database of the server; only someone with the host can do it.
Events: pr.merged, idea.created, schedule (idea.discussion_closed follows with stage E3).
--branch narrows pr.merged (empty: the default branch, *: every branch),
--every is the period of a schedule in minutes, --chain lets events that a
run of this workflow causes start it again (two stages at most).
The built-in workflow ghosttree/echo needs no model and echoes its event.
The built-in workflow ghosttree/ask makes one model call through the model
proxy: the model named in the event data ("model"), else the alias klein.`

// cmdWorkflow is the operator's way to switch workflows on until workflows
// get their own pages (REQ-693, stage E2).
func cmdWorkflow(args []string, stdout io.Writer) int {
	if len(args) == 0 || args[0] != "trigger" {
		fmt.Fprintln(stdout, workflowUsage)
		return 2
	}
	fs := flag.NewFlagSet("workflow trigger", flag.ContinueOnError)
	fs.SetOutput(stdout)
	db := fs.String("db", "", "database of the server")
	project := fs.String("project", "", "project (remote)")
	name := fs.String("workflow", "", "workflow, for example ghosttree/echo")
	on := fs.String("on", "", "kind of event")
	branch := fs.String("branch", "", "base branch of pr.merged; empty is the default branch, * every branch")
	every := fs.Int("every", 0, "period of a schedule in minutes")
	chain := fs.Bool("chain", false, "let events of its own runs start it again")
	off := fs.Bool("off", false, "switch it off")
	if err := fs.Parse(args[1:]); err != nil || *db == "" || *project == "" || *name == "" || *on == "" {
		fmt.Fprintln(stdout, workflowUsage)
		return 2
	}
	st, err := store.Open(*db)
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return 1
	}
	defer st.Close()
	t, err := st.SetWorkflowTriggerAsOperator(store.WorkflowTrigger{Project: *project, Workflow: *name, EventKind: *on, Branch: *branch,
		EveryMinutes: *every, AllowChain: *chain, Enabled: !*off})
	if err != nil {
		fmt.Fprintf(stdout, "workflow trigger: %v\n", err)
		return 1
	}
	state := "on"
	if !t.Enabled {
		state = "off"
	}
	fmt.Fprintf(stdout, "%s %s for %s in %s (trigger %d)\n", t.Workflow, state, t.EventKind, t.Project, t.ID)
	return 0
}
