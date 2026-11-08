package main

import (
	"flag"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/mcpserver"
	"github.com/Deadweight-Labs/ghosttree/internal/query"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var negatedQualifier = regexp.MustCompile(`^-[a-z]+:`)

const searchUsage = `usage: ctx search [--project P] [--kind K] [--limit N] [--json] <query>
       ctx search --preset NAME [--pr N] [--branch B] [--area PATH] [--since DATE] [--project P]
       ctx search --presets
       ctx search --save NAME [--scope user|project|org] [--description D] --line "<query>" [--line ...]
       ctx search --delete-preset ID

The query is words and qualifiers, like GitHub:
  is:open|closed|done|pain|idea|issue|knowledge|<state>  kind:  type:  repo:owner/name  branch:  commit:<7+ hex>
  pr:12  pr:10..14  package:N  req:N  task:N  pain:N  idea:N  parent:N  blocks:N  author:  for:  (@me is you)
  label:name  -label:name  area:<path prefix>  topic:<name>  created:>2026-11-01  updated:<7d  since:  until:
  -qualifier negates, a,b means a or b, "..." groups. An unknown qualifier is an error.

Saved searches take {pr} {branch} {area} {since}; the built-in ones are pr-ergebnis, branch-offen, ordner, seit, fuer-mich.`

func cmdSearch(args []string, stdout io.Writer) int {
	args, jsonMode := removeBoolArg(args, "--json")
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.SetOutput(stdout)
	project := fs.String("project", "", "project (remote or unique short name); default: the current one")
	kind := fs.String("kind", "all", "knowledge, requests, pains, ideas, criteria, tasks, files or all")
	limit := fs.Int("limit", 20, "hits per kind")
	preset := fs.String("preset", "", "run a saved search by name")
	presets := fs.Bool("presets", false, "list the saved searches")
	save := fs.String("save", "", "save a search under this name")
	scopeKind := fs.String("scope", "user", "with --save: user, project or org")
	description := fs.String("description", "", "with --save: what the search answers")
	deleteID := fs.Int64("delete-preset", 0, "delete the saved search with this id")
	pr := fs.String("pr", "", "with --preset: {pr}")
	branch := fs.String("branch", "", "with --preset: {branch}")
	area := fs.String("area", "", "with --preset: {area}")
	since := fs.String("since", "", "with --preset: {since}")
	var lines repeatedString
	fs.Var(&lines, "line", "with --save: a search line; repeatable")
	// Ein verneinter Qualifier (-label:bug) sieht aus wie ein Schalter; er ist ein
	// Wort der Suchzeile. Schalter haben nie einen Doppelpunkt.
	var negated []string
	var flagArgs []string
	for _, a := range args {
		if negatedQualifier.MatchString(a) {
			negated = append(negated, a)
		} else {
			flagArgs = append(flagArgs, a)
		}
	}
	positional, err := parseInterspersed(fs, flagArgs)
	if err != nil {
		fmt.Fprintln(stdout, searchUsage)
		return 2
	}
	positional = append(positional, negated...)
	cfg, err := config.Load()
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	c := client.New(cfg)
	target := requestProjectAxes(cfg.Machine).Project
	if *project != "" {
		if target, err = c.WriteTarget(*project, ""); err != nil {
			return writeRequestCLIError(stdout, jsonMode, 1, err)
		}
	}
	switch {
	case *presets:
		list, err := c.ListPresets(target)
		if err != nil || jsonMode {
			return writeRequestCLIResult(stdout, jsonMode, list, nil, err)
		}
		for _, p := range list {
			args := ""
			if len(p.Args) > 0 {
				args = " {" + strings.Join(p.Args, "} {") + "}"
			}
			id := ""
			if p.ID != 0 {
				id = fmt.Sprintf("  (id %d)", p.ID)
			}
			fmt.Fprintf(stdout, "%-14s %-8s%s%s\n", p.Name, p.Scope, args, id)
			if p.Description != "" {
				fmt.Fprintf(stdout, "    %s\n", p.Description)
			}
			for _, l := range p.Queries {
				fmt.Fprintf(stdout, "    %s\n", l)
			}
		}
		return 0
	case *save != "":
		p, err := c.SavePreset(target, *scopeKind, *save, *description, lines)
		if err != nil || jsonMode {
			return writeRequestCLIResult(stdout, jsonMode, p, nil, err)
		}
		fmt.Fprintf(stdout, "saved %s [%s]\n", p.Name, p.Scope)
		return 0
	case *deleteID != 0:
		if err := c.DeletePreset(*deleteID); err != nil {
			return writeRequestCLIError(stdout, jsonMode, 1, err)
		}
		fmt.Fprintf(stdout, "deleted %d\n", *deleteID)
		return 0
	case *preset != "":
		run := map[string]string{}
		for k, v := range map[string]string{"pr": *pr, "branch": *branch, "area": *area, "since": *since} {
			if v != "" {
				run[k] = v
			}
		}
		res, err := c.RunPreset(target, *preset, run, *limit)
		if err != nil || jsonMode {
			return writeRequestCLIResult(stdout, jsonMode, res, nil, err)
		}
		fmt.Fprintf(stdout, "search %s: %s\n", res.Preset.Name, strings.Join(res.Queries, " | "))
		fmt.Fprint(stdout, mcpserver.RenderQualifiedLinked(res.Result, cliBuilder(cfg, target)))
		return 0
	}
	line := strings.TrimSpace(strings.Join(positional, " "))
	if line == "" {
		fmt.Fprintln(stdout, searchUsage)
		return 2
	}
	// Dieselbe Prüfung wie am Server: ein Fehler in der Suchzeile steht vor dem Aufruf da.
	if _, err := query.Parse(line, time.Now()); err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	ax := requestProjectAxes(cfg.Machine)
	ax.Project = target
	res, err := c.SearchAnchored(line, *kind, ax, *limit, false, client.AnchorFilter{})
	if err != nil || jsonMode {
		return writeRequestCLIResult(stdout, jsonMode, res, nil, err)
	}
	if res.Query != "" {
		fmt.Fprintf(stdout, "query: %s\n", res.Query)
	}
	fmt.Fprint(stdout, mcpserver.RenderQualifiedLinked(res, cliBuilder(cfg, target)))
	return 0
}

const ideaUsage = `usage: ctx idea <command>

  add "title" [--body B] [--force] [--project P]      record a wish; a similar open idea is named unless --force
  list [--state open|accepted|dismissed|all] [--q text] [--project P]
  show <IDEA-n|n>
  promote <n> [--title T] [--description D] [--type feature|change|bug|investigation] [--criterion C ...]   make a request from it and link both
  link <n> --request REQ-n                             link a request that came out of the idea
  dismiss <n> --reason R
  reopen <n>

Worked-out ideas (raw text, summary, concept, discussion, decisions, images, issues; see also ctx decision):
  show <n> prints all of it` + ideaWorkshopUsage + `

All commands take --json.`

func parseIdeaRef(s string) (int64, bool) { return idea.ParseRef(s) }

func ideaLines(stdout io.Writer, i idea.Idea) {
	fmt.Fprintf(stdout, "%s [%s] %s\n", i.HumanID(), i.State, i.Title)
	if i.Reason != "" {
		fmt.Fprintf(stdout, "reason: %s\n", i.Reason)
	}
	if strings.TrimSpace(i.Body) != "" {
		fmt.Fprintf(stdout, "\n%s\n", i.Body)
	}
	for _, r := range i.Requests {
		fmt.Fprintf(stdout, "became REQ-%d\n", r)
	}
	if len(i.Labels) > 0 {
		var names []string
		for _, l := range i.Labels {
			names = append(names, l.Name)
		}
		fmt.Fprintf(stdout, "labels: %s\n", strings.Join(names, ", "))
	}
}

func cmdIdea(args []string, stdout io.Writer) int {
	args, jsonMode := removeBoolArg(args, "--json")
	usageErr := func() int { fmt.Fprintln(stdout, ideaUsage); return 2 }
	if len(args) == 0 {
		return usageErr()
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add", "list", "show", "promote", "link", "dismiss", "reopen":
	default:
		if !ideaWorkshopSubs[sub] {
			return usageErr()
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	c := client.New(cfg)
	if code, handled := cmdIdeaWorkshop(sub, rest, stdout, jsonMode, cfg, c); handled {
		return code
	}
	result := func(i idea.Idea, err error) int {
		card := cliBuilder(cfg, i.Project).IdeaCard(i)
		if err == nil && !jsonMode {
			ideaLines(stdout, i)
			cliCard(stdout, card)
			return 0
		}
		return writeRequestCLIResultCard(stdout, jsonMode, i, &card, err)
	}
	fs := flag.NewFlagSet("idea "+sub, flag.ContinueOnError)
	fs.SetOutput(stdout)
	body := fs.String("body", "", "one to three sentences")
	force := fs.Bool("force", false, "record it although a similar open idea exists")
	project := fs.String("project", "", "project (remote or unique short name); default: the current one")
	state := fs.String("state", "", "open, accepted, dismissed or all")
	q := fs.String("q", "", "words in title or text")
	title := fs.String("title", "", "request title (promote)")
	description := fs.String("description", "", "request description (promote)")
	reqType := fs.String("type", "", "request type (promote)")
	reason := fs.String("reason", "", "why (dismiss)")
	requestRef := fs.String("request", "", "REQ-n (link)")
	var criteria repeatedString
	fs.Var(&criteria, "criterion", "acceptance criterion of the new request (promote); repeatable")
	positional, err := parseInterspersed(fs, rest)
	if err != nil {
		return usageErr()
	}
	switch sub {
	case "add":
		text := strings.TrimSpace(strings.Join(positional, " "))
		if text == "" {
			return usageErr()
		}
		target, err := c.WriteTarget(*project, requestProjectAxes(cfg.Machine).Project)
		if err != nil {
			return writeRequestCLIError(stdout, jsonMode, 1, err)
		}
		return result(c.CreateIdea(target, text, *body, *force, cliSessionID(c, cfg)))
	case "list":
		target := requestProjectAxes(cfg.Machine).Project
		if *project != "" {
			if target, err = c.WriteTarget(*project, ""); err != nil {
				return writeRequestCLIError(stdout, jsonMode, 1, err)
			}
		}
		list, err := c.SearchIdeas(target, *state, *q, 200)
		if err != nil || jsonMode {
			return writeRequestCLIResult(stdout, jsonMode, list, nil, err)
		}
		if len(list) == 0 {
			fmt.Fprintln(stdout, "no matching ideas")
			return 0
		}
		for _, i := range list {
			fmt.Fprintf(stdout, "%-8s %-10s %s\n", i.HumanID(), i.State, i.Title)
		}
		return 0
	}
	if len(positional) != 1 {
		return usageErr()
	}
	id, ok := parseIdeaRef(positional[0])
	if !ok {
		return usageErr()
	}
	switch sub {
	case "show":
		if jsonMode {
			return result(c.GetIdea(id))
		}
		ws, err := c.IdeaWorkshop(id)
		if err != nil {
			return result(idea.Idea{}, err)
		}
		ideaLines(stdout, ws.Idea)
		workshopLines(stdout, ws)
		cliCard(stdout, cliBuilder(cfg, ws.Idea.Project).IdeaCard(ws.Idea))
		return 0
	case "dismiss":
		return result(c.SetIdeaState(id, idea.Dismissed, *reason))
	case "reopen":
		return result(c.SetIdeaState(id, idea.Open, ""))
	case "link":
		n, ok := idea.ParseRef(strings.TrimPrefix(strings.ToUpper(*requestRef), "REQ-"))
		if !ok {
			return usageErr()
		}
		return result(c.LinkIdeaRequest(id, n))
	default:
		detail, linked, err := c.PromoteIdea(id, requestdomain.CreateInput{
			Request:  requestdomain.Request{Type: *reqType, Title: *title, Description: *description, Origin: "human", SessionRef: "cli"},
			Criteria: criteria,
		})
		if err == nil && !jsonMode {
			fmt.Fprintf(stdout, "created %s\n", detail.Request.HumanID())
			ideaLines(stdout, linked)
			cliCard(stdout, cliBuilder(cfg, linked.Project).IdeaCard(linked))
			return 0
		}
		return writeRequestCLIResult(stdout, jsonMode, map[string]any{"request": detail.Request, "idea": linked}, nil, err)
	}
}

const labelUsage = `usage: ctx label <command>

  list [--org SLUG] [--project P]
  create <name> [--color d73a4a] [--description D] [--org SLUG] [--project P]
  edit <name> [--rename N] [--color C] [--description D] [--org SLUG] [--project P]
  delete <name> [--org SLUG] [--project P]
  add <request|pain|idea|knowledge> <n> <label>...
  remove <request|pain|idea|knowledge> <n> <label>...

A label belongs to the project, or with --org to the org and then applies in all its projects.
Search with label:name and -label:name (ctx search). All commands take --json.`

func cmdLabel(args []string, stdout io.Writer) int {
	args, jsonMode := removeBoolArg(args, "--json")
	usageErr := func() int { fmt.Fprintln(stdout, labelUsage); return 2 }
	if len(args) == 0 {
		return usageErr()
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "create", "edit", "delete", "add", "remove":
	default:
		return usageErr()
	}
	cfg, err := config.Load()
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	c := client.New(cfg)
	fs := flag.NewFlagSet("label "+sub, flag.ContinueOnError)
	fs.SetOutput(stdout)
	org := fs.String("org", "", "the org (slug) instead of the project")
	project := fs.String("project", "", "project (remote or unique short name); default: the current one")
	color := fs.String("color", "", "six hexadecimal digits")
	description := fs.String("description", "", "one short line")
	rename := fs.String("rename", "", "new name (edit)")
	positional, err := parseInterspersed(fs, rest)
	if err != nil {
		return usageErr()
	}
	target := requestProjectAxes(cfg.Machine).Project
	if *project != "" {
		if target, err = c.WriteTarget(*project, ""); err != nil {
			return writeRequestCLIError(stdout, jsonMode, 1, err)
		}
	}
	printLabels := func(ls []store.Label, err error) int {
		if err != nil || jsonMode {
			return writeRequestCLIResult(stdout, jsonMode, ls, nil, err)
		}
		if len(ls) == 0 {
			fmt.Fprintln(stdout, "no labels")
		}
		for _, l := range ls {
			scopeName := "project"
			if l.Scope == "org" {
				scopeName = "org"
			}
			fmt.Fprintf(stdout, "%-24s #%s  %-8s %s\n", l.Name, l.Color, scopeName, l.Description)
		}
		return 0
	}
	switch sub {
	case "list":
		return printLabels(c.ListLabels(target, *org))
	case "create":
		if len(positional) != 1 {
			return usageErr()
		}
		proj := target
		if *org == "" {
			if proj, err = c.WriteTarget(*project, requestProjectAxes(cfg.Machine).Project); err != nil {
				return writeRequestCLIError(stdout, jsonMode, 1, err)
			}
		}
		l, err := c.CreateLabel(proj, *org, positional[0], *color, *description)
		return writeLabel(stdout, jsonMode, "created", l, err)
	case "edit", "delete":
		if len(positional) != 1 {
			return usageErr()
		}
		ls, err := c.ListLabels(target, *org)
		if err != nil {
			return writeRequestCLIError(stdout, jsonMode, 1, err)
		}
		var id int64
		for _, l := range ls {
			if strings.EqualFold(l.Name, positional[0]) && (*org != "" || l.Scope == "project") {
				id = l.ID
			}
		}
		if id == 0 {
			return writeRequestCLIError(stdout, jsonMode, 1, fmt.Errorf("no label %q here", positional[0]))
		}
		if sub == "delete" {
			if err := c.DeleteLabel(id); err != nil {
				return writeRequestCLIError(stdout, jsonMode, 1, err)
			}
			fmt.Fprintf(stdout, "deleted %s\n", positional[0])
			return 0
		}
		var n, col, d *string
		if *rename != "" {
			n = rename
		}
		if *color != "" {
			col = color
		}
		if *description != "" {
			d = description
		}
		l, err := c.UpdateLabel(id, n, col, d)
		return writeLabel(stdout, jsonMode, "updated", l, err)
	default:
		if len(positional) < 3 {
			return usageErr()
		}
		kind := positional[0]
		n, err := strconv.ParseInt(strings.TrimLeft(strings.ToUpper(positional[1]), "REQPAINDEATK-"), 10, 64)
		if err != nil || n <= 0 {
			return usageErr()
		}
		var add, remove []string
		if sub == "add" {
			add = positional[2:]
		} else {
			remove = positional[2:]
		}
		return printLabels(c.SetLabels(kind, n, add, remove))
	}
}

func writeLabel(stdout io.Writer, jsonMode bool, verb string, l store.Label, err error) int {
	if err != nil || jsonMode {
		return writeRequestCLIResult(stdout, jsonMode, l, nil, err)
	}
	fmt.Fprintf(stdout, "%s %s #%s\n", verb, l.Name, l.Color)
	return 0
}

const topicUsage = `usage: ctx topic <command>

  list [--project P]
  save <name> <path-prefix>... [--description D] [--project P]    create or replace a named area
  delete <name> [--project P]

topic:<name> in a search then acts like all its path prefixes. All commands take --json.`

func cmdTopic(args []string, stdout io.Writer) int {
	args, jsonMode := removeBoolArg(args, "--json")
	usageErr := func() int { fmt.Fprintln(stdout, topicUsage); return 2 }
	if len(args) == 0 {
		return usageErr()
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "save", "delete":
	default:
		return usageErr()
	}
	cfg, err := config.Load()
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	c := client.New(cfg)
	fs := flag.NewFlagSet("topic "+sub, flag.ContinueOnError)
	fs.SetOutput(stdout)
	project := fs.String("project", "", "project (remote or unique short name); default: the current one")
	description := fs.String("description", "", "one short line")
	positional, err := parseInterspersed(fs, rest)
	if err != nil {
		return usageErr()
	}
	target, err := c.WriteTarget(*project, requestProjectAxes(cfg.Machine).Project)
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	switch sub {
	case "list":
		ts, err := c.ListTopics(target)
		if err != nil || jsonMode {
			return writeRequestCLIResult(stdout, jsonMode, ts, nil, err)
		}
		if len(ts) == 0 {
			fmt.Fprintln(stdout, "no topics")
		}
		for _, t := range ts {
			fmt.Fprintf(stdout, "%-20s %s\n", t.Name, strings.Join(t.Prefixes, ", "))
		}
		return 0
	case "save":
		if len(positional) < 2 {
			return usageErr()
		}
		t, err := c.SaveTopic(target, positional[0], *description, positional[1:])
		if err != nil || jsonMode {
			return writeRequestCLIResult(stdout, jsonMode, t, nil, err)
		}
		fmt.Fprintf(stdout, "saved %s: %s\n", t.Name, strings.Join(t.Prefixes, ", "))
		return 0
	default:
		if len(positional) != 1 {
			return usageErr()
		}
		if err := c.DeleteTopic(target, positional[0]); err != nil {
			return writeRequestCLIError(stdout, jsonMode, 1, err)
		}
		fmt.Fprintf(stdout, "deleted %s\n", positional[0])
		return 0
	}
}
