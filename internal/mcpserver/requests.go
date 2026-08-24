package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type RequestSearchInput struct {
	Query          string `json:"query" jsonschema:"the current task or outcome to match against the request ledger"`
	State          string `json:"state,omitempty" jsonschema:"open, done, or dropped; defaults to open"`
	Type           string `json:"type,omitempty" jsonschema:"feature, change, bug, or investigation"`
	Cursor         string `json:"cursor,omitempty" jsonschema:"opaque cursor from a previous result"`
	Limit          int    `json:"limit,omitempty" jsonschema:"maximum results, default 10 and maximum 25"`
	ResponseFormat string `json:"response_format,omitempty" jsonschema:"concise or detailed; defaults to concise"`
}

type RequestSearchOutput struct {
	Page requestdomain.SearchPage `json:"page"`
}

type RequestGetInput struct {
	RequestID      int64  `json:"request_id" jsonschema:"numeric request identifier"`
	ResponseFormat string `json:"response_format,omitempty" jsonschema:"concise or detailed; defaults to concise"`
}

type RequestDetailOutput struct {
	Detail requestdomain.Detail `json:"detail"`
}

// CriterionRef is a criterion reduced to what an agent acts on: the id it will
// pass back, the number it reads in prose, and the state it is in now. The
// description is already in the request the agent just read.
type CriterionRef struct {
	State  string `json:"state"`
	ID     int64  `json:"id"`
	Number int    `json:"number"`
}

// RequestChangeOutput is what a mutation answers with. Returning the whole
// request instead — description, every criterion, every piece of evidence, the
// entire activity list — charges the agent for each status update out of the
// context it needs for the work itself. That makes neglecting the ledger the
// cheaper option, which is the opposite of what the ledger is for. Anyone who
// wants the full picture calls request_get, which exists for exactly that.
type RequestChangeOutput struct {
	State        string         `json:"state"`
	Changed      string         `json:"changed"`
	Criterion    *CriterionRef  `json:"criterion,omitempty"`
	Criteria     []CriterionRef `json:"criteria,omitempty"`
	RequestID    int64          `json:"request_id"`
	OpenCriteria int            `json:"open_criteria"`
}

func changeOutput(detail requestdomain.Detail, changed string, criterionID int64) RequestChangeOutput {
	out := RequestChangeOutput{RequestID: detail.Request.ID, State: detail.Request.State, Changed: changed}
	for _, c := range detail.Criteria {
		if c.State == "open" {
			out.OpenCriteria++
		}
		if c.ID == criterionID {
			ref := CriterionRef{ID: c.ID, Number: c.Number, State: c.State}
			out.Criterion = &ref
		}
	}
	return out
}

type RequestCreateInput struct {
	Type           string   `json:"type" jsonschema:"feature, change, bug, or investigation"`
	Title          string   `json:"title" jsonschema:"concise desired outcome"`
	Description    string   `json:"description" jsonschema:"requirements and context for the work"`
	Priority       string   `json:"priority,omitempty" jsonschema:"optional project priority"`
	Criteria       []string `json:"criteria" jsonschema:"observable acceptance criteria"`
	IdempotencyKey string   `json:"idempotency_key,omitempty" jsonschema:"stable retry key for this creation attempt"`
}

type RequestStartWorkInput struct {
	RequestID int64  `json:"request_id" jsonschema:"request to work on"`
	SessionID int64  `json:"session_id,omitempty" jsonschema:"current Ghosttree session identifier; omit to associate the newest matching active session"`
	Role      string `json:"role,omitempty" jsonschema:"primary or related; defaults to primary"`
}

type RequestWorkOutput struct {
	Work     requestdomain.Work `json:"work"`
	Warnings []string           `json:"warnings,omitempty"`
}

type RequestFinishWorkInput struct {
	WorkID  int64  `json:"work_id" jsonschema:"work association to finish"`
	State   string `json:"state" jsonschema:"paused, completed, or abandoned"`
	Summary string `json:"summary" jsonschema:"handoff covering achieved work, remaining work, and next step"`
}

type RequestProgressInput struct {
	RequestID      int64  `json:"request_id" jsonschema:"request being updated"`
	Action         string `json:"action" jsonschema:"criterion_add, criterion_met, criterion_waive, complete, drop, or relation_add"`
	CriterionID    int64  `json:"criterion_id,omitempty" jsonschema:"criterion for met or waive"`
	Description    string `json:"description,omitempty" jsonschema:"new criterion description"`
	EvidenceKind   string `json:"evidence_kind,omitempty" jsonschema:"commit, test, file, decision, session, or url"`
	EvidenceRef    string `json:"evidence_ref,omitempty" jsonschema:"concrete evidence reference"`
	Reason         string `json:"reason,omitempty" jsonschema:"reason for dropping a request"`
	RelationKind   string `json:"relation_kind,omitempty" jsonschema:"parent, related, blocks, duplicates, supersedes, knowledge, or external"`
	OtherRequestID int64  `json:"other_request_id,omitempty"`
	KnowledgeID    int64  `json:"knowledge_id,omitempty"`
	ExternalRef    string `json:"external_ref,omitempty"`
}

func requestResult(v any) *mcp.CallToolResult {
	raw, _ := json.Marshal(v)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}
}

