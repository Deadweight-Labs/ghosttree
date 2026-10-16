package mcpserver

import (
	"context"
	"fmt"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type HumanTaskCreateInput struct {
	Project     string `json:"project,omitempty" jsonschema:"the project when it is not the session's own: a remote like github.com/owner/repo or a short name that is unique among the projects you can write to"`
	Kind        string `json:"kind" jsonschema:"secret, file, input or action"`
	Title       string `json:"title" jsonschema:"what the human has to do, in a few words"`
	Description string `json:"description,omitempty" jsonschema:"why it is needed and anything the human has to know"`
	Location    string `json:"location,omitempty" jsonschema:"where it should end up: a path, a vault entry, an env file. Required for secret and file. Never the value itself"`
	CheckHint   string `json:"check_hint,omitempty" jsonschema:"how you will check it is done, for example that the file exists or the entry is in the vault"`
	RequestID   int64  `json:"request_id,omitempty" jsonschema:"the request it hangs on; omit to use the request this session works on"`
	WorkID      int64  `json:"work_id,omitempty" jsonschema:"the work from request_start_work, so the handoff travels with the task"`
	Assignee    string `json:"assignee,omitempty" jsonschema:"the name of a project member; omit to leave it to whoever can"`
}

type HumanTaskListInput struct {
	Project   string `json:"project,omitempty" jsonschema:"the project when it is not the session's own"`
	State     string `json:"state,omitempty" jsonschema:"open, done, confirmed, dropped, pending (open and done, the default) or all"`
	RequestID int64  `json:"request_id,omitempty" jsonschema:"only tasks of this request"`
	Mine      bool   `json:"mine,omitempty" jsonschema:"only tasks assigned to you or to nobody and still open"`
}

type HumanTaskResolveInput struct {
	ID     int64  `json:"id" jsonschema:"the task number, TASK-n"`
	Action string `json:"action" jsonschema:"confirm after you checked the task against its check hint; reopen when it is not what was asked; drop when it is no longer needed"`
	Note   string `json:"note,omitempty" jsonschema:"required for reopen and drop: why"`
}

type HumanTaskOutput struct {
	Ref       string `json:"ref"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Location  string `json:"location,omitempty"`
	CheckHint string `json:"check_hint,omitempty"`
	RequestID int64  `json:"request_id"`
	Assignee  string `json:"assignee,omitempty"`
	DoneNote  string `json:"done_note,omitempty"`
	DoneVia   string `json:"done_via,omitempty"`
	StateNote string `json:"state_note,omitempty"`
}

type HumanTaskListOutput struct {
	Tasks []HumanTaskOutput `json:"tasks"`
}

func taskOutput(t humantask.Task) HumanTaskOutput {
	return HumanTaskOutput{Ref: t.HumanID(), Kind: t.Kind, Title: t.Title, State: t.State, Location: t.Location,
		CheckHint: t.CheckHint, RequestID: t.RequestID, Assignee: t.Assignee, DoneNote: t.DoneNote, DoneVia: t.DoneVia, StateNote: t.StateNote}
}

// currentSessionID finds the session this process belongs to the way
// request_start_work does: the newest recently active session of this project,
// branch and machine. Zero when there is none; callers then go without.
func (s *Server) currentSessionID() int64 {
	sessions, err := s.client.Sessions(scope.Axes{Project: s.ctxAxes.Project}, 20)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-30 * time.Minute)
	for _, candidate := range sessions {
		seen, err := time.Parse(time.RFC3339Nano, candidate.LastSeenAt)
		if err == nil && seen.After(cutoff) && (s.ctxAxes.Branch == "" || candidate.Scope.Branch == s.ctxAxes.Branch) && (s.ctxAxes.Machine == "" || candidate.Scope.Machine == s.ctxAxes.Machine) {
			return candidate.ID
		}
	}
	return 0
}

func (s *Server) handleHumanTaskCreate(_ context.Context, _ *mcp.CallToolRequest, in HumanTaskCreateInput) (*mcp.CallToolResult, HumanTaskOutput, error) {
	project, err := s.writeTarget(in.Project)
	if err != nil {
		return nil, HumanTaskOutput{}, err
	}
	create := humantask.CreateInput{
		Project: project, Kind: in.Kind, Title: in.Title, Description: in.Description, Location: in.Location,
		CheckHint: in.CheckHint, RequestID: in.RequestID, WorkID: in.WorkID, Assignee: in.Assignee,
	}
	if in.RequestID == 0 && in.WorkID == 0 {
		create.SessionID = s.currentSessionID()
	}
	task, err := s.client.CreateHumanTask(create)
	if err != nil {
		return nil, HumanTaskOutput{}, err
	}
	out := taskOutput(task)
	return requestResult(out), out, nil
}

func (s *Server) handleHumanTaskList(_ context.Context, _ *mcp.CallToolRequest, in HumanTaskListInput) (*mcp.CallToolResult, HumanTaskListOutput, error) {
	project := s.ctxAxes.Project
	if in.Project != "" {
		var err error
		if project, err = s.writeTarget(in.Project); err != nil {
			return nil, HumanTaskListOutput{}, err
		}
	}
	if project == "" {
		return nil, HumanTaskListOutput{}, fmt.Errorf("this session has no project; name one")
	}
	list, err := s.client.ListHumanTasks(project, in.State, in.Mine, in.RequestID)
	if err != nil {
		return nil, HumanTaskListOutput{}, err
	}
	out := HumanTaskListOutput{Tasks: []HumanTaskOutput{}}
	for _, t := range list {
		out.Tasks = append(out.Tasks, taskOutput(t))
	}
	return requestResult(out), out, nil
}

func (s *Server) handleHumanTaskResolve(_ context.Context, _ *mcp.CallToolRequest, in HumanTaskResolveInput) (*mcp.CallToolResult, HumanTaskOutput, error) {
	session := s.currentSessionID()
	var task humantask.Task
	var err error
	switch in.Action {
	case "confirm":
		task, err = s.client.ConfirmHumanTask(in.ID, session)
	case "reopen":
		task, err = s.client.ReopenHumanTask(in.ID, in.Note, session)
	case "drop":
		task, err = s.client.DropHumanTask(in.ID, in.Note, session)
	default:
		return nil, HumanTaskOutput{}, fmt.Errorf("action must be confirm, reopen or drop; an agent never marks a task done")
	}
	if err != nil {
		return nil, HumanTaskOutput{}, err
	}
	out := taskOutput(task)
	return requestResult(out), out, nil
}

func (s *Server) registerHumanTaskTools(srv *mcp.Server) {
	closed := false
	additive := false
	mcp.AddTool(srv, &mcp.Tool{Name: "human_task_create", Description: "Ask the human for something only they can provide (a key, a file, a decision, an action) instead of saying it in chat, so it survives the session. Give where it should end up and how you will check it. Never write a secret or file contents into any text or location, only where it lives; text that looks like a token is rejected (a heuristic, not a guarantee: the duty stays with you). It hangs on a request: the one this session works on, or request_id. You never mark it done yourself." + targetHint, Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleHumanTaskCreate)
	mcp.AddTool(srv, &mcp.Tool{Name: "human_task_list", Description: "List human tasks of the project: what is still open and what the human marked done and awaits your check.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handleHumanTaskList)
	mcp.AddTool(srv, &mcp.Tool{Name: "human_task_resolve", Description: "After the human marked a task done: check it against its check hint, then confirm; or reopen it with a reason when it is not what was asked; or drop it when it is no longer needed. Done is the human's claim, not proof.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handleHumanTaskResolve)
}
