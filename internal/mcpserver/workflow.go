package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/workflow"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Workflows (REQ-695) over MCP: read, check and save the definition files that
// live on the server. They are actions of the core tool runbook (the list of
// core tools is full, MaxCore): both are procedures of a project, one followed
// by an agent in a session, the other run by the server on an event. A save
// here gives the same version as one in the web interface.

type WorkflowGetInput struct {
	Name     string `json:"name,omitempty" jsonschema:"the workflow; omit it to list the workflows of the project"`
	Versions bool   `json:"versions,omitempty" jsonschema:"also list every saved version with author and time"`
	Project  string `json:"project,omitempty" jsonschema:"the project when it is not the session's own"`
}

type WorkflowGetOutput struct {
	List     []store.WorkflowDef        `json:"list,omitempty"`
	Workflow *store.WorkflowDef         `json:"workflow,omitempty"`
	Versions []store.WorkflowDefVersion `json:"versions,omitempty"`
	CanEdit  bool                       `json:"can_edit,omitempty"`
}

type WorkflowWriteInput struct {
	Name    string `json:"name" jsonschema:"workflow name: 2 to 64 lower-case letters, digits, - and _"`
	Text    string `json:"text" jsonschema:"the whole file: a line ---, the settings (on, branches, every, chain, inputs, uses, tools, rounds, results, model, budget, research, description), a line ---, then Markdown. With uses (a block ghosttree/<name>) the Markdown is an optional instruction; without uses it is an own prompt (stage 3) that runs with the declared read tools"`
	Base    int    `json:"base_version,omitempty" jsonschema:"the version the text was written on, from workflow_get; 0 creates a new workflow. A save on an older version is refused"`
	Note    string `json:"note,omitempty" jsonschema:"what changed in this version"`
	Project string `json:"project,omitempty" jsonschema:"the project when it is not the session's own"`
}

type WorkflowWriteOutput struct {
	Version int                  `json:"version,omitempty"`
	Changed bool                 `json:"changed"`
	Check   workflow.CheckReport `json:"check"`
	Message string               `json:"message"`
}

type WorkflowCheckInput struct {
	Text    string `json:"text" jsonschema:"the whole file to check"`
	Project string `json:"project,omitempty" jsonschema:"the project whose aliases and caps apply; default is the session's own"`
}

