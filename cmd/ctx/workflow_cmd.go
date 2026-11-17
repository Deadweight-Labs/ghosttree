package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/workflow"
)

const workflowUsage = `usage: ctx workflow trigger --db <path> --project <remote> --workflow <name> --on <event>
         [--branch <name>|*] [--every <minutes>] [--chain] [--off]
       ctx workflow check <file> [--project <remote>] [--offline]

Switches a workflow on (or with --off off) for a kind of event in a project,
directly on the database of the server; only someone with the host can do it.
Events: pr.merged, idea.created, idea.discussion_closed, schedule.
--branch narrows pr.merged (empty: the default branch, *: every branch),
--every is the period of a schedule in minutes, --chain lets events that a
run of this workflow causes start it again (two stages at most).
The built-in workflow ghosttree/echo needs no model and echoes its event.
The built-in workflow ghosttree/ask makes one model call through the model
proxy: the model named in the event data ("model"), else the alias klein.

check reads a workflow file and reports unknown keys, kinds of result and
model aliases as errors, then what the workflow would read, write and cost at
most. It never calls a model. With a server it checks the aliases and caps of
the project (default: the project of this directory); --offline checks only
the file and the catalogue of blocks. The exit code is 1 when there are errors.`

// cmdWorkflow is the operator's way to switch workflows on until workflows
// get their own pages (REQ-693, stage E2).
func cmdWorkflow(args []string, stdout io.Writer) int {
	if len(args) > 0 && args[0] == "check" {
		return workflowCheck(args[1:], stdout)
	}
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

// workflowCheck is ctx workflow check (REQ-695). The file is read here; the
// server only sees its text, and only when a project is known and --offline
// is not given.
func workflowCheck(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("workflow check", flag.ContinueOnError)
	fs.SetOutput(stdout)
	project := fs.String("project", "", "project whose aliases and caps apply (default: the project of this directory)")
	offline := fs.Bool("offline", false, "check without the server: the file and the catalogue of blocks only")
	var file string
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		file, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if file == "" && fs.NArg() == 1 {
		file = fs.Arg(0)
	}
	if file == "" {
		fmt.Fprintln(stdout, workflowUsage)
		return 2
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	var rep workflow.CheckReport
	server := false
	if !*offline {
		if cfg, err := config.Load(); err == nil && cfg.ServerURL != "" {
			p := *project
			if p == "" {
				p = currentAxes(cfg.Machine).Project
			}
			if p != "" {
				rep, err = client.New(cfg).CheckWorkflow(p, string(raw))
				if err != nil {
					fmt.Fprintf(stdout, "server: %v\n", err)
					return 1
				}
				server = true
			}
		}
	}
	if !server {
		_, rep = workflow.Check(string(raw), workflow.Env{})
	}
	fmt.Fprint(stdout, workflow.FormatReport(rep))
	if !rep.OK() {
		return 1
	}
	return 0
}
