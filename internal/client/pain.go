package client

import (
	"net/url"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/pain"
)

func painPath(id int64) string { return "/api/pains/" + strconv.FormatInt(id, 10) }

// CreatePain records a problem without a solution.
func (c *Client) CreatePain(project, title, body string, motivates []pain.Target, sessionID int64) (pain.Detail, error) {
	var out pain.Detail
	err := c.do("POST", "/api/pains", nil, map[string]any{
		"project": project, "title": title, "body": body, "motivates": motivates, "session_id": sessionID,
	}, &out)
	return out, err
}

func (c *Client) GetPain(id int64) (pain.Detail, error) {
	var out pain.Detail
	err := c.do("GET", painPath(id), nil, nil, &out)
	return out, err
}

func (c *Client) SearchPains(project, state, query string, requestID int64, limit int) ([]pain.Pain, error) {
	q := url.Values{"project": {project}}
	if state != "" {
		q.Set("state", state)
	}
	if query != "" {
		q.Set("q", query)
	}
	if requestID > 0 {
		q.Set("request_id", strconv.FormatInt(requestID, 10))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []pain.Pain
	err := c.do("GET", "/api/pains", q, nil, &out)
	return out, err
}

// SearchPainDetails is SearchPains with edges and history in one call.
func (c *Client) SearchPainDetails(project, state string, limit int) ([]pain.Detail, error) {
	q := url.Values{"project": {project}, "detail": {"1"}}
	if state != "" {
		q.Set("state", state)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []pain.Detail
	err := c.do("GET", "/api/pains", q, nil, &out)
	return out, err
}

func (c *Client) SetPainState(id int64, state, remaining string, ev pain.Evidence, note string, sessionID int64) (pain.Detail, error) {
	var out pain.Detail
	err := c.do("POST", painPath(id)+"/state", nil, map[string]any{
		"state": state, "remaining": remaining, "evidence_kind": ev.Kind, "evidence_ref": ev.Ref, "note": note, "session_id": sessionID,
	}, &out)
	return out, err
}

func (c *Client) CorrectPain(id int64, title, body *string, reason string) (pain.Detail, error) {
	var out pain.Detail
	err := c.do("PATCH", painPath(id), nil, map[string]any{"title": title, "body": body, "reason": reason}, &out)
	return out, err
}

func (c *Client) AddPainLink(id int64, target pain.Target, sessionID int64) (pain.Detail, error) {
	var out pain.Detail
	err := c.do("POST", painPath(id)+"/links", nil, map[string]any{
		"request_id": target.RequestID, "knowledge_id": target.KnowledgeID, "external_ref": target.ExternalRef, "session_id": sessionID,
	}, &out)
	return out, err
}

func (c *Client) RemovePainLink(linkID int64, reason string) error {
	return c.do("DELETE", "/api/pain-links/"+strconv.FormatInt(linkID, 10), url.Values{"reason": {reason}}, nil, nil)
}
