package client

import (
	"fmt"
	"net/url"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func agentQuery(agentExternalID string) url.Values {
	return url.Values{"agent_external_id": {agentExternalID}}
}

func (c *Client) CreateThread(t store.Thread, agentExternalID string) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	err := c.do("POST", "/api/threads", agentQuery(agentExternalID), t, &out)
	return out.ID, err
}

func (c *Client) SearchThreads(project, query string, includeArchived bool, limit int, agentExternalID string) ([]store.Thread, error) {
	q := agentQuery(agentExternalID)
	q.Set("project", project)
	if query != "" {
		q.Set("q", query)
	}
	if includeArchived {
		q.Set("archived", "1")
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []store.Thread
	err := c.do("GET", "/api/threads", q, nil, &out)
	return out, err
}

func (c *Client) PublicSearchThreads(project, query string, includeArchived bool, limit int) ([]store.Thread, error) {
	q := url.Values{"public_only": {"1"}, "project": {project}}
	if query != "" {
		q.Set("q", query)
	}
	if includeArchived {
		q.Set("archived", "1")
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []store.Thread
	err := c.do("GET", "/api/threads", q, nil, &out)
	return out, err
}

func (c *Client) ThreadByID(id int64, agentExternalID string) (store.Thread, error) {
	var out store.Thread
	err := c.do("GET", fmt.Sprintf("/api/threads/%d", id), agentQuery(agentExternalID), nil, &out)
	return out, err
}

func (c *Client) ThreadsForObject(kind, id, agentExternalID string) ([]store.Thread, error) {
	q := agentQuery(agentExternalID)
	q.Set("kind", kind)
	q.Set("id", id)
	var out []store.Thread
	err := c.do("GET", "/api/threads/for", q, nil, &out)
	return out, err
}

func (c *Client) SetThreadState(id int64, state, agentExternalID string) error {
	return c.do("POST", fmt.Sprintf("/api/threads/%d/state", id), agentQuery(agentExternalID),
		map[string]any{"state": state}, nil)
}

func (c *Client) SetThreadArchived(id int64, archived bool, agentExternalID string) error {
	return c.do("POST", fmt.Sprintf("/api/threads/%d/state", id), agentQuery(agentExternalID),
		map[string]any{"archived": archived}, nil)
}

func (c *Client) LinkThread(l store.ThreadLink, agentExternalID string) error {
	return c.do("POST", fmt.Sprintf("/api/threads/%d/links", l.ThreadID), agentQuery(agentExternalID), l, nil)
}

func (c *Client) ThreadLinks(id int64, agentExternalID string) ([]store.ThreadLink, error) {
	var out []store.ThreadLink
	err := c.do("GET", fmt.Sprintf("/api/threads/%d/links", id), agentQuery(agentExternalID), nil, &out)
	return out, err
}

func (c *Client) PublicThreadLinks(id int64) ([]store.ThreadLink, error) {
	var out []store.ThreadLink
	err := c.do("GET", fmt.Sprintf("/api/threads/%d/links", id), url.Values{"public_only": {"1"}}, nil, &out)
	return out, err
}

func (c *Client) PutThreadSummary(s store.ThreadSummary, agentExternalID string) (int, error) {
	var out struct {
		Revision int `json:"revision"`
	}
	err := c.do("POST", fmt.Sprintf("/api/threads/%d/summary", s.ThreadID), agentQuery(agentExternalID), s, &out)
	return out.Revision, err
}

// ThreadSummary liefert die jüngste Karte. Fehlt sie, antwortet der Server
// mit 204 und der Aufrufer bekommt die Nullfassung ohne Fehler: ein Thread
// ohne Karte ist lesbar, nur roh.
func (c *Client) ThreadSummary(id int64, agentExternalID string) (store.ThreadSummary, error) {
	var out store.ThreadSummary
	err := c.do("GET", fmt.Sprintf("/api/threads/%d/summary", id), agentQuery(agentExternalID), nil, &out)
	return out, err
}

func (c *Client) PublicThreadSummary(id int64) (store.ThreadSummary, error) {
	var out store.ThreadSummary
	err := c.do("GET", fmt.Sprintf("/api/threads/%d/summary", id), url.Values{"public_only": {"1"}}, nil, &out)
	return out, err
}

func (c *Client) PutThreadOutcome(o store.ThreadOutcome, agentExternalID string) error {
	return c.do("POST", fmt.Sprintf("/api/threads/%d/outcomes", o.ThreadID), agentQuery(agentExternalID), o, nil)
}

func (c *Client) ThreadOutcomes(id int64, agentExternalID string) ([]store.ThreadOutcome, error) {
	var out []store.ThreadOutcome
	err := c.do("GET", fmt.Sprintf("/api/threads/%d/outcomes", id), agentQuery(agentExternalID), nil, &out)
	return out, err
}

func (c *Client) PublicThreadOutcomes(id int64) ([]store.ThreadOutcome, error) {
	var out []store.ThreadOutcome
	err := c.do("GET", fmt.Sprintf("/api/threads/%d/outcomes", id), url.Values{"public_only": {"1"}}, nil, &out)
	return out, err
}

// TouchThread schreibt die letzte Aktivität fort. Getrennt vom Beitrag,
// weil ein Beitrag über die Nachrichtenroute läuft und der Thread davon
// sonst nichts erführe — ein lebhaft diskutierter Thread erschiene dann
// nach 14 Tagen als ruhend.
func (c *Client) TouchThread(id int64, agentExternalID string) error {
	return c.do("POST", fmt.Sprintf("/api/threads/%d/touch", id), agentQuery(agentExternalID), nil, nil)
}
