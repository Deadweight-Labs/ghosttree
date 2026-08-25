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

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type Client struct {
	cfg  config.Config
	http *http.Client
}

type SearchResult struct {
	Knowledge []store.Knowledge         `json:"knowledge"`
	Sessions  []store.SessionHit        `json:"sessions"`
	Requests  []requestdomain.SearchHit `json:"requests"`
}

type APIError struct {
	Status     int            `json:"-"`
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	Resolution string         `json:"resolution"`
	Details    map[string]any `json:"details,omitempty"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("HTTP %d: %s: %s", e.Status, e.Code, e.Message)
}

func New(cfg config.Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}}
}

// NewWithTimeout is for callers that sit in front of a person waiting. The
// relevance hook runs between pressing enter and the model seeing the prompt, so
// a server that is merely slow has to look the same as one that is absent.
func NewWithTimeout(cfg config.Config, d time.Duration) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: d}}
}

// Relevant returns knowledge the text gives a reason to deliver. An empty
// result is the usual answer.
func (c *Client) Relevant(text string, ax scope.Axes, limit int) (string, error) {
	q := axesQuery(ax)
	q.Set("q", text)
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var md string
	err := c.do("GET", "/api/context/relevant", q, nil, &md)
	return md, err
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
		var apiErr APIError
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Code != "" {
			apiErr.Status = resp.StatusCode
			return &apiErr
		}
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
	// Repeated rather than joined: a branch name may contain anything a ref
	// allows, and inventing a separator is inventing a way to split wrongly.
	for _, ancestor := range ax.Lineage {
		q.Add("lineage", ancestor)
	}
	if ax.AnyBranch {
		q.Set("any_branch", "1")
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
		"origin":  k.Origin,
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
	Knowledge         store.Knowledge          `json:"knowledge"`
	Evidence          []store.Evidence         `json:"evidence"`
	MigrationEvidence *store.MigrationEvidence `json:"migration_evidence,omitempty"`
	Recurrence        int                      `json:"recurrence"`
}

func (c *Client) Pending(project string, limit int) ([]PendingEntry, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if project != "" {
		q.Set("project", project)
	}
	var out []PendingEntry
	err := c.do("GET", "/api/knowledge/pending", q, nil, &out)
	return out, err
}

func (c *Client) PatchKnowledge(id int64, patch map[string]string) error {
	return c.do("PATCH", "/api/knowledge/"+strconv.FormatInt(id, 10), nil, patch, nil)
}

// SetRegressionCover records what guards a fixed defect: the test that would
// catch its return, or that none exists, or that there is nothing to test here.
func (c *Client) SetRegressionCover(id int64, state, test string) error {
	body := map[string]string{"state": state, "test": test}
	return c.do("PUT", "/api/knowledge/"+strconv.FormatInt(id, 10)+"/regression", nil, body, nil)
}

// RegressionGaps returns the fixes nothing guards, and how many entries nobody
// has judged yet. The second number keeps a short list from reading as all-clear.
func (c *Client) RegressionGaps(ax scope.Axes) ([]store.Knowledge, int, error) {
	var out struct {
		Gaps       []store.Knowledge `json:"gaps"`
		Unreviewed int               `json:"unreviewed"`
	}
	err := c.do("GET", "/api/knowledge/regression-gaps", axesQuery(ax), nil, &out)
	return out.Gaps, out.Unreviewed, err
}

// KnowledgeByID fetches one entry with its body intact, for the case where
// somebody asked for exactly this entry rather than for an overview.
func (c *Client) KnowledgeByID(id int64) (store.Knowledge, error) {
	var out store.Knowledge
	err := c.do("GET", "/api/knowledge/"+strconv.FormatInt(id, 10), nil, nil, &out)
	return out, err
}

func (c *Client) Knowledge(ax scope.Axes) ([]store.Knowledge, error) {
	var out []store.Knowledge
	err := c.do("GET", "/api/knowledge", axesQuery(ax), nil, &out)
	return out, err
}

func (c *Client) ProjectKnowledge(project string, includeArchived bool) ([]store.Knowledge, error) {
	q := axesQuery(scope.Axes{Project: project})
	if includeArchived {
		q.Set("include_archived", "1")
	}
	var out []store.Knowledge
	err := c.do("GET", "/api/knowledge", q, nil, &out)
	return out, err
}

func (c *Client) InsertMigrated(in store.MigratedEntry) (store.MigratedResult, error) {
	var out store.MigratedResult
	err := c.do("POST", "/api/migrated-knowledge", nil, in, &out)
	return out, err
}

func (c *Client) BeginMigration(project string, artifacts map[string]string) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	err := c.do("POST", "/api/migrations", nil, map[string]any{"project": project, "artifacts": artifacts}, &out)
	return out.ID, err
}

func (c *Client) CompleteMigration(id int64) error {
	return c.do("PUT", "/api/migrations/"+strconv.FormatInt(id, 10)+"/complete", nil, nil, nil)
}

func (c *Client) CompletedMigrationArtifacts(project string) (map[string][]string, error) {
	q := url.Values{"project": {project}}
	var out map[string][]string
	err := c.do("GET", "/api/migrations", q, nil, &out)
	return out, err
}

func (c *Client) Search(q, kind string, filter scope.Axes, limit int) (SearchResult, error) {
	return c.search(q, kind, filter, "", limit, false)
}

func (c *Client) SearchExcludingSession(q, kind string, filter scope.Axes, session string, limit int) (SearchResult, error) {
	return c.search(q, kind, filter, session, limit, false)
}

// SearchUnion searches knowledge along the scope union of the given context
// instead of matching the axes exactly.
func (c *Client) SearchUnion(q, kind string, ax scope.Axes, limit int) (SearchResult, error) {
	return c.search(q, kind, ax, "", limit, true)
}

func (c *Client) search(q, kind string, filter scope.Axes, excludeSession string, limit int, union bool) (SearchResult, error) {
	query := axesQuery(filter)
	query.Set("q", q)
	if union {
		query.Set("scope", "union")
	}
	if kind != "" {
		query.Set("kind", kind)
	}
	if excludeSession != "" {
		query.Set("exclude_session", excludeSession)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	var out SearchResult
	err := c.do("GET", "/api/search", query, nil, &out)
	return out, err
}

func (c *Client) InterruptedWork(ax scope.Axes, excludeSession string) ([]store.InterruptedThread, error) {
	q := axesQuery(ax)
	if excludeSession != "" {
		q.Set("session", excludeSession)
	}
	var out []store.InterruptedThread
	err := c.do("GET", "/api/context/interrupted", q, nil, &out)
	return out, err
}

// session ist die Kennung der fragenden Sitzung. Sie dient dem Server allein
// dazu, ihren eigenen angefangenen Faden nicht als unterbrochen zu melden; leer
// heisst, dass der Aufrufer sie nicht kennt.
func (c *Client) Bootstrap(ax scope.Axes, actx activation.Context, budget int, session string) (string, error) {
	q := axesQuery(ax)
	if session != "" {
		q.Set("session", session)
	}
	if actx.RepoPath != "" {
		q.Set("repo_path", actx.RepoPath)
	}
	for _, p := range actx.Paths {
		q.Add("path", p)
	}
	if budget > 0 {
		q.Set("budget", strconv.Itoa(budget))
	}
	var md string
	err := c.do("GET", "/api/context/bootstrap", q, nil, &md)
	return md, err
}

func (c *Client) PutGhost(g store.GhostFile) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	err := c.do("POST", "/api/ghosts", nil, g, &out)
	return out.ID, err
}

// GhostsForPath liefert die Beschreibung dieses Pfades und die seiner
// Vorfahren, jede einmal je Session.
//
// Der git-Blob reiste hier einmal mit, damit eine verschobene Datei ihre
// Beschreibung behält. Das ist entfallen: der Server kann Verschiebung und
// Kopie nicht unterscheiden, weil er die Repositorien nicht hat, und hängte
// deshalb bei einer Dateikopie die Beschreibung des Originals um (REQ-179).
// Der Umzug wird jetzt beim Baumschreiben erkannt, wo die Dateiliste vorliegt.
func (c *Client) GhostsForPath(project, path, sessionKey string) ([]store.GhostFile, error) {
	q := url.Values{}
	q.Set("project", project)
	q.Set("path", path)
	q.Set("session", sessionKey)
	var out []store.GhostFile
	err := c.do("GET", "/api/ghosts", q, nil, &out)
	return out, err
}

// MoveGhost hängt eine Beschreibung samt ihrer Historie auf einen neuen Pfad.
// Aufgerufen wird das nur aus dem Baumschreiben, nach ghost.DetectMoves.
func (c *Client) MoveGhost(project, from, to string) error {
	body := map[string]string{"project": project, "from": from, "to": to}
	return c.do("POST", "/api/ghosts/move", nil, body, nil)
}

// GhostHistoryCount ist die Zahl ohne den Text — der Hook nennt nur, DASS es
// Vorfassungen gibt.
func (c *Client) GhostHistoryCount(project, path string) (int, error) {
	q := url.Values{}
	q.Set("project", project)
	q.Set("path", path)
	q.Set("count", "1")
	var out struct {
		Count int `json:"count"`
	}
	err := c.do("GET", "/api/ghosts/history", q, nil, &out)
	return out.Count, err
}

// GhostHistory liefert die abgelösten Fassungen eines Pfades, neueste zuerst.
func (c *Client) GhostHistory(project, path string, limit int) ([]store.GhostVersion, error) {
	return c.ghostVersions(project, path, limit, false)
}

// GhostChain liefert dasselbe mit der aktuellen Fassung an der Spitze. Wer den
// Unterschied zwischen zwei Fassungen zeigen will, braucht sie: der Nachfolger
// der neuesten abgelösten Fassung ist die Beschreibung, die heute gilt.
func (c *Client) GhostChain(project, path string, limit int) ([]store.GhostVersion, error) {
	return c.ghostVersions(project, path, limit, true)
}

func (c *Client) ghostVersions(project, path string, limit int, chain bool) ([]store.GhostVersion, error) {
	q := url.Values{}
	q.Set("project", project)
	q.Set("path", path)
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if chain {
		q.Set("chain", "1")
	}
	var out []store.GhostVersion
	err := c.do("GET", "/api/ghosts/history", q, nil, &out)
	return out, err
}

func (c *Client) GhostTree(project, prefix string) ([]store.GhostFile, error) {
	q := url.Values{}
	q.Set("project", project)
	q.Set("prefix", prefix)
	var out []store.GhostFile
	err := c.do("GET", "/api/ghosts/tree", q, nil, &out)
	return out, err
}

func (c *Client) SearchGhosts(q string, project string, limit int) ([]store.GhostFile, error) {
	v := url.Values{}
	v.Set("q", q)
	v.Set("project", project)
	if limit > 0 {
		v.Set("limit", strconv.Itoa(limit))
	}
	var out []store.GhostFile
	err := c.do("GET", "/api/ghosts/search", v, nil, &out)
	return out, err
}

// PutGhostReview meldet, dass ein Pfad angesehen und absichtlich nicht
// beschrieben wurde. Der Blob muss mitkommen: ohne ihn gälte die Entscheidung
// jeder künftigen Fassung der Datei.
func (c *Client) PutGhostReview(r store.GhostReview) error {
	return c.do("POST", "/api/ghosts/reviews", nil, r, nil)
}

func (c *Client) GhostReviews(project, prefix string) ([]store.GhostReview, error) {
	q := url.Values{}
	q.Set("project", project)
	q.Set("prefix", prefix)
	var out []store.GhostReview
	err := c.do("GET", "/api/ghosts/reviews", q, nil, &out)
	return out, err
}
