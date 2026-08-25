package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type RequestSearchInput struct {
	Query  string `json:"query" jsonschema:"the current task or outcome to match against the request ledger"`
	State  string `json:"state,omitempty" jsonschema:"open, done, or dropped; defaults to open"`
	Type   string `json:"type,omitempty" jsonschema:"feature, change, bug, or investigation"`
	Cursor string `json:"cursor,omitempty" jsonschema:"opaque cursor from a previous result"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum results, default 10 and maximum 25"`
}

// RequestSearchOutput is a list to choose from, not the backlog itself.
//
// It used to carry every hit in full. Twenty-four requests came to 64,462
// characters and exceeded what a tool result may return, so the first thing a
// fresh agent does — see what is open — failed outright. The descriptions are
// long because they carry problem, evidence, trade-off and approach, which is
// what makes them worth having; the fix is not to write less but to send the
// list and let request_get answer for one entry. Same reasoning as REQ-83,
// which fixed the mutation replies and left the search path alone.
type RequestSearchOutput struct {
	Results        []RequestListItem `json:"results"`
	Interpretation string            `json:"interpretation"`
	NextCursor     string            `json:"next_cursor,omitempty"`
	// Truncated says the list was cut. A shortened answer that looks complete
	// is worse than a short one.
	Truncated bool `json:"truncated,omitempty"`
}

// RequestListItem carries what it takes to pick one and nothing else.
type RequestListItem struct {
	ID           int64  `json:"id"`
	Type         string `json:"type"`
	Title        string `json:"title"`
	State        string `json:"state"`
	Priority     string `json:"priority,omitempty"`
	OpenCriteria int    `json:"open_criteria"`
	// Handoff is the last thing somebody said when they stopped working on it.
	// One line, because "where did this get to" is the question that decides
	// whether to pick it up.
	Handoff string `json:"handoff,omitempty"`
	// Sightings counts the independent sessions that voiced this wish. Absent
	// for anything a person wrote by hand, where the description is the source.
	Sightings int `json:"sightings,omitempty"`
}

// maxListChars bounds the whole answer. It is enforced while building the list
// rather than checked afterwards, because a check that fires after serialising
// has already produced the thing it was meant to prevent.
const maxListChars = 6000

func listFromPage(page requestdomain.SearchPage) RequestSearchOutput {
	out := RequestSearchOutput{Results: []RequestListItem{}, NextCursor: page.NextCursor}
	size := 0
	for _, hit := range page.Results {
		item := RequestListItem{
			ID: hit.Request.ID, Type: hit.Request.Type, Title: hit.Request.Title,
			State: hit.Request.State, Priority: hit.Request.Priority,
			OpenCriteria: hit.OpenCriteria, Handoff: firstLine(hit.LatestHandoff, 160),
			Sightings: hit.Sightings,
		}
		size += len(item.Title) + len(item.Handoff) + 64
		if size > maxListChars && len(out.Results) > 0 {
			out.Truncated = true
			break
		}
		out.Results = append(out.Results, item)
	}
	return out
}

// firstLine keeps a handoff to one readable line, cut on a rune boundary.
func firstLine(s string, max int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
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
	Action         string `json:"action" jsonschema:"criterion_add, criterion_met, criterion_waive, complete, drop, relation_add, relation_remove, or correct"`
	CriterionID    int64  `json:"criterion_id,omitempty" jsonschema:"criterion for met or waive"`
	Description    string `json:"description,omitempty" jsonschema:"new criterion description for criterion_add; the replacement description for correct"`
	EvidenceKind   string `json:"evidence_kind,omitempty" jsonschema:"commit, test, file, decision, session, or url"`
	EvidenceRef    string `json:"evidence_ref,omitempty" jsonschema:"concrete evidence reference"`
	Reason         string `json:"reason,omitempty" jsonschema:"why: required for drop, for correct, and for relation_remove"`
	Title          string `json:"title,omitempty" jsonschema:"replacement title for correct"`
	Type           string `json:"type,omitempty" jsonschema:"replacement type for correct: feature, change, bug, or investigation"`
	Priority       string `json:"priority,omitempty" jsonschema:"replacement priority for correct"`
	RelationID     int64  `json:"relation_id,omitempty" jsonschema:"relation to remove; request_get lists every relation with its id. Pass request_id alongside it — that is the request the edge hangs off"`
	RelationKind   string `json:"relation_kind,omitempty" jsonschema:"parent, related, blocks, duplicates, supersedes, knowledge, or external. The edge is read subject first: request_id <kind> other_request_id. \"blocks\" therefore means request_id blocks other_request_id — the one named in request_id is the one that has to be finished first. Same for parent: request_id is the child, other_request_id is the parent."`
	OtherRequestID int64  `json:"other_request_id,omitempty" jsonschema:"the object of the relation, read after the kind: request_id <kind> other_request_id"`
	KnowledgeID    int64  `json:"knowledge_id,omitempty"`
	ExternalRef    string `json:"external_ref,omitempty"`
}

