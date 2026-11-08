package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Abläufe (REQ-604) über MCP: lesen, vorschlagen, zurückmelden. Ausführen und
// Freigeben gibt es hier absichtlich nicht. Ausgeführt wird nur im Vordergrund
// eines Befehls, den der Agent durch die Bash-Freigabe der Harness schickt
// (ctx run NAME --target T --run), freigegeben wird von Menschen (ctx runbook
// approve, ctx runbook allow).

type RunbookGetInput struct {
	Name    string            `json:"name,omitempty" jsonschema:"the runbook, e.g. rollout-prod. Omit it to list the runbooks that apply here"`
	Version int               `json:"version,omitempty" jsonschema:"a version number; default is the newest. Only an approved version should be followed without asking"`
	Params  map[string]string `json:"params,omitempty" jsonschema:"parameter values to fill into the checklist, e.g. {\"sha\":\"abc1234\"}. Values are typed and limited to letters, digits and . _ / : @ + , = -"`
	Project string            `json:"project,omitempty" jsonschema:"the project when it is not the session's own"`
}

type RunbookGetOutput struct {
	Runbook *runbook.View     `json:"runbook,omitempty"`
	List    []runbook.Summary `json:"list,omitempty"`
}

type RunbookWriteInput struct {
	Name    string       `json:"name" jsonschema:"runbook name: lowercase letters, digits, '.', '_', '-'"`
	Spec    runbook.Spec `json:"spec" jsonschema:"purpose, params (typed), preconditions, steps (title, exact command, check), rollback, pitfalls (knowledge numbers), triggers (keywords, command patterns, paths) and optional executable targets (argv lists, no shell)"`
	Note    string       `json:"note,omitempty" jsonschema:"what changed in this version"`
	Project string       `json:"project,omitempty" jsonschema:"the project when it is not the session's own"`
	Machine string       `json:"machine,omitempty" jsonschema:"the machine the runbook applies to (use here for the one you are on). Together with the project (the default) it is a project runbook for that host: everyone with a role in the project reads it, the machine only says where it applies. With global it concerns the host alone and stays private to the machine's owner"`
	Global  bool         `json:"global,omitempty" jsonschema:"no project: the runbook is yours alone, or, with machine, the owner's of that machine. Use it only for a procedure that concerns the host and no repository; a procedure for operating a repository on a host belongs to the project, with machine"`
}

type RunbookWriteOutput struct {
	ID         int64  `json:"id"`
	Version    int    `json:"version"`
	Hash       string `json:"hash"`
	NewVersion bool   `json:"new_version"`
	Approved   bool   `json:"approved"`
	Message    string `json:"message"`
}

type RunReportInput struct {
	Name       string              `json:"name" jsonschema:"the runbook that was followed"`
	Result     string              `json:"result" jsonschema:"ok, failed or aborted"`
	Version    int                 `json:"version,omitempty" jsonschema:"the version that was followed; default is the newest"`
	Note       string              `json:"note,omitempty" jsonschema:"what happened, in one line"`
	Params     map[string]string   `json:"params,omitempty" jsonschema:"the parameter values of the run"`
	Target     string              `json:"target,omitempty" jsonschema:"the executable target, if one was run"`
	Deviations []runbook.Deviation `json:"deviations,omitempty" jsonschema:"where the run differed: kind add, change or remove, the step number, title and exact command. This becomes a proposal that an owner or lead must approve; it never changes the runbook by itself"`
	Project    string              `json:"project,omitempty" jsonschema:"the project when it is not the session's own"`
}

type RunReportOutput struct {
	RunID      int64  `json:"run_id"`
	ProposalID int64  `json:"proposal_id,omitempty"`
	Message    string `json:"message"`
}

