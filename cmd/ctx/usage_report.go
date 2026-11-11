package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/deliverylog"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const usageReportUsage = `usage: ctx usage tools      [--since YYYY-MM-DD] [--project P] [--actions] [--json]
       ctx usage deliveries [--since YYYY-MM-DD] [--project P] [--json]
       ctx usage signals    [--since YYYY-MM-DD] [--project P] [--json]
       ctx usage context    [--since YYYY-MM-DD] [--project P] [--json]

Reports from the server, over the projects you may see in full (or --project).
They hold sums only.

tools       calls of the ghosttree tools per tool, from tool_call_counts: in how
            many sessions and how often. This is the count the gate of the
            tool-loading decision asks for. The share is of the sessions of the
            window whose transcript is stored; a session that called no ghosttree
            tool is in the denominator.
deliveries  what the hooks put in front of agents, per route and kind: parts,
            characters, sessions. Characters add up to what the session budget
            counted. Months older than the retention appear folded, as whole
            months that start inside the window (a month the window cuts into
            is left out).
signals     the environment variables of the process a session ran in, per value:
            how many sessions, and what share of the finished ones had at most
            three prompts.
context     how much of the delivered context the agent used: per route and
            kind and per profile, the parts that were judged from the session
            transcript, how many the agent picked up (named by number or by
            two words of the title, or looked up) or followed (opened the path
            the entry names, ran its command, reported the runbook), and the
            share of the characters. Only entries and runbooks are judged;
            path descriptions are counted but not judged. A hint, not a proof:
            nothing is lowered or held back on it.`

var sinceFlag = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// cmdUsageReport runs the reports of REQ-636. It reports whether args named one,
// so that the older flags of ctx usage keep working.
func cmdUsageReport(args []string, stdout io.Writer) (code int, handled bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case "tools", "deliveries", "signals", "context":
	default:
		return 0, false
	}
	sub := args[0]
	fs := flag.NewFlagSet("usage "+sub, flag.ContinueOnError)
	fs.SetOutput(stdout)
	since := fs.String("since", time.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02"), "start of the window, a day in UTC")
	project := fs.String("project", "", "only this project; default: every project you may see in full")
	actions := fs.Bool("actions", false, "tools: also list the actions of each tool")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Usage = func() { fmt.Fprintln(stdout, usageReportUsage) }
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || !sinceFlag.MatchString(*since) {
		fmt.Fprintln(stdout, usageReportUsage)
		return 2, true
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "config: %v\n", err)
		return 1, true
	}
	c := client.New(cfg)
	var out any
	switch sub {
	case "tools":
		rep, err := c.UsageTools(*project, *since)
		if err != nil {
			return usageReportError(stdout, err), true
		}
		out = rep
		if !*asJSON {
			printToolReport(stdout, rep, *actions)
		}
	case "deliveries":
		rep, err := c.UsageDeliveries(*project, *since)
		if err != nil {
			return usageReportError(stdout, err), true
		}
		out = rep
		if !*asJSON {
			printDeliveryReport(stdout, rep)
		}
	case "context":
		rep, err := c.UsageContext(*project, *since)
		if err != nil {
			return usageReportError(stdout, err), true
		}
		out = rep
		if !*asJSON {
			printContextUse(stdout, rep)
		}
	case "signals":
		rep, err := c.UsageSignals(*project, *since)
		if err != nil {
			return usageReportError(stdout, err), true
		}
		out = rep
		if !*asJSON {
			printSignalReport(stdout, rep)
		}
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	}
	return 0, true
}

func usageReportError(stdout io.Writer, err error) int {
	if client.IsRouteMissing(err) {
		fmt.Fprintln(stdout, "this server does not have the usage reports yet; update it first")
		return 1
	}
	fmt.Fprintf(stdout, "report: %v\n", err)
	return 1
}

func percent(n, of int) string {
	if of <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f %%", 100*float64(n)/float64(of))
}

func printToolReport(w io.Writer, rep store.ToolReport, actions bool) {
	// A tool counted in a session whose transcript was not uploaded must not push
	// a share over 100 %: the larger of the two is the denominator.
	of := max(rep.Sessions, rep.Counted)
	fmt.Fprintf(w, "ghosttree tool calls since %s (tool_call_counts)\n", rep.Since)
	fmt.Fprintf(w, "sessions in the window: %d, with at least one counted call: %d\n", of, rep.Counted)
	if len(rep.Tools) == 0 {
		fmt.Fprintln(w, "nothing counted")
		return
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "tool\tsessions\tshare\tcalls\t")
	for _, t := range rep.Tools {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%d\t\n", t.Tool, t.Sessions, percent(t.Sessions, of), t.Calls)
		if actions {
			for _, a := range t.Actions {
				name := a.Action
				if name == "" {
					name = "(none)"
				}
				fmt.Fprintf(tw, "  %s\t%d\t%s\t%d\t\n", name, a.Sessions, percent(a.Sessions, of), a.Calls)
			}
		}
	}
	_ = tw.Flush()
}

func printDeliveryReport(w io.Writer, rep deliverylog.DeliveryUsage) {
	fmt.Fprintf(w, "context delivered by the hooks since %s\n", rep.Since)
	fmt.Fprintf(w, "%d sessions, %d characters", rep.Sessions, rep.Chars)
	if rep.SubagentChars > 0 {
		fmt.Fprintf(w, "; %d of them in hooks that ran for a subagent (%d sessions)", rep.SubagentChars, rep.SubagentSessions)
	}
	fmt.Fprintln(w)
	print := func(title string, lines []deliverylog.DeliveryLine, sessions bool) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(w, "\n%s\n", title)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
		head := "route\tkind\tparts\tchars\tchars per part\t"
		if sessions {
			head = "route\tkind\tparts\tchars\tchars per part\tsessions\t"
		}
		fmt.Fprintln(tw, head)
		for _, l := range lines {
			per := 0
			if l.Parts > 0 {
				per = l.Chars / l.Parts
			}
			if sessions {
				fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t\n", l.Via, l.Kind, l.Parts, l.Chars, per, l.Sessions)
			} else {
				fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t\n", l.Via, l.Kind, l.Parts, l.Chars, per)
			}
		}
		_ = tw.Flush()
	}
	print("by route and kind", rep.Lines, true)
	print(fmt.Sprintf("folded months from %s (whole months inside the window, older than the retention; no session counts)", rep.FoldedFrom), rep.Folded, false)
	if len(rep.Lines) == 0 && len(rep.Folded) == 0 {
		fmt.Fprintln(w, "nothing logged")
	}
}

func printSignalReport(w io.Writer, rep deliverylog.SignalUsage) {
	fmt.Fprintf(w, "session signals since %s\n", rep.Since)
	fmt.Fprintln(w, "short = at most three prompts, of the finished sessions whose transcript is stored")
	if len(rep.Lines) == 0 {
		fmt.Fprintln(w, "nothing recorded")
		return
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "signal\tvalue\tsessions\tmeasured\tshort\tshare short\t")
	for _, l := range rep.Lines {
		value := l.Value
		if value == "" {
			value = "(unset)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\t\n", strings.TrimPrefix(l.Name, "CLAUDE_CODE_"), value, l.Sessions, l.Measured, l.Short, percent(l.Short, l.Measured))
	}
	_ = tw.Flush()
}

// printContextUse prints the use of the delivered context. The share is of the
// judged parts: a part that waits for its judgement or has none is not in it.
func printContextUse(w io.Writer, rep deliverylog.ContextUse) {
	fmt.Fprintf(w, "use of the delivered context since %s\n", rep.Since)
	fmt.Fprintln(w, "used = picked up or followed, of the judged parts; weight = their share of the judged characters")
	print := func(title, first string, lines []deliverylog.UseLine, key func(deliverylog.UseLine) string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(w, "\n%s\n", title)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
		fmt.Fprintf(tw, "%s\tkind\tparts\tjudged\tpicked up\tfollowed\twaiting\tused\tweight\t\n", first)
		for _, l := range lines {
			used, weight := "-", "-"
			if deliverylog.Judgeable(l.Kind) {
				used, weight = percent(l.PickedUp+l.Followed, l.Judged), percent(l.UsedChars, l.JudgedChars)
			}
			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t\n", key(l), l.Kind, l.Parts, l.Judged, l.PickedUp, l.Followed, l.Pending, used, weight)
		}
		_ = tw.Flush()
	}
	print("by route and kind", "route", rep.Lines, func(l deliverylog.UseLine) string { return l.Via })
	print("by profile of the session (entries and runbooks)", "profile", rep.Profiles, func(l deliverylog.UseLine) string { return l.Profile })
	if len(rep.Lines) == 0 {
		fmt.Fprintln(w, "nothing logged")
	}
}
