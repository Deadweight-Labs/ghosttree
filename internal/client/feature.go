package client

import (
	"net/url"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Features (REQ-612): lebende Beschreibung dessen, was das Produkt tut.

// FeatureInput ist, was Anlegen und Fortschreiben gemeinsam nehmen: die Felder des
// Inhalts (nil lässt eines stehen) und was die Fassung ausgelöst hat.
type FeatureInput struct {
	feature.Patch
	Message  string  `json:"message,omitempty"`
	Base     int     `json:"base,omitempty"`
	Idea     int64   `json:"idea,omitempty"`
	Requests []int64 `json:"requests,omitempty"`
	PR       int64   `json:"pr,omitempty"`
	// Nur beim Anlegen.
	Project  string  `json:"project,omitempty"`
	Ideas    []int64 `json:"ideas,omitempty"`
	FromIdea int64   `json:"from_idea,omitempty"`
	// SessionID nennt die eigene Session für die Herkunft.
	SessionID int64 `json:"session_id,omitempty"`
}

// CreateFeature legt ein Feature an.
func (c *Client) CreateFeature(in FeatureInput) (feature.Feature, error) {
	var out feature.Feature
	err := c.do("POST", "/api/features", nil, in, &out)
	return out, err
}

// GetFeature liest ein Feature mit seiner jüngsten Fassung.
func (c *Client) GetFeature(id int64) (feature.Feature, error) {
	var out feature.Feature
	err := c.do("GET", idPathOf("/api/features/", id, ""), nil, nil, &out)
	return out, err
}

// SearchFeatures liefert die Features eines Projekts; state "" heißt aktive, "all"
// alle. path liefert nur Features, deren Bereiche den Pfad enthalten.
func (c *Client) SearchFeatures(project, state, text, path string, limit int) ([]feature.Feature, error) {
	q := url.Values{"project": {project}}
	if state != "" {
		q.Set("state", state)
	}
	if text != "" {
		q.Set("q", text)
	}
	if path != "" {
		q.Set("path", path)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []feature.Feature
	err := c.do("GET", "/api/features", q, nil, &out)
	return out, err
}

// FeatureVersions listet die Fassungen; full bringt die langen Felder mit.
func (c *Client) FeatureVersions(id int64, full bool) ([]feature.Version, error) {
	q := url.Values{}
	if full {
		q.Set("full", "1")
	}
	var out []feature.Version
	err := c.do("GET", idPathOf("/api/features/", id, "/versions"), q, nil, &out)
	return out, err
}

// FeatureVersion liest eine Fassung mit Inhalt (revision 0 = jüngste).
func (c *Client) FeatureVersion(id int64, revision int) (feature.Version, error) {
	var out feature.Version
	err := c.do("GET", idPathOf("/api/features/", id, "/version"), url.Values{"revision": {strconv.Itoa(revision)}}, nil, &out)
	return out, err
}

// UpdateFeatureResult ist die Antwort auf das Fortschreiben.
type UpdateFeatureResult struct {
	Version   feature.Version `json:"version"`
	Unchanged bool            `json:"unchanged"`
}

// UpdateFeature schreibt eine neue Fassung.
func (c *Client) UpdateFeature(id int64, in FeatureInput) (UpdateFeatureResult, error) {
	var out UpdateFeatureResult
	err := c.do("POST", idPathOf("/api/features/", id, "/versions"), nil, in, &out)
	return out, err
}

// SetFeatureState stellt ein Feature ein (state retired, mit reason) oder nimmt es
// wieder auf (active).
func (c *Client) SetFeatureState(id int64, state, reason string) (feature.Feature, error) {
	var out feature.Feature
	err := c.do("POST", idPathOf("/api/features/", id, "/state"), nil, map[string]any{"state": state, "reason": reason}, &out)
	return out, err
}

// LinkFeature verknüpft ein Feature mit einer Idee oder einem Issue (kind idea oder
// request; rel origin oder affects).
func (c *Client) LinkFeature(id int64, kind string, ref int64, rel string) (feature.Feature, error) {
	var out feature.Feature
	err := c.do("POST", idPathOf("/api/features/", id, "/links"), nil, map[string]any{"kind": kind, "ref": ref, "rel": rel}, &out)
	return out, err
}

// UnlinkFeature löst einen Verweis.
func (c *Client) UnlinkFeature(id int64, kind string, ref int64, rel string) (feature.Feature, error) {
	var out feature.Feature
	err := c.do("DELETE", idPathOf("/api/features/", id, "/links"), url.Values{"kind": {kind}, "ref": {strconv.FormatInt(ref, 10)}, "rel": {rel}}, nil, &out)
	return out, err
}

// LinkIdeaFeature lässt eine Idee ein Feature betreffen.
func (c *Client) LinkIdeaFeature(ideaID, featureID int64) (idea.Idea, error) {
	var out idea.Idea
	err := c.do("POST", idPathOf("/api/ideas/", ideaID, "/features"), nil, map[string]any{"feature": featureID}, &out)
	return out, err
}

// UnlinkIdeaFeature nimmt „betrifft FEAT-n“ zurück.
func (c *Client) UnlinkIdeaFeature(ideaID, featureID int64) (idea.Idea, error) {
	var out idea.Idea
	err := c.do("DELETE", idPathOf("/api/ideas/", ideaID, "/features/"+strconv.FormatInt(featureID, 10)), nil, nil, &out)
	return out, err
}

// PullPathsResult ist die Antwort auf das Melden der PR-Pfade.
type PullPathsResult struct {
	Stored    int                          `json:"stored"`
	Dropped   int                          `json:"dropped"`
	Checklist []requestdomain.FeatureCheck `json:"checklist"`
}

// ReportPullPaths meldet die Pfade, die ein PR ändert, und liefert die Punkte der
// Checkliste, die daraus folgen.
func (c *Client) ReportPullPaths(project string, pr int64, paths []string) (PullPathsResult, error) {
	var out PullPathsResult
	err := c.do("POST", "/api/features/pr-paths", nil, map[string]any{"project": project, "pr": pr, "paths": paths}, &out)
	return out, err
}

// FeatureChecklist liefert die Punkte „Feature X: Beschreibung angepasst?“ eines PRs.
func (c *Client) FeatureChecklist(project string, pr int64) ([]requestdomain.FeatureCheck, error) {
	var out []requestdomain.FeatureCheck
	err := c.do("GET", "/api/features/checklist", url.Values{"project": {project}, "pr": {strconv.FormatInt(pr, 10)}}, nil, &out)
	return out, err
}

// ConfirmFeatureUnchanged hält fest, dass die Beschreibung für den PR geprüft ist
// und stimmt.
func (c *Client) ConfirmFeatureUnchanged(id, pr int64, note string) error {
	return c.do("POST", idPathOf("/api/features/", id, "/checks"), nil, map[string]any{"pr": pr, "note": note}, nil)
}

// FeatureContext liefert die Features, deren Bereiche die Pfade enthalten, in
// Kurzform mit Budget. Mit session kommt jede Fassung einmal je Session.
func (c *Client) FeatureContext(project string, paths []string, session string, budget int) (store.FeatureContext, error) {
	q := url.Values{"project": {project}}
	for _, p := range paths {
		q.Add("path", p)
	}
	if session != "" {
		q.Set("session", session)
	}
	if budget > 0 {
		q.Set("budget", strconv.Itoa(budget))
	}
	var out store.FeatureContext
	err := c.do("GET", "/api/features/context", q, nil, &out)
	return out, err
}

// ExportFeatures liefert die aktiven Features als Markdown-Seite.
func (c *Client) ExportFeatures(project, title string) (string, error) {
	q := url.Values{"project": {project}}
	if title != "" {
		q.Set("title", title)
	}
	var out string
	err := c.do("GET", "/api/features/export", q, nil, &out)
	return out, err
}
