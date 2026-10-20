package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// PainTargetInput names what a pain motivates: exactly one of the three.
type PainTargetInput struct {
	RequestID   int64  `json:"request_id,omitempty" jsonschema:"a request this problem motivates"`
	KnowledgeID int64  `json:"knowledge_id,omitempty" jsonschema:"a knowledge entry, usually a decision, this problem motivates"`
	ExternalRef string `json:"external_ref,omitempty" jsonschema:"an issue URL or an idea as free text"`
}

func (t PainTargetInput) target() pain.Target {
	return pain.Target{RequestID: t.RequestID, KnowledgeID: t.KnowledgeID, ExternalRef: t.ExternalRef}
}

func targets(in []PainTargetInput) []pain.Target {
	var out []pain.Target
	for _, t := range in {
		out = append(out, t.target())
	}
	return out
}

type PainRecordInput struct {
	Project     string            `json:"project,omitempty" jsonschema:"the project this problem belongs to when it is not the session's own: a remote like github.com/owner/repo or a short name that is unique among the projects you can write to"`
	Title       string            `json:"title" jsonschema:"the problem in one line, without a solution"`
	Body        string            `json:"body,omitempty" jsonschema:"what goes wrong, for whom, and how you noticed. No solution here"`
	Motivates   []PainTargetInput `json:"motivates,omitempty" jsonschema:"requests, decisions or external references that exist because of this problem"`
	Explanation string            `json:"explanation,omitempty" jsonschema:"optional \"Kurz erklärt\" for the humans who will read this: two to four plain sentences, in the language the people of the project use. Say what it is about in everyday words (the mechanism, not the jargon), why it matters (who it affects, when), what has to be done or decided, and your recommendation. No markdown, no links, never a secret. You know the context the server does not; without it the server generates one later from the text alone"`
}

type PainGetInput struct {
	ID int64 `json:"id" jsonschema:"the pain number, PAIN-n"`
}

type PainSearchInput struct {
	Query     string `json:"query,omitempty" jsonschema:"words in the title or text"`
	State     string `json:"state,omitempty" jsonschema:"active, mitigated, resolved, obsolete or all; defaults to active and mitigated"`
	RequestID int64  `json:"request_id,omitempty" jsonschema:"only pains that motivate this request"`
	Limit     int    `json:"limit,omitempty" jsonschema:"maximum results, default 50"`
}

type PainUpdateInput struct {
	ID           int64             `json:"id" jsonschema:"the pain number, PAIN-n"`
	State        string            `json:"state,omitempty" jsonschema:"move to mitigated, resolved, obsolete or back to active"`
	Remaining    string            `json:"remaining,omitempty" jsonschema:"required for mitigated: what is still unsolved"`
	EvidenceKind string            `json:"evidence_kind,omitempty" jsonschema:"commit, test, file, decision, session, url or request; required for mitigated and resolved"`
	EvidenceRef  string            `json:"evidence_ref,omitempty" jsonschema:"concrete evidence reference; for request REQ-n, for decision #n"`
	Note         string            `json:"note,omitempty" jsonschema:"required for obsolete and for moving back to active: why"`
	Title        string            `json:"title,omitempty" jsonschema:"replacement title; needs reason"`
	Body         string            `json:"body,omitempty" jsonschema:"replacement text; needs reason"`
	Reason       string            `json:"reason,omitempty" jsonschema:"why the title or text changes, or why a link goes"`
	LinkAdd      []PainTargetInput `json:"link_add,omitempty" jsonschema:"add what this problem motivates"`
	LinkRemove   []int64           `json:"link_remove,omitempty" jsonschema:"ids of links to remove, from pain_get; needs reason"`
}

type PainLinkOut struct {
	ID     int64  `json:"id"`
	Target string `json:"target"`
}

