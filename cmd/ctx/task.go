package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
)

const taskUsage = `usage: ctx task <command>

  add --kind secret|file|input|action --title T [--where PLACE] [--check HINT] [--desc D] [--explanation E]
      [--request N] [--work W] [--assign NAME] [--project P]
  list [--state pending|open|done|confirmed|dropped|all] [--mine] [--request N] [--project P]
  show <TASK-n|n>
  done <n>... [--note N]
  confirm <n>
  reopen <n> --reason R
  drop <n> --reason R

--where names where a secret or file belongs (path, vault entry, env file), never the value.
All commands take --json.`

func taskLine(t humantask.Task) string {
	line := fmt.Sprintf("%-8s %-10s [%s] %s", t.HumanID(), t.State, t.Kind, t.Title)
	if t.Location != "" {
		line += " -> " + t.Location
	}
	line += fmt.Sprintf(" (REQ-%d)", t.RequestID)
	if t.Assignee != "" {
		line += ", for " + t.Assignee
	}
	return line
}

func cmdTask(args []string, stdout io.Writer) int {
	args, jsonMode := removeBoolArg(args, "--json")
	usageErr := func() int { fmt.Fprintln(stdout, taskUsage); return 2 }
	if len(args) == 0 {
		return usageErr()
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add", "list", "show", "done", "confirm", "reopen", "drop":
	default:
		return usageErr()
	}
	cfg, err := config.Load()
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	c := client.New(cfg)
	one := func(t humantask.Task, err error) int {
		if err == nil && !jsonMode {
			fmt.Fprintln(stdout, taskLine(t))
			return 0
		}
		return writeRequestCLIResult(stdout, jsonMode, t, nil, err)
	}
	taskID := func(v string) (int64, bool) { return pain.ParseNumberRef(v, "TASK-") }
	switch sub {
	case "add":
		fs := flag.NewFlagSet("task add", flag.ContinueOnError)
		fs.SetOutput(stdout)
		kind := fs.String("kind", "", "secret, file, input or action")
		title := fs.String("title", "", "what the human has to do")
		where := fs.String("where", "", "where it belongs: path, vault entry, env file; never the value")
		check := fs.String("check", "", "how you will check it")
		desc := fs.String("desc", "", "description")
		explanation := fs.String("explanation", "", "why you ask and what you recommend, in a few plain sentences")
		request := fs.String("request", "", "request it hangs on; default: the work of this session")
		work := fs.Int64("work", 0, "work id from request start")
		assign := fs.String("assign", "", "project member; default: you")
		everyone := fs.Bool("everyone", false, "anybody in the project may do it")
		project := fs.String("project", "", "project; default: the current one")
		if fs.Parse(rest) != nil || *kind == "" || *title == "" {
			return usageErr()
		}
		target, err := c.WriteTarget(*project, requestProjectAxes(cfg.Machine).Project)
		if err != nil {
			return writeRequestCLIError(stdout, jsonMode, 1, err)
		}
		in := humantask.CreateInput{Project: target, Kind: *kind, Title: *title, Description: *desc, Location: *where,
			CheckHint: *check, WorkID: *work, Assignee: *assign, Everyone: *everyone, Explanation: *explanation}
		if *request != "" {
			n, ok := pain.ParseNumberRef(*request, "REQ-")
			if !ok {
				return usageErr()
			}
			in.RequestID = n
		}
		if in.RequestID == 0 && in.WorkID == 0 {
			in.SessionID = cliSessionID(c, cfg)
		}
		return one(c.CreateHumanTask(in))
	case "list":
		fs := flag.NewFlagSet("task list", flag.ContinueOnError)
		fs.SetOutput(stdout)
		state := fs.String("state", "", "pending (default), open, done, confirmed, dropped or all")
		mine := fs.Bool("mine", false, "only what is yours to do")
		request := fs.String("request", "", "only tasks of this request")
		project := fs.String("project", "", "project; default: the current one")
		if fs.Parse(rest) != nil {
			return usageErr()
		}
		target := requestProjectAxes(cfg.Machine).Project
		if *project != "" {
			if target, err = c.WriteTarget(*project, ""); err != nil {
				return writeRequestCLIError(stdout, jsonMode, 1, err)
			}
		}
		var requestID int64
		if *request != "" {
			n, ok := pain.ParseNumberRef(*request, "REQ-")
			if !ok {
				return usageErr()
			}
			requestID = n
		}
		list, err := c.ListHumanTasks(target, *state, *mine, requestID)
		if err != nil || jsonMode {
			return writeRequestCLIResult(stdout, jsonMode, list, nil, err)
		}
		if len(list) == 0 {
			fmt.Fprintln(stdout, "no matching tasks")
			return 0
		}
		for _, t := range list {
			fmt.Fprintln(stdout, taskLine(t))
		}
		return 0
	case "show":
		if len(rest) != 1 {
			return usageErr()
		}
		id, ok := taskID(rest[0])
		if !ok {
			return usageErr()
		}
		t, err := c.GetHumanTask(id)
		if err != nil || jsonMode {
			return writeRequestCLIResult(stdout, jsonMode, t, nil, err)
		}
		fmt.Fprintln(stdout, taskLine(t))
		for _, f := range [][2]string{{"description", t.Description}, {"check", t.CheckHint}, {"done by", t.DoneBy}, {"note", t.DoneNote}, {"via", t.DoneVia}, {"last reason", t.StateNote}} {
			if f[1] != "" {
				fmt.Fprintf(stdout, "%s: %s\n", f[0], f[1])
			}
		}
		return 0
	case "done":
		pos, flags := splitLeading(rest)
		fs := flag.NewFlagSet("task done", flag.ContinueOnError)
		fs.SetOutput(stdout)
		note := fs.String("note", "", "a short note, never a value")
		if fs.Parse(flags) != nil {
			return usageErr()
		}
		var ids []int64
		for _, v := range append(pos, fs.Args()...) {
			id, ok := taskID(v)
			if !ok {
				return usageErr()
			}
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			return usageErr()
		}
		list, err := c.MarkTasksDone(ids, *note, cliSessionID(c, cfg))
		if err != nil || jsonMode {
			return writeRequestCLIResult(stdout, jsonMode, list, nil, err)
		}
		for _, t := range list {
			fmt.Fprintln(stdout, taskLine(t))
		}
		return 0
	case "confirm", "reopen", "drop":
		pos, flags := splitLeading(rest)
		if len(pos) != 1 {
			return usageErr()
		}
		id, ok := taskID(pos[0])
		if !ok {
			return usageErr()
		}
		fs := flag.NewFlagSet("task "+sub, flag.ContinueOnError)
		fs.SetOutput(stdout)
		reason := fs.String("reason", "", "why")
		if fs.Parse(flags) != nil {
			return usageErr()
		}
		session := cliSessionID(c, cfg)
		switch sub {
		case "confirm":
			return one(c.ConfirmHumanTask(id, session))
		case "reopen":
			return one(c.ReopenHumanTask(id, strings.TrimSpace(*reason), session))
		default:
			return one(c.DropHumanTask(id, strings.TrimSpace(*reason), session))
		}
	}
	return usageErr()
}
