package client

import (
	"net/url"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/workflow"
)

// WorkflowDetail is a workflow with its versions (REQ-695).
type WorkflowDetail struct {
	Workflow store.WorkflowDef          `json:"workflow"`
	Versions []store.WorkflowDefVersion `json:"versions,omitempty"`
	CanEdit  bool                       `json:"can_edit"`
}

// WorkflowSaved is the answer to a save.
type WorkflowSaved struct {
	Saved *store.WorkflowDefSaved `json:"saved,omitempty"`
	Check workflow.CheckReport    `json:"check"`
}

func workflowPath(name string) string { return "/api/workflows/" + url.PathEscape(name) }

// ListWorkflows lists the workflows of a project.
func (c *Client) ListWorkflows(project string) ([]store.WorkflowDef, error) {
	var out []store.WorkflowDef
	err := c.do("GET", "/api/workflows", url.Values{"project": {project}}, nil, &out)
	return out, err
}

// GetWorkflow reads a workflow, with its versions when asked.
func (c *Client) GetWorkflow(project, name string, versions bool) (WorkflowDetail, error) {
	q := url.Values{"project": {project}}
	if versions {
		q.Set("versions", "1")
	}
	var out WorkflowDetail
	err := c.do("GET", workflowPath(name), q, nil, &out)
	return out, err
}

// CheckWorkflow checks a text on the server: catalogue, aliases and caps of
// the project, without saving it.
func (c *Client) CheckWorkflow(project, text string) (workflow.CheckReport, error) {
	var out workflow.CheckReport
	err := c.do("POST", "/api/workflows/check", nil, map[string]any{"project": project, "text": text}, &out)
	return out, err
}

// SaveWorkflow checks a text and, when it passes, stores it as a new version.
// A text that does not pass is not sent: the answer carries the report and no
// save. base is the version the text was written on (0: a new workflow, -1:
// over whatever is there).
func (c *Client) SaveWorkflow(project, name, text, note string, base int) (WorkflowSaved, error) {
	rep, err := c.CheckWorkflow(project, text)
	if err != nil {
		return WorkflowSaved{}, err
	}
	if !rep.OK() {
		return WorkflowSaved{Check: rep}, nil
	}
	var out WorkflowSaved
	err = c.do("PUT", workflowPath(name), nil, map[string]any{"project": project, "text": text, "note": note, "base": base, "via": store.WorkflowViaMCP}, &out)
	return out, err
}