func (s *Server) registerWorkflowTools(srv *mcp.Server) {
	closed := false
	additive := false
	mcp.AddTool(srv, &mcp.Tool{Name: "workflow_get", Description: "Read the workflows of a project: what the server runs on an event (a pull request merged, an idea created, a schedule). Without a name it lists them with on/off and version; with a name it returns the file and, with versions, every saved version. Members read, leads and owners change.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleWorkflowGet)
	mcp.AddTool(srv, &mcp.Tool{Name: "workflow_write", Description: "Create a workflow or save a new version of one. The file is checked first (keys, block, inputs, kinds of result, model alias, budget); a file with errors is not saved and the errors are returned. A saved version applies to the next run at once, without approval, so only a lead or owner of the project may save. Never put keys, prices or provider caps in the file.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleWorkflowWrite)
	mcp.AddTool(srv, &mcp.Tool{Name: "workflow_check", Description: "Check a workflow file without saving it and without calling a model: unknown keys, kinds of result and aliases are errors; the answer says what the workflow would read, write and cost at most.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleWorkflowCheck)
}

func (s *Server) workflowProject(explicit string) string {
	return s.runbookAxes(explicit).Project
}

func (s *Server) handleWorkflowGet(_ context.Context, _ *mcp.CallToolRequest, in WorkflowGetInput) (*mcp.CallToolResult, WorkflowGetOutput, error) {
	project := s.workflowProject(in.Project)
	if project == "" {
		return nil, WorkflowGetOutput{}, fmt.Errorf("no project: name one with project")
	}
	b := s.builder().WithProject(project)
	if strings.TrimSpace(in.Name) == "" {
		list, err := s.client.ListWorkflows(project)
		if err != nil {
			return nil, WorkflowGetOutput{}, err
		}
		if len(list) == 0 {
			return textResult("No workflows in " + project + ". " + b.Workflows()), WorkflowGetOutput{}, nil
		}
		var out strings.Builder
		for _, d := range list {
			state := "on"
			if !d.Enabled {
				state = "off"
			}
			fmt.Fprintf(&out, "- %s [v%d, %s, %s, %s]: %s %s\n", d.Name, d.Version, state, d.Event, d.Uses, d.Summary, b.Workflow(d.Name))
		}
		return textResult(out.String()), WorkflowGetOutput{List: list}, nil
	}
	d, err := s.client.GetWorkflow(project, strings.TrimSpace(in.Name), in.Versions)
	if err != nil {
		return nil, WorkflowGetOutput{}, err
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%s version %d (%s) %s\n\n%s", d.Workflow.Name, d.Workflow.Version, map[bool]string{true: "on", false: "off"}[d.Workflow.Enabled], b.Workflow(d.Workflow.Name), d.Workflow.Text)
	for _, v := range d.Versions {
		fmt.Fprintf(&out, "\nversion %d by person:%d at %s via %s %s", v.Version, v.Author, v.CreatedAt, v.Via, v.Note)
	}
	if !d.CanEdit {
		out.WriteString("\n\nYou may read this workflow; only a lead or owner of the project changes it.")
	}
	return textResult(out.String()), WorkflowGetOutput{Workflow: &d.Workflow, Versions: d.Versions, CanEdit: d.CanEdit}, nil
}

func (s *Server) handleWorkflowWrite(_ context.Context, _ *mcp.CallToolRequest, in WorkflowWriteInput) (*mcp.CallToolResult, WorkflowWriteOutput, error) {
	project, err := s.writeTarget(in.Project)
	if err != nil {
		return nil, WorkflowWriteOutput{}, err
	}
	res, err := s.client.SaveWorkflow(project, strings.TrimSpace(in.Name), in.Text, in.Note, in.Base)
	if err != nil {
		return nil, WorkflowWriteOutput{}, err
	}
	out := WorkflowWriteOutput{Check: res.Check}
	if res.Saved == nil {
		out.Message = "not saved, the file has errors:\n" + problemLines(res.Check.Errors)
		return textResult(out.Message), out, nil
	}
	out.Version, out.Changed = res.Saved.Def.Version, res.Saved.Changed
	switch {
	case res.Saved.Created:
		out.Message = fmt.Sprintf("%s created, version %d; it applies to the next run", in.Name, out.Version)
	case res.Saved.Changed:
		out.Message = fmt.Sprintf("%s saved as version %d; it applies to the next run", in.Name, out.Version)
	default:
		out.Message = fmt.Sprintf("%s unchanged, still version %d", in.Name, out.Version)
	}
	if len(res.Check.Warnings) > 0 {
		out.Message += "\nwarnings:\n" + problemLines(res.Check.Warnings)
	}
	return textResult(out.Message + "\n" + s.builder().WithProject(project).Workflow(in.Name)), out, nil
}

func (s *Server) handleWorkflowCheck(_ context.Context, _ *mcp.CallToolRequest, in WorkflowCheckInput) (*mcp.CallToolResult, workflow.CheckReport, error) {
	project := s.workflowProject(in.Project)
	var rep workflow.CheckReport
	var err error
	if project == "" {
		_, rep = workflow.Check(in.Text, workflow.Env{})
	} else if rep, err = s.client.CheckWorkflow(project, in.Text); err != nil {
		return nil, rep, err
	}
	return textResult(workflow.FormatReport(rep)), rep, nil
}

func problemLines(ps []workflow.Problem) string {
	var b strings.Builder
	for _, p := range ps {
		b.WriteString("- " + p.String() + "\n")
	}
	return b.String()
}