func (s *Server) registerRunbookTools(srv *mcp.Server) {
	closed := false
	additive := false
	mcp.AddTool(srv, &mcp.Tool{Name: "runbook_get", Description: "Read a runbook: the named procedure for something that must be done the same way every time (build, roll out, check). Returns the checklist with the parameters filled in, the version and whether an owner or lead approved it, the last successful run with its commit, linked pitfalls, and executable targets. Without a name it lists the runbooks that apply here. Follow the steps exactly; when it differs from what you had to do, report the deviation with run_report. Nothing is executed by this tool.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleRunbookGet)
	mcp.AddTool(srv, &mcp.Tool{Name: "runbook_write", Description: "Create a runbook or write a new version of one (every version is kept). A version you write here is a proposal: it is NOT approved and never runs by itself, and it is never delivered automatically until an owner or lead approves it (ctx runbook approve). Use it to record a procedure that was worked out and verified, with exact commands, checks and a way back. A procedure for operating a repository on a host (deploying the project on build-1) gets BOTH the project (default) and machine, so everyone with a role in the project reads it; only a procedure that concerns the host alone gets machine with global and stays private to its owner. Commands in steps are text for people; only 'targets' (argv lists, no shell, typed parameters) can be executed, and only after approval and a one-time allowance on each machine.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleRunbookWrite)
	mcp.AddTool(srv, &mcp.Tool{Name: "run_report", Description: "Report the outcome of following a runbook: ok, failed or aborted, with the version, the parameters and the git state (recorded automatically). The runbook then shows 'last success on commit ...'. If you added, changed or skipped a step, list it as a deviation: it becomes a proposal to update the runbook that an owner or lead must approve.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleRunReport)
}

func (s *Server) runbookAxes(project string) scope.Axes {
	ax := s.currentAxes()
	out := scope.Axes{Project: ax.Project, Machine: ax.Machine}
	if strings.TrimSpace(project) != "" {
		out.Project = scope.NormalizeRemote(project)
	}
	return out
}

func (s *Server) handleRunbookGet(_ context.Context, _ *mcp.CallToolRequest, in RunbookGetInput) (*mcp.CallToolResult, RunbookGetOutput, error) {
	ax := s.runbookAxes(in.Project)
	if strings.TrimSpace(in.Name) == "" {
		list, err := s.client.ListRunbooks(ax)
		if err != nil {
			return nil, RunbookGetOutput{}, err
		}
		if len(list) == 0 {
			return textResult("No runbooks apply here."), RunbookGetOutput{}, nil
		}
		var b strings.Builder
		for _, r := range list {
			status := fmt.Sprintf("v%d", r.Head)
			if r.ApprovedVersion == 0 {
				status += " NOT APPROVED"
			} else if r.ApprovedVersion != r.Head {
				status += fmt.Sprintf(" (approved v%d)", r.ApprovedVersion)
			}
			fmt.Fprintf(&b, "- %s [%s, %s]: %s %s\n", r.Name, status, runbook.ScopeLabel(r.Project, r.Machine), r.Purpose, s.builder().WithProject(r.Project).Runbook(r.Name))
		}
		return textResult(b.String()), RunbookGetOutput{List: list}, nil
	}
	v, err := s.client.FindRunbook(strings.TrimSpace(in.Name), ax, in.Version)
	if err != nil {
		return nil, RunbookGetOutput{}, err
	}
	values, err := v.Spec.BindPartial(in.Params)
	if err != nil {
		return nil, RunbookGetOutput{}, err
	}
	return s.withCard(textResult(runbook.Checklist(v, values)), s.builder().RunbookCard(links.SummaryOf(v))), RunbookGetOutput{Runbook: &v}, nil
}

func (s *Server) handleRunbookWrite(_ context.Context, _ *mcp.CallToolRequest, in RunbookWriteInput) (*mcp.CallToolResult, RunbookWriteOutput, error) {
	spec, err := runbook.Normalize(in.Spec)
	if err != nil {
		return nil, RunbookWriteOutput{}, err
	}
	project := ""
	if !in.Global {
		if project, err = s.writeTarget(in.Project); err != nil {
			return nil, RunbookWriteOutput{}, err
		}
	}
	machine := ""
	if in.Machine == "here" {
		machine = s.ctxAxes.Machine
	} else if in.Machine != "" {
		machine = in.Machine
	}
	// Ein Agent verlangt nie die Freigabe: er schlägt vor, ein Mensch gibt frei.
	res, err := s.client.WriteRunbook(project, machine, strings.TrimSpace(in.Name), spec, in.Note, false)
	if err != nil {
		return nil, RunbookWriteOutput{}, err
	}
	msg := fmt.Sprintf("%s: version %d", in.Name, res.Version)
	switch {
	case res.NewRunbook:
		msg += " created"
	case res.NewVersion:
		msg += " written"
	default:
		msg += " unchanged"
	}
	if res.Approved {
		msg += ", approved"
	} else {
		msg += ", NOT approved: it will not be delivered or run until an owner or lead runs ctx runbook approve " + in.Name
	}
	out := RunbookWriteOutput{ID: res.ID, Version: res.Version, Hash: res.Hash, NewVersion: res.NewVersion, Approved: res.Approved, Message: msg}
	written := runbook.Summary{Name: strings.TrimSpace(in.Name), Project: project, Purpose: spec.Purpose, Head: res.Version}
	if res.Approved {
		written.ApprovedVersion = res.Version
	}
	return s.withCard(textResult(msg), s.builder().RunbookCard(written)), out, nil
}

func (s *Server) handleRunReport(_ context.Context, _ *mcp.CallToolRequest, in RunReportInput) (*mcp.CallToolResult, RunReportOutput, error) {
	switch in.Result {
	case runbook.ResultOK, runbook.ResultFailed, runbook.ResultAborted:
	default:
		return nil, RunReportOutput{}, fmt.Errorf("result must be ok, failed or aborted")
	}
	ax := s.runbookAxes(in.Project)
	v, err := s.client.FindRunbook(strings.TrimSpace(in.Name), ax, in.Version)
	if err != nil {
		return nil, RunReportOutput{}, err
	}
	res, err := s.client.ReportRun(v.ID, client.RunReport{
		Version: v.Version, Target: in.Target, Mode: runbook.ModeChecklist, Result: in.Result, Params: in.Params,
		Note: in.Note, Machine: s.ctxAxes.Machine, Deviations: in.Deviations,
	})
	if err != nil {
		return nil, RunReportOutput{}, err
	}
	msg := fmt.Sprintf("run %d reported (%s, %s version %d)", res.RunID, in.Result, v.Name, v.Version)
	if res.ProposalID > 0 {
		msg += fmt.Sprintf("; the deviation became proposal %d, which an owner or lead must approve", res.ProposalID)
	}
	return s.withCard(textResult(msg), s.builder().RunbookCard(links.SummaryOf(v))), RunReportOutput{RunID: res.RunID, ProposalID: res.ProposalID, Message: msg}, nil
}
