package client

import (
	"strconv"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

func (c *Client) CreateRequest(in requestdomain.CreateInput) (requestdomain.Detail, error) {
	r := in.Request
	body := map[string]any{
		"type": r.Type, "title": r.Title, "description": r.Description, "priority": r.Priority,
		"project": r.Scope.Project, "branch": r.Scope.Branch, "machine": r.Scope.Machine,
		"origin": r.Origin, "session_ref": r.SessionRef,
		"criteria": in.Criteria, "idempotency_key": in.IdempotencyKey,
	}
	var out requestdomain.Detail
	err := c.do("POST", "/api/requests", nil, body, &out)
	return out, err
}

func (c *Client) SearchRequests(filter requestdomain.SearchFilter) (requestdomain.SearchPage, error) {
	q := axesQuery(filter.Scope)
	if filter.Query != "" {
		q.Set("q", filter.Query)
	}
	if filter.State != "" {
		q.Set("state", filter.State)
	}
	if filter.Type != "" {
		q.Set("type", filter.Type)
	}
	if filter.Cursor != "" {
		q.Set("cursor", filter.Cursor)
	}
	if filter.Limit > 0 {
		q.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.FullDescription {
		q.Set("full", "1")
	}
	var out requestdomain.SearchPage
	err := c.do("GET", "/api/requests/search", q, nil, &out)
	return out, err
}

func (c *Client) GetRequest(id int64) (requestdomain.Detail, error) {
	var out requestdomain.Detail
	err := c.do("GET", "/api/requests/"+strconv.FormatInt(id, 10), nil, nil, &out)
	return out, err
}

func (c *Client) CompleteRequest(id int64, evidence requestdomain.Evidence) error {
	body := map[string]string{"evidence_kind": evidence.Kind, "evidence_ref": evidence.Ref}
	return c.do("POST", "/api/requests/"+strconv.FormatInt(id, 10)+"/complete", nil, body, nil)
}

func (c *Client) StartRequestWork(requestID, sessionID int64, role string) (requestdomain.Work, []string, error) {
	var out struct {
		Work     requestdomain.Work `json:"work"`
		Warnings []string           `json:"warnings"`
	}
	err := c.do("POST", "/api/requests/"+strconv.FormatInt(requestID, 10)+"/work", nil,
		map[string]any{"session_id": sessionID, "role": role}, &out)
	return out.Work, out.Warnings, err
}

func (c *Client) FinishRequestWork(workID int64, state, summary string) (requestdomain.Work, error) {
	var out requestdomain.Work
	err := c.do("PATCH", "/api/request-work/"+strconv.FormatInt(workID, 10), nil,
		map[string]string{"state": state, "summary": summary}, &out)
	return out, err
}

func (c *Client) AddRequestCriterion(requestID int64, description string) (requestdomain.Criterion, error) {
	var out requestdomain.Criterion
	err := c.do("POST", "/api/requests/"+strconv.FormatInt(requestID, 10)+"/criteria", nil,
		map[string]string{"description": description}, &out)
	return out, err
}

func (c *Client) SetRequestCriterion(criterionID int64, state string, evidence requestdomain.Evidence) error {
	return c.do("PATCH", "/api/criteria/"+strconv.FormatInt(criterionID, 10), nil,
		map[string]string{"state": state, "evidence_kind": evidence.Kind, "evidence_ref": evidence.Ref}, nil)
}

func (c *Client) DropRequest(requestID int64, reason string) error {
	return c.do("POST", "/api/requests/"+strconv.FormatInt(requestID, 10)+"/drop", nil,
		map[string]string{"reason": reason}, nil)
}

// CorrectRequest changes what a request says and records why. The reason
// travels with the change rather than beside it, so a correction cannot be made
// silently.
func (c *Client) CorrectRequest(requestID int64, patch map[string]string, reason string) error {
	body := struct {
		Patch  map[string]string `json:"patch"`
		Reason string            `json:"reason"`
	}{Patch: patch, Reason: reason}
	return c.do("PATCH", "/api/requests/"+strconv.FormatInt(requestID, 10), nil, body, nil)
}

func (c *Client) RemoveRequestRelation(relationID int64, reason string) error {
	return c.do("DELETE", "/api/request-relations/"+strconv.FormatInt(relationID, 10), nil,
		map[string]string{"reason": reason}, nil)
}

func (c *Client) AddRequestRelation(requestID int64, relation requestdomain.Relation) (requestdomain.Relation, error) {
	var out requestdomain.Relation
	err := c.do("POST", "/api/requests/"+strconv.FormatInt(requestID, 10)+"/relations", nil, relation, &out)
	return out, err
}