type PainOutput struct {
	Ref       string `json:"ref"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Body      string `json:"body,omitempty"`
	Remaining string `json:"remaining,omitempty"`
	// Match is "semantic" for a pain that a search found by its meaning alone.
	Match   string        `json:"match,omitempty"`
	Links   []PainLinkOut `json:"links,omitempty"`
	History []string      `json:"history,omitempty"`
}

type PainListOutput struct {
	Pains []PainOutput `json:"pains"`
}

func painOutput(d pain.Detail, withHistory bool) PainOutput {
	out := PainOutput{Ref: d.Pain.HumanID(), Title: d.Pain.Title, State: d.Pain.State, Body: d.Pain.Body, Remaining: d.Pain.Remaining}
	for _, l := range d.Motivates {
		target := l.ExternalRef
		switch l.Kind {
		case "request":
			target = fmt.Sprintf("REQ-%d %s", l.RequestID, l.Title)
		case "knowledge":
			target = fmt.Sprintf("#%d %s", l.KnowledgeID, l.Title)
		}
		out.Links = append(out.Links, PainLinkOut{ID: l.ID, Target: target})
	}
	if withHistory {
		for _, e := range d.Events {
			line := e.CreatedAt + " " + e.Kind
			if e.ToState != "" {
				line += " " + e.FromState + "->" + e.ToState
			}
			if e.EvidenceKind != "" {
				line += " evidence " + e.EvidenceKind + ":" + e.EvidenceRef
			}
			if e.Remaining != "" {
				line += " remaining: " + e.Remaining
			}
			if e.Note != "" {
				line += " note: " + e.Note
			}
			out.History = append(out.History, line)
		}
	}
	return out
}

func (s *Server) handlePainRecord(_ context.Context, _ *mcp.CallToolRequest, in PainRecordInput) (*mcp.CallToolResult, PainOutput, error) {
	project, err := s.writeTarget(in.Project)
	if err != nil {
		return nil, PainOutput{}, err
	}
	d, err := s.client.CreatePainExplained(project, in.Title, in.Body, in.Explanation, targets(in.Motivates), s.currentSessionID())
	if err != nil {
		return nil, PainOutput{}, err
	}
	out := painOutput(d, false)
	return requestResult(out), out, nil
}

func (s *Server) handlePainGet(_ context.Context, _ *mcp.CallToolRequest, in PainGetInput) (*mcp.CallToolResult, PainOutput, error) {
	d, err := s.client.GetPain(in.ID)
	if err != nil {
		return nil, PainOutput{}, err
	}
	out := painOutput(d, true)
	return requestResult(out), out, nil
}

func (s *Server) handlePainSearch(_ context.Context, _ *mcp.CallToolRequest, in PainSearchInput) (*mcp.CallToolResult, PainListOutput, error) {
	if s.ctxAxes.Project == "" {
		return nil, PainListOutput{}, fmt.Errorf("this session has no project; pains belong to a project")
	}
	list, err := s.client.SearchPains(s.ctxAxes.Project, in.State, in.Query, in.RequestID, in.Limit)
	if err != nil {
		return nil, PainListOutput{}, err
	}
	out := PainListOutput{Pains: []PainOutput{}}
	for _, p := range list {
		out.Pains = append(out.Pains, PainOutput{Ref: p.HumanID(), Title: p.Title, State: p.State, Remaining: p.Remaining, Match: p.Match})
	}
	return requestResult(out), out, nil
}

func (s *Server) handlePainUpdate(_ context.Context, _ *mcp.CallToolRequest, in PainUpdateInput) (*mcp.CallToolResult, PainOutput, error) {
	// Check everything that could fail on the input alone before the first
	// write, so a rejected call has changed nothing.
	if in.State == "" && in.Title == "" && in.Body == "" && len(in.LinkAdd) == 0 && len(in.LinkRemove) == 0 {
		return nil, PainOutput{}, fmt.Errorf("nothing to update: give state, title, body, link_add or link_remove")
	}
	if (in.Title != "" || in.Body != "" || len(in.LinkRemove) > 0) && strings.TrimSpace(in.Reason) == "" {
		return nil, PainOutput{}, fmt.Errorf("correcting title or text and removing links need a reason; nothing was changed")
	}
	session := s.currentSessionID()
	var d pain.Detail
	var err error
	changed := false
	if in.State != "" {
		d, err = s.client.SetPainState(in.ID, in.State, in.Remaining, pain.Evidence{Kind: in.EvidenceKind, Ref: in.EvidenceRef}, in.Note, session)
		if err != nil {
			return nil, PainOutput{}, err
		}
		changed = true
	}
	if in.Title != "" || in.Body != "" {
		var title, body *string
		if in.Title != "" {
			title = &in.Title
		}
		if in.Body != "" {
			body = &in.Body
		}
		if d, err = s.client.CorrectPain(in.ID, title, body, in.Reason); err != nil {
			return nil, PainOutput{}, err
		}
		changed = true
	}
	for _, t := range in.LinkAdd {
		if d, err = s.client.AddPainLink(in.ID, t.target(), session); err != nil {
			return nil, PainOutput{}, err
		}
		changed = true
	}
	for _, id := range in.LinkRemove {
		if err = s.client.RemovePainLink(id, in.Reason); err != nil {
			return nil, PainOutput{}, err
		}
		changed = true
	}
	if !changed {
		return nil, PainOutput{}, fmt.Errorf("nothing to update: give state, title, body, link_add or link_remove")
	}
	if len(in.LinkRemove) > 0 || d.Pain.ID == 0 {
		if d, err = s.client.GetPain(in.ID); err != nil {
			return nil, PainOutput{}, err
		}
	}
	out := painOutput(d, false)
	return requestResult(out), out, nil
}

func (s *Server) registerPainTools(srv *mcp.Server) {
	closed := false
	additive := false
	mcp.AddTool(srv, &mcp.Tool{Name: "pain_record", Description: "Record a problem without a solution. If you already have a solution, write a request. A pain needs only a title; it has no criteria and is not finished by work, it is mitigated or resolved with evidence. Add explanation, a short plain-language note for the people who read the problems." + targetHint, Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handlePainRecord)
	mcp.AddTool(srv, &mcp.Tool{Name: "pain_get", Description: "Read one pain with its state, what is still unsolved, what it motivates and its history.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handlePainGet)
	mcp.AddTool(srv, &mcp.Tool{Name: "pain_search", Description: "List the current project's pains. Without a state it lists active and mitigated ones; a pain never ages out.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.handlePainSearch)
	mcp.AddTool(srv, &mcp.Tool{Name: "pain_update", Description: "Change a pain: move its state, correct title or text, add or remove what it motivates. mitigated needs remaining (what is still unsolved) and evidence; resolved needs evidence; obsolete and returning to active need a note. A finished request does not set a pain to resolved by itself: say so here with evidence.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive, OpenWorldHint: &closed}}, s.handlePainUpdate)
}
