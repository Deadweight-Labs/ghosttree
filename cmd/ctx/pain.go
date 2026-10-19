package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
)

const painUsage = `usage: ctx pain <command>

  add "title" [--body B] [--explanation E] [--motivates REQ-98|#42|URL ...] [--project P]
  list [--state active|mitigated|resolved|obsolete|all] [--request N] [--q text] [--project P]
  show <PAIN-n|n>
  state <n> <mitigated|resolved|obsolete|active> [--remaining R] [--evidence commit:abc|test:T|request:98|decision:42|url:U|file:F|session:S] [--note N]
  edit <n> [--title T] [--body B] --reason R
  link <n> (--request N | --knowledge ID | --ref "text or url")
  unlink <link-id> --reason R

All commands take --json.`

// splitLeading separates the arguments before the first flag from the flags.
func splitLeading(args []string) (positional, flags []string) {
	i := 0
	for i < len(args) && !strings.HasPrefix(args[i], "-") {
		i++
	}
	return args[:i], args[i:]
}

// parseMotivates reads REQ-98, #42 or free text / a URL.
func parseMotivates(v string) pain.Target {
	v = strings.TrimSpace(v)
	if n, ok := pain.ParseNumberRef(v, "REQ-"); ok && (strings.HasPrefix(strings.ToUpper(v), "REQ-")) {
		return pain.Target{RequestID: n}
	}
	if strings.HasPrefix(v, "#") {
		if n, ok := pain.ParseNumberRef(v, ""); ok {
			return pain.Target{KnowledgeID: n}
		}
	}
	return pain.Target{ExternalRef: v}
}

func parseEvidence(v string) pain.Evidence {
	kind, ref, _ := strings.Cut(v, ":")
	return pain.Evidence{Kind: strings.TrimSpace(kind), Ref: strings.TrimSpace(ref)}
}

func painDetailLines(stdout io.Writer, d pain.Detail) {
	p := d.Pain
	fmt.Fprintf(stdout, "%s [%s] %s\n", p.HumanID(), p.State, p.Title)
	if p.Remaining != "" {
		fmt.Fprintf(stdout, "remaining: %s\n", p.Remaining)
	}
	if strings.TrimSpace(p.Body) != "" {
		fmt.Fprintf(stdout, "\n%s\n", p.Body)
	}
	if len(d.Motivates) > 0 {
		fmt.Fprintln(stdout, "\nmotivates")
		for _, l := range d.Motivates {
			switch l.Kind {
			case "request":
				fmt.Fprintf(stdout, "  #%d REQ-%d %s\n", l.ID, l.RequestID, l.Title)
			case "knowledge":
				fmt.Fprintf(stdout, "  #%d knowledge #%d %s\n", l.ID, l.KnowledgeID, l.Title)
			default:
				fmt.Fprintf(stdout, "  #%d %s\n", l.ID, l.ExternalRef)
			}
		}
	}
	if len(d.Events) > 0 {
		fmt.Fprintln(stdout, "\nhistory")
		for _, e := range d.Events {
			line := fmt.Sprintf("  %s %s", e.CreatedAt, e.Kind)
			if e.ToState != "" {
				line += fmt.Sprintf(" %s->%s", e.FromState, e.ToState)
			}
			if e.EvidenceKind != "" {
				line += fmt.Sprintf(" [%s:%s]", e.EvidenceKind, e.EvidenceRef)
			}
			if e.Note != "" {
				line += " " + e.Note
			}
			fmt.Fprintln(stdout, line)
		}
	}
}