// correctionPatch collects the replacement values a correct action carries.
// Only the fields actually given are sent, so naming one does not blank the
// others.
func (in RequestProgressInput) correctionPatch() map[string]string {
	patch := map[string]string{}
	for field, value := range map[string]string{
		"title": in.Title, "description": in.Description, "type": in.Type, "priority": in.Priority,
	} {
		if value != "" {
			patch[field] = value
		}
	}
	return patch
}

func requestResult(v any) *mcp.CallToolResult {
	raw, _ := json.Marshal(v)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}
}

func requestDetailResult(detail requestdomain.Detail, concise bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: renderRequestDetail(detail, concise)}}}
}

// countSessions zählt die unabhängigen Sitzungen, nicht die Zitate: zwei Sätze
// aus derselben Sitzung sind ein Mal geäussert, nicht zwei.
func countSessions(sightings []requestdomain.Sighting) int {
	seen := map[int64]bool{}
	for _, sighting := range sightings {
		seen[sighting.SessionID] = true
	}
	return len(seen)
}

func pluralSessions(n int) string {
	if n == 1 {
		return "1 session"
	}
	return fmt.Sprintf("%d separate sessions", n)
}

func renderRequestDetail(detail requestdomain.Detail, concise bool) string {
	var b strings.Builder
	r := detail.Request
	fmt.Fprintf(&b, "# %s — %s\n", r.HumanID(), r.Title)
	fmt.Fprintf(&b, "type:%s state:%s", r.Type, r.State)
	if r.Priority != "" {
		fmt.Fprintf(&b, " priority:%s", r.Priority)
	}
	b.WriteString("\n\nDescription\n\n")
	b.WriteString(r.Description)
	// Direkt unter der Beschreibung, weil das der Punkt ist, an dem sie beurteilt
	// wird: bei einem destillierten Eintrag ist sie Modelltext, und erst der
	// Wortlaut daneben trennt "die KI behauptet das" von "das wurde gesagt". Auch
	// in der knappen Form — sie sind kurz, und ohne sie kostet jede Beurteilung
	// einen Griff ins Transkript. Handgeschriebene Einträge haben keine.
	if len(detail.Sightings) > 0 {
		fmt.Fprintf(&b, "\n\nVoiced in %s\n", pluralSessions(countSessions(detail.Sightings)))
		for _, sighting := range detail.Sightings {
			fmt.Fprintf(&b, "- session %d", sighting.SessionID)
			if sighting.At != "" {
				fmt.Fprintf(&b, " (%s)", sighting.At)
			}
			fmt.Fprintf(&b, ": %q\n", sighting.Quote)
		}
	}
	b.WriteString("\nCriteria\n")
	for _, criterion := range detail.Criteria {
		fmt.Fprintf(&b, "- %s [%s] %s\n", criterion.HumanID(), criterion.State, criterion.Description)
		if concise {
			continue
		}
		for _, evidence := range criterion.Evidence {
			fmt.Fprintf(&b, "  - evidence %s: %s\n", evidence.Kind, evidence.Ref)
		}
	}
	if len(detail.Relations) > 0 {
		b.WriteString("\nRelations\n")
		for _, relation := range detail.Relations {
			fmt.Fprintf(&b, "- #%d %s", relation.ID, relation.Kind)
			if relation.OtherRequestID != 0 {
				fmt.Fprintf(&b, " REQ-%d", relation.OtherRequestID)
			}
			if relation.KnowledgeID != 0 {
				fmt.Fprintf(&b, " knowledge #%d", relation.KnowledgeID)
			}
			if relation.ExternalRef != "" {
				fmt.Fprintf(&b, " %s", relation.ExternalRef)
			}
			b.WriteByte('\n')
		}
	}
	if len(detail.Work) > 0 {
		b.WriteString("\nWork\n")
		for _, work := range detail.Work {
			fmt.Fprintf(&b, "- #%d %s %s", work.ID, work.Role, work.State)
			if work.Summary != "" {
				fmt.Fprintf(&b, " — %s", work.Summary)
			}
			b.WriteByte('\n')
		}
	}
	if concise {
		b.WriteString("\nconcise omits activity, older work, and criterion evidence; use response_format=detailed for the full history.\n")
		return b.String()
	}
	if len(detail.Activity) > 0 {
		b.WriteString("\nActivity\n")
		for _, activity := range detail.Activity {
			fmt.Fprintf(&b, "- %s %s", activity.Kind, activity.Data)
			if activity.Person != "" {
				fmt.Fprintf(&b, " (%s)", activity.Person)
			}
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (s *Server) handleRequestSearch(_ context.Context, _ *mcp.CallToolRequest, in RequestSearchInput) (*mcp.CallToolResult, RequestSearchOutput, error) {
	state := in.State
	if state == "" {
		state = "open"
	}
	intent := store.ClassifySearch(in.Query)
	if intent == store.SearchInterrupted {
		threads, err := s.client.InterruptedWork(scope.Axes{Project: s.ctxAxes.Project}, s.sessionRef)
		out := RequestSearchOutput{Results: []RequestListItem{}, Interpretation: string(store.SearchInterrupted)}
		for _, thread := range threads {
			out.Results = append(out.Results, RequestListItem{ID: thread.RequestID, Type: thread.Type,
				Title: thread.Title, State: "open", Priority: thread.Priority,
				OpenCriteria: thread.OpenCriteria, Handoff: firstLine(thread.Handoff, 160)})
		}
		return requestResult(out), out, err
	}
	query := in.Query
	interpretation := "full_text_search"
	if intent == store.SearchInventory {
		query = ""
		interpretation = "open_request_inventory"
	}
	page, err := s.client.SearchRequests(requestdomain.SearchFilter{Scope: scope.Axes{Project: s.ctxAxes.Project}, Query: query, State: state, Type: in.Type, Cursor: in.Cursor, Limit: in.Limit})
	out := listFromPage(page)
	out.Interpretation = interpretation
	return requestResult(out), out, err
}

func (s *Server) handleRequestGet(_ context.Context, _ *mcp.CallToolRequest, in RequestGetInput) (*mcp.CallToolResult, RequestDetailOutput, error) {
	detail, err := s.client.GetRequest(in.RequestID)
	concise := in.ResponseFormat != "detailed"
	if concise {
		detail.Activity = nil
		if len(detail.Work) > 1 {
			detail.Work = detail.Work[:1]
		}
		for i := range detail.Criteria {
			detail.Criteria[i].Evidence = nil
		}
	}
	out := RequestDetailOutput{Detail: detail}
	return requestDetailResult(detail, concise), out, err
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
	if in.Action == "relation_remove" && in.RelationID == 0 {
		return nil, RequestChangeOutput{}, fmt.Errorf("relation_remove needs relation_id; request_get lists each relation with its id")
	}
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
	case "relation_remove":
		err = s.client.RemoveRequestRelation(in.RelationID, in.Reason)
	case "correct":
		err = s.client.CorrectRequest(in.RequestID, in.correctionPatch(), in.Reason)
	default:
		err = fmt.Errorf("unknown request progress action %q; use criterion_add, criterion_met, criterion_waive, complete, drop, relation_add, relation_remove, or correct", in.Action)
	}
	if err != nil {
		return nil, RequestChangeOutput{}, err
	}
	detail, err := s.client.GetRequest(in.RequestID)
	out := changeOutput(detail, in.Action, in.CriterionID)
	return requestResult(out), out, err
}
