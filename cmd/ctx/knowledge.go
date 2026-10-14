package main

import (
	"errors"
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
       ctx knowledge supersede <id> --old ID[,ID...] --reason R
       ctx knowledge retire <id> --reason R
       ctx knowledge show <id>

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

supersede  Marks the existing entry <id> as the replacement of older entries,
        when you notice only afterwards that it makes them obsolete.

retire  Retires an entry whose subject no longer exists, with a required reason.
        It needs the same right as editing the entry in the web.

show    Prints one entry in full, with the reason if it was superseded or retired.

Run 'ctx knowledge <command> --help' for the options of one command.`

const supersedeUsage = `usage: ctx knowledge supersede <id> --old ID[,ID...] --reason R

Marks the existing entry <id> as the replacement of the older entries. They stop
appearing in search and the briefing and stay readable through their history.
You need the right to edit <id> and every older entry, and all of them must have
exactly the same scope.

  --old ID[,ID...]  ids of the older entries <id> replaces (required)
  --reason R        what changed (required)`

const retireUsage = `usage: ctx knowledge retire <id> --reason R

Retires an entry whose subject no longer exists and that has no replacement to
write. It stops appearing in search and the briefing and stays readable through
its history. It needs the same right as editing the entry in the web. If the
state changed rather than vanished, write the new state with 'ctx knowledge add
--supersedes' or use 'ctx knowledge supersede'.

  --reason R  why the entry is no longer true or needed (required)`

const showUsage = `usage: ctx knowledge show <id>

Prints one entry in full: title, type, scope and status, the reason if it was
superseded or retired, and the body.`

// helpRequested says whether the arguments ask for a command's own help.
func helpRequested(args []string) bool {
	return len(args) > 0 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help")
}

func cmdKnowledge(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, knowledgeUsage)
		return 2
	}
	if helpRequested(args) {
		fmt.Fprintln(stdout, knowledgeUsage)
		return 0
	}
	switch args[0] {
	case "move":
		return cmdKnowledgeMove(args[1:], stdout)
	case "add":
		return cmdKnowledgeAdd(args[1:], stdout)
	case "retire":
		return cmdKnowledgeRetire(args[1:], stdout)
	case "supersede":
		return cmdKnowledgeSupersede(args[1:], stdout)
	case "show":
		return cmdKnowledgeShow(args[1:], stdout)
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
	ids, err := parseIDList("--supersedes", *supersedes)
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

func parseIDList(flagName, s string) ([]int64, error) {
	var ids []int64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimPrefix(strings.TrimSpace(part), "#")
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%s takes entry ids like 12,15, got %q", flagName, part)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// parseKnowledgeID reads the leading <id> of a subcommand. ok is false when the
// command is done; code is then its exit code.
func parseKnowledgeID(args []string, usage string, stdout io.Writer) (id int64, code int, ok bool) {
	if helpRequested(args) {
		fmt.Fprintln(stdout, usage)
		return 0, 0, false
	}
	if len(args) > 0 {
		var err error
		if id, err = strconv.ParseInt(strings.TrimPrefix(args[0], "#"), 10, 64); err == nil && id > 0 {
			return id, 0, true
		}
	}
	fmt.Fprintln(stdout, usage)
	return 0, 2, false
}

// parseKnowledgeFlags parses the flags after <id>; ok is false when the
// command is done (help asked, or the flags were wrong).
func parseKnowledgeFlags(fs *flag.FlagSet, usage string, args []string, stdout io.Writer) (code int, ok bool) {
	fs.SetOutput(stdout)
	fs.Usage = func() { fmt.Fprintln(stdout, usage) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, false
		}
		return 2, false
	}
	return 0, true
}

func cmdKnowledgeRetire(args []string, stdout io.Writer) int {
	id, code, ok := parseKnowledgeID(args, retireUsage, stdout)
	if !ok {
		return code
	}
	fs := flag.NewFlagSet("knowledge retire", flag.ContinueOnError)
	reason := fs.String("reason", "", "why the entry is no longer true or needed")
	if code, ok := parseKnowledgeFlags(fs, retireUsage, args[1:], stdout); !ok {
		return code
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

func cmdKnowledgeSupersede(args []string, stdout io.Writer) int {
	id, code, ok := parseKnowledgeID(args, supersedeUsage, stdout)
	if !ok {
		return code
	}
	fs := flag.NewFlagSet("knowledge supersede", flag.ContinueOnError)
	old := fs.String("old", "", "comma-separated ids of the older entries this one replaces")
	reason := fs.String("reason", "", "what changed")
	if code, ok := parseKnowledgeFlags(fs, supersedeUsage, args[1:], stdout); !ok {
		return code
	}
	ids, err := parseIDList("--old", *old)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}
	if len(ids) == 0 {
		fmt.Fprintln(stdout, "--old is required: name the entries to supersede")
		return 2
	}
	if strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(stdout, "--reason is required: say what changed")
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "no config (%s) - run 'ctx setup'\n", config.Path())
		return 1
	}
	saved, err := client.New(cfg).SupersedeKnowledge(id, ids, *reason, "cli")
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	parts := make([]string, len(ids))
	for i, older := range ids {
		parts[i] = "#" + strconv.FormatInt(older, 10)
	}
	fmt.Fprintf(stdout, "#%d now supersedes %s\n", saved.ID, strings.Join(parts, ", "))
	return 0
}

func cmdKnowledgeShow(args []string, stdout io.Writer) int {
	id, code, ok := parseKnowledgeID(args, showUsage, stdout)
	if !ok {
		return code
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "no config (%s) - run 'ctx setup'\n", config.Path())
		return 1
	}
	k, err := client.New(cfg).KnowledgeByID(id)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	fmt.Fprintf(stdout, "#%d %s\n[%s|%s|%s]\n", k.ID, k.Title, k.Type, k.Scope.Label(), k.Status)
	if k.Closure != nil {
		fmt.Fprintln(stdout, k.Closure.Summary())
	}
	fmt.Fprintf(stdout, "\n%s\n", k.Body)
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
