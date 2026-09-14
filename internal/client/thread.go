package client

import (
	"fmt"
	"net/url"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func (c *Client) CreateThread(t store.Thread) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	err := c.do("POST", "/api/threads", nil, t, &out)
	return out.ID, err
}

func (c *Client) SearchThreads(project, query string, includeArchived bool, limit int) ([]store.Thread, error) {
	q := url.Values{}
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

func (c *Client) ThreadByID(id int64) (store.Thread, error) {
	var out store.Thread
	err := c.do("GET", fmt.Sprintf("/api/threads/%d", id), nil, nil, &out)
	return out, err
}

func (c *Client) ThreadsForObject(kind, id string) ([]store.Thread, error) {
	q := url.Values{}
	q.Set("kind", kind)
	q.Set("id", id)
	var out []store.Thread
	err := c.do("GET", "/api/threads/for", q, nil, &out)
	return out, err
}

func (c *Client) SetThreadState(id int64, state string) error {
	return c.do("POST", fmt.Sprintf("/api/threads/%d/state", id), nil,
		map[string]any{"state": state}, nil)
}

func (c *Client) SetThreadArchived(id int64, archived bool) error {
	return c.do("POST", fmt.Sprintf("/api/threads/%d/state", id), nil,
		map[string]any{"archived": archived}, nil)
}

func (c *Client) LinkThread(l store.ThreadLink) error {
	return c.do("POST", fmt.Sprintf("/api/threads/%d/links", l.ThreadID), nil, l, nil)
}

func (c *Client) ThreadLinks(id int64) ([]store.ThreadLink, error) {
	var out []store.ThreadLink
	err := c.do("GET", fmt.Sprintf("/api/threads/%d/links", id), nil, nil, &out)
	return out, err
}

func (c *Client) PutThreadSummary(s store.ThreadSummary) (int, error) {
	var out struct {
		Revision int `json:"revision"`
	}
	err := c.do("POST", fmt.Sprintf("/api/threads/%d/summary", s.ThreadID), nil, s, &out)
	return out.Revision, err
}

// ThreadSummary liefert die jüngste Karte. Fehlt sie, antwortet der Server
// mit 204 und der Aufrufer bekommt die Nullfassung ohne Fehler: ein Thread
// ohne Karte ist lesbar, nur roh.
func (c *Client) ThreadSummary(id int64) (store.ThreadSummary, error) {
	var out store.ThreadSummary
	err := c.do("GET", fmt.Sprintf("/api/threads/%d/summary", id), nil, nil, &out)
	return out, err
}

func (c *Client) PutThreadOutcome(o store.ThreadOutcome) error {
	return c.do("POST", fmt.Sprintf("/api/threads/%d/outcomes", o.ThreadID), nil, o, nil)
}

func (c *Client) ThreadOutcomes(id int64) ([]store.ThreadOutcome, error) {
	var out []store.ThreadOutcome
	err := c.do("GET", fmt.Sprintf("/api/threads/%d/outcomes", id), nil, nil, &out)
	return out, err
}

// TouchThread schreibt die letzte Aktivität fort. Getrennt vom Beitrag,
// weil ein Beitrag über die Nachrichtenroute läuft und der Thread davon
// sonst nichts erführe — ein lebhaft diskutierter Thread erschiene dann
// nach 14 Tagen als ruhend.
func (c *Client) TouchThread(id int64) error {
	return c.do("POST", fmt.Sprintf("/api/threads/%d/touch", id), nil, nil, nil)
}
