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
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const knowledgeUsage = `usage: ctx knowledge move <id> --project P | --machine M | --global
       ctx knowledge add --type T --title X --body B --scope S [--supersedes ID[,ID...] --reason R]
       ctx knowledge retire <id> --reason R

move    Moves a knowledge entry that was filed in the wrong place. You need write
        access to where it is and to where it goes; the move is recorded.

  --project P   a remote (github.com/owner/repo) or a short name that is unique
                among the projects you can write to
  --machine M   any machine name, including one no agent ever ran on
  --global      applies everywhere

add     Writes a pitfall, decision, note or plan. Search first. When a known
        state changed, pass --supersedes with the ids of the older entries and
        --reason saying what changed: they stop appearing in search and the
        briefing and stay readable through their history. Do not write a
        follow-up that leaves both active.

  --scope S     project, branch, machine or global (required)
  --project P   with --scope project: another project than the current one

retire  Retires an entry whose subject no longer exists, with a required reason.
        It needs the same right as editing the entry in the web.`

func cmdKnowledge(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, knowledgeUsage)
		return 2
	}
	switch args[0] {
	case "move":
		return cmdKnowledgeMove(args[1:], stdout)
	case "add":
		return cmdKnowledgeAdd(args[1:], stdout)
	case "retire":
		return cmdKnowledgeRetire(args[1:], stdout)
	}
	fmt.Fprintln(stdout, knowledgeUsage)
	return 2
}

func cmdKnowledgeAdd(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("knowledge add", flag.ContinueOnError)
	fs.SetOutput(stdout)
	typ := fs.String("type", "", "pitfall, decision, note or plan")
	title := fs.String("title", "", "one line summary")
	body := fs.String("body", "", "the knowledge itself")
	scopeFlag := fs.String("scope", "", "project, branch, machine or global")
	project := fs.String("project", "", "with --scope project: another project")
	supersedes := fs.String("supersedes", "", "comma-separated ids of older entries this one replaces")
	reason := fs.String("reason", "", "with --supersedes: what changed")
	if fs.Parse(args) != nil {
		return 2
	}
	if *typ == "" || *title == "" || *scopeFlag == "" {
		fmt.Fprintln(stdout, "--type, --title and --scope are required")
		return 2
	}
	ids, err := parseIDList(*supersedes)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}
	if len(ids) > 0 && strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(stdout, "--reason is required with --supersedes: say what changed")
		return 2
	}
	if len(ids) == 0 && *reason != "" {
		fmt.Fprintln(stdout, "--reason only goes with --supersedes")
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "no config (%s) - run 'ctx setup'\n", config.Path())
		return 1
	}
	return addKnowledge(client.New(cfg), currentAxes(cfg.Machine),
		store.Knowledge{Type: *typ, Title: *title, Body: *body, Harness: "cli"},
		*scopeFlag, *project, ids, *reason, stdout)
}

func addKnowledge(c *client.Client, here scope.Axes, k store.Knowledge, scopeHint, project string, supersedes []int64, reason string, stdout io.Writer) int {
	autoCtx := here
	switch scopeHint {
	case "project", "branch":
		if scopeHint == "branch" && (here.Branch == "" || project != "") {
			fmt.Fprintln(stdout, "--scope branch needs a named branch of the current project; use project otherwise")
			return 2
		}
		target, err := c.WriteTarget(project, here.Project)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		k.Scope = scope.Axes{Project: target}
		if scopeHint == "branch" {
			k.Scope.Branch = here.Branch
		}
	case "machine":
		if here.Machine == "" {
			fmt.Fprintln(stdout, "this machine has no name configured")
			return 1
		}
		k.Scope = scope.Axes{Machine: here.Machine}
	case "global":
		k.Scope, autoCtx = scope.Axes{}, scope.Axes{}
	default:
		fmt.Fprintf(stdout, "unknown --scope %q; use project, branch, machine or global\n", scopeHint)
		return 2
	}
	saved, err := c.RememberSuperseding(k, autoCtx, supersedes, reason)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	msg := fmt.Sprintf("stored #%d [%s|%s]", saved.ID, saved.Type, saved.Scope.Label())
	if len(supersedes) > 0 {
		parts := make([]string, len(supersedes))
		for i, id := range supersedes {
			parts[i] = "#" + strconv.FormatInt(id, 10)
		}
		msg += "; superseded " + strings.Join(parts, ", ")
	}
	fmt.Fprintln(stdout, msg)
	return 0
}

func parseIDList(s string) ([]int64, error) {
	var ids []int64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimPrefix(strings.TrimSpace(part), "#")
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("--supersedes takes entry ids like 12,15, got %q", part)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func cmdKnowledgeRetire(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, knowledgeUsage)
		return 2
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(args[0], "#"), 10, 64)
	if err != nil {
		fmt.Fprintln(stdout, knowledgeUsage)
		return 2
	}
	fs := flag.NewFlagSet("knowledge retire", flag.ContinueOnError)
	fs.SetOutput(stdout)
	reason := fs.String("reason", "", "why the entry is no longer true or needed")
	if fs.Parse(args[1:]) != nil {
		return 2
	}
	if strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(stdout, "--reason is required: say why the entry is no longer true or needed")
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "no config (%s) - run 'ctx setup'\n", config.Path())
		return 1
	}
	saved, err := client.New(cfg).RetireKnowledge(id, *reason)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	fmt.Fprintf(stdout, "retired #%d\n", saved.ID)
	return 0
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