func cmdPain(args []string, stdout io.Writer) int {
	args, jsonMode := removeBoolArg(args, "--json")
	usageErr := func() int { fmt.Fprintln(stdout, painUsage); return 2 }
	if len(args) == 0 {
		return usageErr()
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add", "list", "show", "state", "edit", "link", "unlink":
	default:
		return usageErr()
	}
	cfg, err := config.Load()
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	c := client.New(cfg)
	result := func(d pain.Detail, err error) int {
		if err == nil && !jsonMode {
			painDetailLines(stdout, d)
			return 0
		}
		return writeRequestCLIResult(stdout, jsonMode, d, nil, err)
	}
	switch sub {
	case "add":
		pos, flags := splitLeading(rest)
		fs := flag.NewFlagSet("pain add", flag.ContinueOnError)
		fs.SetOutput(stdout)
		body := fs.String("body", "", "what goes wrong")
		explanation := fs.String("explanation", "", "short plain-language note for the people who read the problems")
		project := fs.String("project", "", "project (remote or unique short name); default: the current one")
		var motivates repeatedString
		fs.Var(&motivates, "motivates", "REQ-n, #knowledge-id or a URL / free text; repeatable")
		if fs.Parse(flags) != nil {
			return usageErr()
		}
		title := strings.TrimSpace(strings.Join(append(pos, fs.Args()...), " "))
		if title == "" {
			return usageErr()
		}
		target, err := c.WriteTarget(*project, requestProjectAxes(cfg.Machine).Project)
		if err != nil {
			return writeRequestCLIError(stdout, jsonMode, 1, err)
		}
		var targets []pain.Target
		for _, m := range motivates {
			targets = append(targets, parseMotivates(m))
		}
		d, err := c.CreatePainExplained(target, title, *body, *explanation, targets, cliSessionID(c, cfg))
		return result(d, err)
	case "list":
		fs := flag.NewFlagSet("pain list", flag.ContinueOnError)
		fs.SetOutput(stdout)
		state := fs.String("state", "", "active, mitigated, resolved, obsolete or all")
		request := fs.String("request", "", "only pains that motivate this request")
		q := fs.String("q", "", "words in title or text")
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
		list, err := c.SearchPains(target, *state, *q, requestID, 200)
		if err != nil || jsonMode {
			return writeRequestCLIResult(stdout, jsonMode, list, nil, err)
		}
		if len(list) == 0 {
			fmt.Fprintln(stdout, "no matching pains")
			return 0
		}
		for _, p := range list {
			fmt.Fprintf(stdout, "%-8s %-10s %s\n", p.HumanID(), p.State, p.Title)
			if p.State == pain.Mitigated {
				fmt.Fprintf(stdout, "%-8s %-10s remaining: %s\n", "", "", p.Remaining)
			}
		}
		return 0
	case "show":
		if len(rest) != 1 {
			return usageErr()
		}
		id, ok := pain.ParseNumberRef(rest[0], "PAIN-")
		if !ok {
			return usageErr()
		}
		return result(c.GetPain(id))
	case "state":
		pos, flags := splitLeading(rest)
		if len(pos) != 2 {
			return usageErr()
		}
		id, ok := pain.ParseNumberRef(pos[0], "PAIN-")
		if !ok {
			return usageErr()
		}
		fs := flag.NewFlagSet("pain state", flag.ContinueOnError)
		fs.SetOutput(stdout)
		remaining := fs.String("remaining", "", "what is still unsolved (mitigated)")
		evidence := fs.String("evidence", "", "kind:ref")
		note := fs.String("note", "", "why")
		if fs.Parse(flags) != nil {
			return usageErr()
		}
		return result(c.SetPainState(id, pos[1], *remaining, parseEvidence(*evidence), *note, cliSessionID(c, cfg)))
	case "edit":
		pos, flags := splitLeading(rest)
		if len(pos) != 1 {
			return usageErr()
		}
		id, ok := pain.ParseNumberRef(pos[0], "PAIN-")
		if !ok {
			return usageErr()
		}
		fs := flag.NewFlagSet("pain edit", flag.ContinueOnError)
		fs.SetOutput(stdout)
		title := fs.String("title", "", "new title")
		body := fs.String("body", "", "new text")
		reason := fs.String("reason", "", "why")
		if fs.Parse(flags) != nil {
			return usageErr()
		}
		var t, b *string
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "title":
				t = title
			case "body":
				b = body
			}
		})
		return result(c.CorrectPain(id, t, b, *reason))
	case "link":
		pos, flags := splitLeading(rest)
		if len(pos) != 1 {
			return usageErr()
		}
		id, ok := pain.ParseNumberRef(pos[0], "PAIN-")
		if !ok {
			return usageErr()
		}
		fs := flag.NewFlagSet("pain link", flag.ContinueOnError)
		fs.SetOutput(stdout)
		request := fs.Int64("request", 0, "request number")
		knowledge := fs.Int64("knowledge", 0, "knowledge id")
		ref := fs.String("ref", "", "external text or URL")
		if fs.Parse(flags) != nil {
			return usageErr()
		}
		return result(c.AddPainLink(id, pain.Target{RequestID: *request, KnowledgeID: *knowledge, ExternalRef: *ref}, cliSessionID(c, cfg)))
	case "unlink":
		pos, flags := splitLeading(rest)
		if len(pos) != 1 {
			return usageErr()
		}
		var id int64
		if _, err := fmt.Sscan(pos[0], &id); err != nil {
			return usageErr()
		}
		fs := flag.NewFlagSet("pain unlink", flag.ContinueOnError)
		fs.SetOutput(stdout)
		reason := fs.String("reason", "", "why")
		if fs.Parse(flags) != nil {
			return usageErr()
		}
		if err := c.RemovePainLink(id, *reason); err != nil {
			return writeRequestCLIError(stdout, jsonMode, 1, err)
		}
		if !jsonMode {
			fmt.Fprintln(stdout, "link removed")
			return 0
		}
		return writeRequestCLIResult(stdout, jsonMode, map[string]any{"removed": id}, nil, nil)
	}
	return usageErr()
}

// cliSessionID is the numeric session of the calling harness, found among the
// recent sessions of the project by its external id; zero when unknown.
func cliSessionID(c *client.Client, cfg config.Config) int64 {
	ref := currentSessionRef()
	if ref == "" {
		return 0
	}
	sessions, err := c.Sessions(requestProjectAxes(cfg.Machine), 50)
	if err != nil {
		return 0
	}
	for _, s := range sessions {
		if s.ExternalID == ref {
			return s.ID
		}
	}
	return 0
}