func (s *Server) handleRequestSearch(_ context.Context, _ *mcp.CallToolRequest, in RequestSearchInput) (*mcp.CallToolResult, RequestSearchOutput, error) {
	state := in.State
	if state == "" {
		state = "open"
	}
	page, err := s.client.SearchRequests(requestdomain.SearchFilter{Scope: scope.Axes{Project: s.ctxAxes.Project}, Query: in.Query, State: state, Type: in.Type, Cursor: in.Cursor, Limit: in.Limit})
	out := RequestSearchOutput{Page: page}
	return requestResult(out), out, err
}

func (s *Server) handleRequestGet(_ context.Context, _ *mcp.CallToolRequest, in RequestGetInput) (*mcp.CallToolResult, RequestDetailOutput, error) {
	detail, err := s.client.GetRequest(in.RequestID)
	if in.ResponseFormat != "detailed" {
		detail.Activity = nil
		if len(detail.Work) > 1 {
			detail.Work = detail.Work[:1]
		}
	}
	out := RequestDetailOutput{Detail: detail}
	return requestResult(out), out, err
}

func (s *Server) handleRequestCreate(_ context.Context, _ *mcp.CallToolRequest, in RequestCreateInput) (*mcp.CallToolResult, RequestChangeOutput, error) {
	// Project only: a backlog entry belongs to the repository, not to the
	// branch or machine that happened to file it.
	ax := scope.Axes{Project: s.ctxAxes.Project}
	detail, err := s.client.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: in.Type, Title: in.Title, Description: in.Description, Priority: in.Priority, Scope: ax, Origin: "agent", SessionRef: "mcp",
	}, Criteria: in.Criteria, IdempotencyKey: in.IdempotencyKey})
	out := changeOutput(detail, "created", 0)
	// Creation is the one mutation that has to list its criteria: their ids
	// exist nowhere else yet, and without them the next step is a request_get
	// that this reply just saved.
	for _, c := range detail.Criteria {
		out.Criteria = append(out.Criteria, CriterionRef{ID: c.ID, Number: c.Number, State: c.State})
	}
	return requestResult(out), out, err
}

func (s *Server) handleRequestStartWork(_ context.Context, _ *mcp.CallToolRequest, in RequestStartWorkInput) (*mcp.CallToolResult, RequestWorkOutput, error) {
	role := in.Role
	if role == "" {
		role = "primary"
	}
	sessionID := in.SessionID
	if sessionID == 0 {
		sessions, err := s.client.Sessions(scope.Axes{Project: s.ctxAxes.Project}, 20)
		if err != nil {
			return nil, RequestWorkOutput{}, err
		}
		cutoff := time.Now().Add(-30 * time.Minute)
		for _, candidate := range sessions {
			seen, err := time.Parse(time.RFC3339Nano, candidate.LastSeenAt)
			if err == nil && seen.After(cutoff) && (s.ctxAxes.Branch == "" || candidate.Scope.Branch == s.ctxAxes.Branch) && (s.ctxAxes.Machine == "" || candidate.Scope.Machine == s.ctxAxes.Machine) {
				sessionID = candidate.ID
				break
			}
		}
		if sessionID == 0 {
			return nil, RequestWorkOutput{}, fmt.Errorf("no recently active session matches this project, branch, and machine; sync the collector or provide session_id")
		}
	}
	work, warnings, err := s.client.StartRequestWork(in.RequestID, sessionID, role)
	out := RequestWorkOutput{Work: work, Warnings: warnings}
	return requestResult(out), out, err
}

func (s *Server) handleRequestFinishWork(_ context.Context, _ *mcp.CallToolRequest, in RequestFinishWorkInput) (*mcp.CallToolResult, RequestWorkOutput, error) {
	work, err := s.client.FinishRequestWork(in.WorkID, in.State, in.Summary)
	out := RequestWorkOutput{Work: work}
	return requestResult(out), out, err
}

func (s *Server) handleRequestProgress(_ context.Context, _ *mcp.CallToolRequest, in RequestProgressInput) (*mcp.CallToolResult, RequestChangeOutput, error) {
	evidence := requestdomain.Evidence{Kind: in.EvidenceKind, Ref: in.EvidenceRef}
	var err error
	switch in.Action {
	case "criterion_add":
		_, err = s.client.AddRequestCriterion(in.RequestID, in.Description)
	case "criterion_met":
		err = s.client.SetRequestCriterion(in.CriterionID, "met", evidence)
	case "criterion_waive":
		err = s.client.SetRequestCriterion(in.CriterionID, "waived", evidence)
	case "complete":
		err = s.client.CompleteRequest(in.RequestID, evidence)
	case "drop":
		err = s.client.DropRequest(in.RequestID, in.Reason)
	case "relation_add":
		_, err = s.client.AddRequestRelation(in.RequestID, requestdomain.Relation{Kind: in.RelationKind, OtherRequestID: in.OtherRequestID, KnowledgeID: in.KnowledgeID, ExternalRef: in.ExternalRef})
	default:
		err = fmt.Errorf("unknown request progress action %q; use criterion_add, criterion_met, criterion_waive, complete, drop, or relation_add", in.Action)
	}
	if err != nil {
		return nil, RequestChangeOutput{}, err
	}
	detail, err := s.client.GetRequest(in.RequestID)
	out := changeOutput(detail, in.Action, in.CriterionID)
	return requestResult(out), out, err
}
