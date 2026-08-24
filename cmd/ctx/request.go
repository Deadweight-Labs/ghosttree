package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const requestUsage = `usage: ctx request <command>

  list [--json]                    list open requests
  search <query> [--json]          search requests for the current project
  show <id> [--json]               show one request
  create --type T --title T [--description D] [--ac C...] [--json]
  start <id> --session N [--role primary|related] [--json]
  pause|complete-work|abandon <work-id> --summary S [--json]
  ac add <id> <criterion> [--json]
  ac met|waive <id> <criterion-id> --evidence-kind K --evidence-ref R [--json]
  done <id> --evidence-kind K --evidence-ref R [--json]`

type requestEnvelope struct {
	OK       bool     `json:"ok"`
	Data     any      `json:"data,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Error    any      `json:"error,omitempty"`
}

type repeatedString []string

func (r *repeatedString) String() string     { return strings.Join(*r, ",") }
func (r *repeatedString) Set(v string) error { *r = append(*r, v); return nil }

func cmdRequest(args []string, stdout io.Writer) int {
	args, jsonMode := removeBoolArg(args, "--json")
	if len(args) == 0 {
		fmt.Fprintln(stdout, requestUsage)
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	c := client.New(cfg)
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		if len(rest) != 0 {
			return requestCLIUsage(stdout)
		}
		page, err := c.SearchRequests(requestdomain.SearchFilter{Scope: requestProjectAxes(cfg.Machine), State: "open", Limit: 25})
		return writeRequestCLIResult(stdout, jsonMode, page, nil, err)
	case "search":
		if len(rest) == 0 {
			return requestCLIUsage(stdout)
		}
		page, err := c.SearchRequests(requestdomain.SearchFilter{Scope: requestProjectAxes(cfg.Machine), Query: strings.Join(rest, " "), State: "open", Limit: 25})
		return writeRequestCLIResult(stdout, jsonMode, page, nil, err)
	case "show":
		if len(rest) != 1 {
			return requestCLIUsage(stdout)
		}
		id, err := strconv.ParseInt(rest[0], 10, 64)
		if err != nil {
			return requestCLIUsage(stdout)
		}
		detail, err := c.GetRequest(id)
		return writeRequestCLIResult(stdout, jsonMode, detail, nil, err)
	case "create":
		fs := flag.NewFlagSet("request create", flag.ContinueOnError)
		fs.SetOutput(stdout)
		typ := fs.String("type", "", "request type")
		title := fs.String("title", "", "title")
		description := fs.String("description", "", "description")
		priority := fs.String("priority", "", "priority")
		key := fs.String("idempotency-key", "", "retry key")
		var criteria repeatedString
		fs.Var(&criteria, "ac", "acceptance criterion")
		if fs.Parse(rest) != nil || *typ == "" || *title == "" {
			return requestCLIUsage(stdout)
		}
		detail, err := c.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: *typ, Title: *title, Description: *description, Priority: *priority, Scope: requestProjectAxes(cfg.Machine)}, Criteria: criteria, IdempotencyKey: *key})
		return writeRequestCLIResult(stdout, jsonMode, detail, nil, err)
	case "start":
		id, flags, ok := requestIDAndFlags(rest)
		if !ok {
			return requestCLIUsage(stdout)
		}
		fs := flag.NewFlagSet("request start", flag.ContinueOnError)
		fs.SetOutput(stdout)
		sessionID := fs.Int64("session", 0, "Ghosttree session ID")
		role := fs.String("role", "primary", "primary or related")
		if fs.Parse(flags) != nil || *sessionID == 0 {
			return requestCLIUsage(stdout)
		}
		work, warnings, err := c.StartRequestWork(id, *sessionID, *role)
		return writeRequestCLIResult(stdout, jsonMode, work, warnings, err)
	case "pause", "complete-work", "abandon":
		workID, flags, ok := requestIDAndFlags(rest)
		if !ok {
			return requestCLIUsage(stdout)
		}
		fs := flag.NewFlagSet("request "+sub, flag.ContinueOnError)
		fs.SetOutput(stdout)
		summary := fs.String("summary", "", "handoff summary")
		if fs.Parse(flags) != nil || *summary == "" {
			return requestCLIUsage(stdout)
		}
		state := map[string]string{"pause": "paused", "complete-work": "completed", "abandon": "abandoned"}[sub]
		work, err := c.FinishRequestWork(workID, state, *summary)
		return writeRequestCLIResult(stdout, jsonMode, work, nil, err)
	case "ac":
		return cmdRequestAC(c, rest, stdout, jsonMode)
	case "done":
		id, flags, ok := requestIDAndFlags(rest)
		if !ok {
			return requestCLIUsage(stdout)
		}
		fs := flag.NewFlagSet("request done", flag.ContinueOnError)
		fs.SetOutput(stdout)
		kind := fs.String("evidence-kind", "", "evidence kind")
		ref := fs.String("evidence-ref", "", "evidence reference")
		if fs.Parse(flags) != nil || *kind == "" || *ref == "" {
			return requestCLIUsage(stdout)
		}
		err := c.CompleteRequest(id, requestdomain.Evidence{Kind: *kind, Ref: *ref})
		if err != nil {
			return writeRequestCLIError(stdout, jsonMode, 1, err)
		}
		detail, err := c.GetRequest(id)
		return writeRequestCLIResult(stdout, jsonMode, detail, nil, err)
	default:
		return requestCLIUsage(stdout)
	}
}

func requestIDAndFlags(args []string) (int64, []string, bool) {
	if len(args) == 0 {
		return 0, nil, false
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	return id, args[1:], err == nil
}

func cmdRequestAC(c *client.Client, args []string, stdout io.Writer, jsonMode bool) int {
	if len(args) < 3 {
		return requestCLIUsage(stdout)
	}
	action := args[0]
	requestID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return requestCLIUsage(stdout)
	}
	if action == "add" {
		criterion, err := c.AddRequestCriterion(requestID, strings.Join(args[2:], " "))
		return writeRequestCLIResult(stdout, jsonMode, criterion, nil, err)
	}
	if action != "met" && action != "waive" {
		return requestCLIUsage(stdout)
	}
	criterionID, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		return requestCLIUsage(stdout)
	}
	fs := flag.NewFlagSet("request ac "+action, flag.ContinueOnError)
	fs.SetOutput(stdout)
	kind := fs.String("evidence-kind", "", "evidence kind")
	ref := fs.String("evidence-ref", "", "evidence reference")
	if fs.Parse(args[3:]) != nil || *kind == "" || *ref == "" {
		return requestCLIUsage(stdout)
	}
	state := "met"
	if action == "waive" {
		state = "waived"
	}
	err = c.SetRequestCriterion(criterionID, state, requestdomain.Evidence{Kind: *kind, Ref: *ref})
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	detail, err := c.GetRequest(requestID)
	return writeRequestCLIResult(stdout, jsonMode, detail, nil, err)
}

func requestProjectAxes(machine string) scope.Axes {
	return scope.Axes{Project: currentAxes(machine).Project}
}

func removeBoolArg(args []string, name string) ([]string, bool) {
	out := make([]string, 0, len(args))
	found := false
	for _, arg := range args {
		if arg == name {
			found = true
		} else {
			out = append(out, arg)
		}
	}
	return out, found
}

func requestCLIUsage(stdout io.Writer) int { fmt.Fprintln(stdout, requestUsage); return 2 }

func writeRequestCLIResult(stdout io.Writer, jsonMode bool, data any, warnings []string, err error) int {
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	if jsonMode {
		_ = json.NewEncoder(stdout).Encode(requestEnvelope{OK: true, Data: data, Warnings: warnings})
		return 0
	}
	// A list is read, not parsed. Printing the page as indented JSON put 68,617
	// characters on the terminal for twenty-four requests, which is the same
	// failure the tool result had and for the same reason: a listing carried
	// every description in full.
	if page, ok := data.(requestdomain.SearchPage); ok {
		writeRequestTable(stdout, page)
		return 0
	}
	b, _ := json.MarshalIndent(data, "", "  ")
	fmt.Fprintln(stdout, string(b))
	return 0
}

func writeRequestTable(stdout io.Writer, page requestdomain.SearchPage) {
	if len(page.Results) == 0 {
		fmt.Fprintln(stdout, "no matching requests")
		return
	}
	for _, hit := range page.Results {
		priority := hit.Request.Priority
		if priority == "" {
			priority = "—"
		}
		fmt.Fprintf(stdout, "%-8s %-14s %-8s %-8s %2d AC  %s\n",
			hit.Request.HumanID(), hit.Request.Type, priority, hit.Request.State,
			hit.OpenCriteria, hit.Request.Title)
	}
	fmt.Fprintf(stdout, "\n%d requests — 'ctx request show <id>' for one of them\n", len(page.Results))
	if page.NextCursor != "" {
		fmt.Fprintf(stdout, "more with --cursor %s\n", page.NextCursor)
	}
}

func writeRequestCLIError(stdout io.Writer, jsonMode bool, code int, err error) int {
	if jsonMode {
		_ = json.NewEncoder(stdout).Encode(requestEnvelope{OK: false, Error: err.Error()})
	} else {
		fmt.Fprintln(stdout, err)
	}
	return code
}
