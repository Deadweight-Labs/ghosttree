package client

import (
	"net/url"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/query"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Labels, Themen, gespeicherte Suchen (REQ-600) und Ideen (REQ-606) über die API.

func idPathOf(prefix string, id int64, rest string) string {
	return prefix + strconv.FormatInt(id, 10) + rest
}

// ListLabels liefert die Labels eines Projekts (Projekt und Org zusammen) oder,
// mit org, die einer Org.
func (c *Client) ListLabels(project, org string) ([]store.Label, error) {
	q := url.Values{}
	if org != "" {
		q.Set("org", org)
	} else {
		q.Set("project", project)
	}
	var out []store.Label
	err := c.do("GET", "/api/labels", q, nil, &out)
	return out, err
}

// CreateLabel legt ein Label im Projekt oder, mit org, in der Org an.
func (c *Client) CreateLabel(project, org, name, color, description string) (store.Label, error) {
	body := map[string]any{"name": name, "color": color, "description": description}
	if org != "" {
		body["org"] = org
	} else {
		body["project"] = project
	}
	var out store.Label
	err := c.do("POST", "/api/labels", nil, body, &out)
	return out, err
}

// UpdateLabel ändert ein Label; nil lässt ein Feld stehen.
func (c *Client) UpdateLabel(id int64, name, color, description *string) (store.Label, error) {
	var out store.Label
	err := c.do("PATCH", idPathOf("/api/labels/", id, ""), nil, map[string]any{"name": name, "color": color, "description": description}, &out)
	return out, err
}

// DeleteLabel löscht ein Label.
func (c *Client) DeleteLabel(id int64) error {
	return c.do("DELETE", idPathOf("/api/labels/", id, ""), nil, nil, nil)
}

// SetLabels setzt und nimmt Labels (nach Name) an einem Objekt (request, pain,
// idea oder knowledge) und liefert seine Labels danach.
func (c *Client) SetLabels(kind string, id int64, add, remove []string) ([]store.Label, error) {
	var out []store.Label
	err := c.do("POST", "/api/label-assignments", nil, map[string]any{"kind": kind, "id": id, "add": add, "remove": remove}, &out)
	return out, err
}

// LabelsOf liefert die Labels eines Objekts.
func (c *Client) LabelsOf(kind string, id int64) ([]store.Label, error) {
	var out []store.Label
	err := c.do("GET", "/api/label-assignments", url.Values{"kind": {kind}, "id": {strconv.FormatInt(id, 10)}}, nil, &out)
	return out, err
}

// ListTopics liefert die Themen eines Projekts.
func (c *Client) ListTopics(project string) ([]store.Topic, error) {
	var out []store.Topic
	err := c.do("GET", "/api/topics", url.Values{"project": {project}}, nil, &out)
	return out, err
}

// SaveTopic legt ein Thema an oder ersetzt das gleichnamige.
func (c *Client) SaveTopic(project, name, description string, prefixes []string) (store.Topic, error) {
	var out store.Topic
	err := c.do("POST", "/api/topics", nil, map[string]any{"project": project, "name": name, "description": description, "prefixes": prefixes}, &out)
	return out, err
}

// DeleteTopic löscht ein Thema.
func (c *Client) DeleteTopic(project, name string) error {
	return c.do("DELETE", "/api/topics", url.Values{"project": {project}, "name": {name}}, nil, nil)
}

// ListPresets liefert die mitgelieferten und die gespeicherten Suchen, die für
// den Aufrufer im Projekt gelten.
func (c *Client) ListPresets(project string) ([]store.SearchPreset, error) {
	var out []store.SearchPreset
	err := c.do("GET", "/api/search-presets", url.Values{"project": {project}}, nil, &out)
	return out, err
}

// SavePreset speichert eine Suche (scope user, project oder org).
func (c *Client) SavePreset(project, scope, name, description string, queries []string) (store.SearchPreset, error) {
	var out store.SearchPreset
	err := c.do("POST", "/api/search-presets", nil, map[string]any{"project": project, "scope": scope, "name": name, "description": description, "queries": queries}, &out)
	return out, err
}

// DeletePreset löscht eine gespeicherte Suche.
func (c *Client) DeletePreset(id int64) error {
	return c.do("DELETE", idPathOf("/api/search-presets/", id, ""), nil, nil, nil)
}

// PresetRun ist das Ergebnis einer ausgeführten Suche.
type PresetRun struct {
	Preset  query.Preset `json:"preset"`
	Queries []string     `json:"queries"`
	Result  SearchResult `json:"result"`
}

// RunPreset führt eine Suche mit Argumenten aus (pr, branch, area, since).
func (c *Client) RunPreset(project, name string, args map[string]string, limit int) (PresetRun, error) {
	q := url.Values{"project": {project}, "name": {name}}
	for k, v := range args {
		q.Set(k, v)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out PresetRun
	err := c.do("GET", "/api/search-presets/run", q, nil, &out)
	return out, err
}

// CreateIdea hält einen Wunsch fest. allowDuplicate legt ihn auch an, wenn es eine
// ähnliche offene Idee gibt.
func (c *Client) CreateIdea(project, title, body string, allowDuplicate bool, sessionID int64) (idea.Idea, error) {
	var out idea.Idea
	err := c.do("POST", "/api/ideas", nil, map[string]any{
		"project": project, "title": title, "body": body, "allow_duplicate": allowDuplicate, "session_id": sessionID,
	}, &out)
	return out, err
}

// GetIdea liest eine Idee.
func (c *Client) GetIdea(id int64) (idea.Idea, error) {
	var out idea.Idea
	err := c.do("GET", idPathOf("/api/ideas/", id, ""), nil, nil, &out)
	return out, err
}

// SearchIdeas liefert die Ideen eines Projekts; state "" heißt offene, "all" alle.
func (c *Client) SearchIdeas(project, state, text string, limit int) ([]idea.Idea, error) {
	q := url.Values{"project": {project}}
	if state != "" {
		q.Set("state", state)
	}
	if text != "" {
		q.Set("q", text)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []idea.Idea
	err := c.do("GET", "/api/ideas", q, nil, &out)
	return out, err
}

// SetIdeaState verwirft eine Idee (mit Grund) oder öffnet sie wieder.
func (c *Client) SetIdeaState(id int64, state, reason string) (idea.Idea, error) {
	var out idea.Idea
	err := c.do("POST", idPathOf("/api/ideas/", id, "/state"), nil, map[string]any{"state": state, "reason": reason}, &out)
	return out, err
}

// LinkIdeaRequest verknüpft eine Idee mit einem Request, der aus ihr entstand.
func (c *Client) LinkIdeaRequest(id, requestID int64) (idea.Idea, error) {
	var out idea.Idea
	err := c.do("POST", idPathOf("/api/ideas/", id, "/requests"), nil, map[string]any{"request_id": requestID}, &out)
	return out, err
}

// PromoteIdea macht aus einer Idee einen Request und verknüpft beide: der Request
// geht durch denselben Weg wie jeder andere (Anker und Kontext der Session
// inklusive), danach die Verknüpfung. Scheitert sie, steht der Request schon da;
// der Fehler sagt es, damit niemand ihn doppelt anlegt.
func (c *Client) PromoteIdea(id int64, in requestdomain.CreateInput) (requestdomain.Detail, idea.Idea, error) {
	i, err := c.GetIdea(id)
	if err != nil {
		return requestdomain.Detail{}, idea.Idea{}, err
	}
	in.Request.Scope.Project = i.Project
	if in.Request.Title == "" {
		in.Request.Title = i.Title
	}
	if in.Request.Description == "" {
		in.Request.Description = i.Body + "\n\nAus " + i.HumanID() + "."
	}
	if in.Request.Type == "" {
		in.Request.Type = "feature"
	}
	detail, err := c.CreateRequest(in)
	if err != nil {
		return requestdomain.Detail{}, idea.Idea{}, err
	}
	linked, err := c.LinkIdeaRequest(id, detail.Request.ID)
	if err != nil {
		return detail, idea.Idea{}, &promoteError{request: detail.Request.ID, err: err}
	}
	return detail, linked, nil
}

type promoteError struct {
	request int64
	err     error
}

func (e *promoteError) Error() string {
	return "REQ-" + strconv.FormatInt(e.request, 10) + " was created, but linking it to the idea failed: " + e.err.Error() + " (link it with idea_update link_request, do not create it again)"
}

func (e *promoteError) Unwrap() error { return e.err }
