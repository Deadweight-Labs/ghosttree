// Package client talks to a ghosttree server over the REST API.
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type Client struct {
	cfg  config.Config
	http *http.Client
}

type SearchResult struct {
	Knowledge []store.Knowledge  `json:"knowledge"`
	Sessions  []store.SessionHit `json:"sessions"`
}

func New(cfg config.Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) Machine() string { return c.cfg.Machine }

// do performs a request; out may be a *string to capture a raw text body.
func (c *Client) do(method, path string, query url.Values, in, out any) error {
	u := strings.TrimSuffix(c.cfg.ServerURL, "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s %s: %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	switch o := out.(type) {
	case nil:
		return nil
	case *string:
		*o = string(raw)
		return nil
	default:
		return json.Unmarshal(raw, out)
	}
}

func axesQuery(ax scope.Axes) url.Values {
	q := url.Values{}
	if ax.Project != "" {
		q.Set("project", ax.Project)
	}
	if ax.Branch != "" {
		q.Set("branch", ax.Branch)
	}
	if ax.Machine != "" {
		q.Set("machine", ax.Machine)
	}
	return q
}

func (c *Client) Health() error {
	var res struct {
		OK bool `json:"ok"`
	}
	if err := c.do("GET", "/api/health", nil, nil, &res); err != nil {
		return err
	}
	if !res.OK {
		return fmt.Errorf("server reported not ok")
	}
	return nil
}

func (c *Client) UpsertSession(s store.Session) (int64, error) {
	var res struct {
		ID int64 `json:"id"`
	}
	err := c.do("POST", "/api/sessions", nil, s, &res)
	return res.ID, err
}

func (c *Client) AppendChunks(id int64, chunks []store.Chunk) error {
	body := map[string]any{"chunks": chunks}
	return c.do("POST", "/api/sessions/"+strconv.FormatInt(id, 10)+"/chunks", nil, body, nil)
}

func (c *Client) Sessions(filter scope.Axes, limit int) ([]store.Session, error) {
	q := axesQuery(filter)
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []store.Session
	err := c.do("GET", "/api/sessions", q, nil, &out)
	return out, err
}

func (c *Client) ReadSession(id int64, from, limit int) ([]store.Chunk, error) {
	q := url.Values{"from": {strconv.Itoa(from)}}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []store.Chunk
	err := c.do("GET", "/api/sessions/"+strconv.FormatInt(id, 10), q, nil, &out)
	return out, err
}

// RawSession returns a session's original transcript as newline-delimited JSON.
func (c *Client) RawSession(id int64) (string, error) {
	var raw string
	err := c.do("GET", "/api/sessions/"+strconv.FormatInt(id, 10)+"/raw", nil, nil, &raw)
	return raw, err
}

// Remember writes a knowledge entry. When k.Scope is empty, autoCtx drives the
// server-side write defaults.
func (c *Client) Remember(k store.Knowledge, autoCtx scope.Axes) (store.Knowledge, error) {
	body := map[string]any{
		"type": k.Type, "title": k.Title, "body": k.Body,
		"scope": k.Scope, "confidence": k.Confidence, "status": k.Status,
		"harness": k.Harness, "session_ref": k.SessionRef,
		"auto_scope": map[string]any{"context": autoCtx},
	}
	var saved store.Knowledge
	err := c.do("POST", "/api/knowledge", nil, body, &saved)
	return saved, err
}

// PendingEntry mirrors the server's pending payload: the entry plus the
// evidence and recurrence a human needs in order to judge it.
type PendingEntry struct {
	Knowledge  store.Knowledge  `json:"knowledge"`
	Evidence   []store.Evidence `json:"evidence"`
	Recurrence int              `json:"recurrence"`
}

func (c *Client) Pending(limit int) ([]PendingEntry, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []PendingEntry
	err := c.do("GET", "/api/knowledge/pending", q, nil, &out)
	return out, err
}

func (c *Client) PatchKnowledge(id int64, patch map[string]string) error {
	return c.do("PATCH", "/api/knowledge/"+strconv.FormatInt(id, 10), nil, patch, nil)
}

func (c *Client) Knowledge(ax scope.Axes) ([]store.Knowledge, error) {
	var out []store.Knowledge
	err := c.do("GET", "/api/knowledge", axesQuery(ax), nil, &out)
	return out, err
}

func (c *Client) Search(q, kind string, filter scope.Axes, limit int) (SearchResult, error) {
	return c.search(q, kind, filter, limit, false)
}

// SearchUnion searches knowledge along the scope union of the given context
// instead of matching the axes exactly.
func (c *Client) SearchUnion(q, kind string, ax scope.Axes, limit int) (SearchResult, error) {
	return c.search(q, kind, ax, limit, true)
}

func (c *Client) search(q, kind string, filter scope.Axes, limit int, union bool) (SearchResult, error) {
	query := axesQuery(filter)
	query.Set("q", q)
	if union {
		query.Set("scope", "union")
	}
	if kind != "" {
		query.Set("kind", kind)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	var out SearchResult
	err := c.do("GET", "/api/search", query, nil, &out)
	return out, err
}

func (c *Client) Bootstrap(ax scope.Axes, budget int) (string, error) {
	q := axesQuery(ax)
	if budget > 0 {
		q.Set("budget", strconv.Itoa(budget))
	}
	var md string
	err := c.do("GET", "/api/context/bootstrap", q, nil, &md)
	return md, err
}
