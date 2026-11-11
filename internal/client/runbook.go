package client

import (
	"net/url"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func runbookPath(id int64, rest string) string {
	return "/api/runbooks/" + strconv.FormatInt(id, 10) + rest
}

func runbookQuery(ax scope.Axes) url.Values {
	q := url.Values{}
	if ax.Project != "" {
		q.Set("project", ax.Project)
	}
	if ax.Machine != "" {
		q.Set("machine", ax.Machine)
	}
	return q
}

// RunbookWritten ist die Antwort auf das Schreiben eines Ablaufs.
type RunbookWritten struct {
	ID                int64  `json:"id"`
	Version           int    `json:"version"`
	Hash              string `json:"hash"`
	NewRunbook        bool   `json:"new_runbook"`
	NewVersion        bool   `json:"new_version"`
	Approved          bool   `json:"approved"`
	ApprovalRequested bool   `json:"approval_requested"`
}

// WriteRunbook legt einen Ablauf oder eine neue Fassung an. approve verlangt
// die Freigabe durch den Schreiber; der Server gewährt sie nur Owner und Lead
// (bei einem Ablauf ohne Projekt dem Autor).
func (c *Client) WriteRunbook(project, machine, name string, spec runbook.Spec, note string, approve bool) (RunbookWritten, error) {
	var out RunbookWritten
	err := c.do("POST", "/api/runbooks", nil, map[string]any{
		"project": project, "machine": machine, "name": name, "spec": spec, "note": note, "approve": approve,
	}, &out)
	return out, err
}

// ListRunbooks listet die Abläufe, die im Kontext gelten.
func (c *Client) ListRunbooks(ax scope.Axes) ([]runbook.Summary, error) {
	var out []runbook.Summary
	err := c.do("GET", "/api/runbooks", runbookQuery(ax), nil, &out)
	return out, err
}

// FindRunbook löst einen Namen im Kontext auf; version 0 ist die Spitze.
func (c *Client) FindRunbook(name string, ax scope.Axes, version int) (runbook.View, error) {
	q := runbookQuery(ax)
	q.Set("name", name)
	if version > 0 {
		q.Set("version", strconv.Itoa(version))
	}
	var out runbook.View
	err := c.do("GET", "/api/runbooks/find", q, nil, &out)
	return out, err
}

// GetRunbook liest einen Ablauf nach Nummer.
func (c *Client) GetRunbook(id int64, version int) (runbook.View, error) {
	q := url.Values{}
	if version > 0 {
		q.Set("version", strconv.Itoa(version))
	}
	var out runbook.View
	err := c.do("GET", runbookPath(id, ""), q, nil, &out)
	return out, err
}

// RunbookHistory liefert alle Fassungen, älteste zuerst.
func (c *Client) RunbookHistory(id int64) ([]runbook.View, error) {
	var out []runbook.View
	err := c.do("GET", runbookPath(id, "/history"), nil, nil, &out)
	return out, err
}

// ApproveRunbook gibt eine Fassung frei. Mit hash gilt die Freigabe nur, wenn
// die Fassung noch dieselbe ist, die der Freigebende gelesen hat.
func (c *Client) ApproveRunbook(id int64, version int, hash string) error {
	return c.do("POST", runbookPath(id, "/approve"), nil, map[string]any{"version": version, "hash": hash}, nil)
}

// RunReport ist die Rückmeldung zu einem Lauf.
type RunReport struct {
	Version    int                 `json:"version"`
	Target     string              `json:"target,omitempty"`
	Mode       string              `json:"mode"`
	Result     string              `json:"result"`
	Params     map[string]string   `json:"params,omitempty"`
	Note       string              `json:"note,omitempty"`
	Machine    string              `json:"machine,omitempty"`
	Deviations []runbook.Deviation `json:"deviations,omitempty"`
}

// RunReported ist die Antwort: der Lauf und, falls Abweichungen gemeldet
// wurden, der entstandene Vorschlag.
type RunReported struct {
	RunID      int64 `json:"run_id"`
	ProposalID int64 `json:"proposal_id"`
}

// ReportRun meldet einen Lauf.
func (c *Client) ReportRun(id int64, r RunReport) (RunReported, error) {
	var out RunReported
	err := c.do("POST", runbookPath(id, "/runs"), nil, r, &out)
	return out, err
}

// RunbookRuns liefert die letzten Läufe.
func (c *Client) RunbookRuns(id int64, limit int) ([]runbook.Run, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []runbook.Run
	err := c.do("GET", runbookPath(id, "/runs"), q, nil, &out)
	return out, err
}

// RunbookProposals liefert die Vorschläge zu einem Ablauf; state leer heißt alle.
func (c *Client) RunbookProposals(id int64, state string) ([]runbook.Proposal, error) {
	q := url.Values{}
	if state != "" {
		q.Set("state", state)
	}
	var out []runbook.Proposal
	err := c.do("GET", runbookPath(id, "/proposals"), q, nil, &out)
	return out, err
}

// DecideRunbookProposal nimmt einen Vorschlag an oder lehnt ihn ab.
func (c *Client) DecideRunbookProposal(proposalID int64, accept bool) (runbook.Proposal, error) {
	var out runbook.Proposal
	err := c.do("POST", "/api/runbook-proposals/"+strconv.FormatInt(proposalID, 10)+"/decide", nil, map[string]any{"accept": accept}, &out)
	return out, err
}

// RunbookTrigger ist, woran die Auslöser geprüft werden.
type RunbookTrigger struct {
	Prompt   string   `json:"prompt,omitempty"`
	Command  string   `json:"command,omitempty"`
	Paths    []string `json:"paths,omitempty"`
	RepoPath string   `json:"repo_path,omitempty"`
	Session  string   `json:"session,omitempty"`
	Project  string   `json:"project,omitempty"`
	Machine  string   `json:"machine,omitempty"`
	// Budget is what the hook budget of the session has left (hookbudget.Remaining).
	// The server delivers and remembers only runbooks that fit; nil asks as
	// before.
	Budget *int `json:"budget,omitempty"`
}

// TriggeredRunbooks fragt, welche Abläufe zu Prompt, Befehl oder Pfaden passen.
// Die Antwort ist Markdown, ohne Treffer leer. Der Aufrufer nimmt einen Client
// mit kurzem Timeout (NewWithTimeout), wie bei den anderen Hook-Abfragen: Wissen,
// das spät kommt, ist schlechter als keines.
func (c *Client) TriggeredRunbooks(t RunbookTrigger) (string, error) {
	var md string
	err := c.do("POST", "/api/runbooks/triggered", nil, t, &md)
	return md, err
}

// RunbookTriggerSets returns the command and path triggers of the runbooks the
// server would consider for this project and machine, one set per runbook. A
// server that does not know the route answers with an error IsRouteMissing
// recognises.
func (c *Client) RunbookTriggerSets(project, machine string) ([]runbook.Triggers, error) {
	q := url.Values{}
	if project != "" {
		q.Set("project", project)
	}
	if machine != "" {
		q.Set("machine", machine)
	}
	var out struct {
		Sets []runbook.Triggers `json:"sets"`
	}
	err := c.do("GET", "/api/runbook-triggers", q, nil, &out)
	return out.Sets, err
}

// RunbookAllowed fragt den Server, ob genau diese Fassung auf dieser Maschine
// laufen darf. Das ist die einzige Quelle für ctx run --run: eine Datei auf der
// Maschine ist keine Freigabe.
func (c *Client) RunbookAllowed(id int64, version int, machine string) (store.RunbookAllowState, error) {
	q := url.Values{"machine": {machine}}
	if version > 0 {
		q.Set("version", strconv.Itoa(version))
	}
	var out store.RunbookAllowState
	err := c.do("GET", runbookPath(id, "/allowed"), q, nil, &out)
	return out, err
}
