package humantask

import (
	"fmt"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

const (
	Open      = "open"
	Done      = "done"
	Confirmed = "confirmed"
	Dropped   = "dropped"
)

var Kinds = []string{"secret", "file", "input", "action"}

type Task struct {
	ID          int64  `json:"id"`
	Project     string `json:"project"`
	RequestID   int64  `json:"request_id"`
	WorkID      int64  `json:"work_id,omitempty"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Location    string `json:"location,omitempty"`
	CheckHint   string `json:"check_hint,omitempty"`
	State       string `json:"state"`
	Assignee    string `json:"assignee,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
	DoneBy      string `json:"done_by,omitempty"`
	DoneNote    string `json:"done_note,omitempty"`
	DoneVia     string `json:"done_via,omitempty"`
	DoneAt      string `json:"done_at,omitempty"`
	StateNote   string `json:"state_note,omitempty"`
	ConfirmedAt string `json:"confirmed_at,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	// Handoff is the summary of the work the task hangs on, when there is one.
	Handoff string `json:"-"`
	// HandoffSession is the session that wrote the handoff; the bootstrap shows
	// the handoff only where the viewer may read that session.
	HandoffSession int64 `json:"-"`
	// Account columns decide visibility and never leave the server.
	AssigneeAccount  int64 `json:"-"`
	CreatedByAccount int64 `json:"-"`
}

func (t Task) HumanID() string { return fmt.Sprintf("TASK-%d", t.ID) }

type CreateInput struct {
	RequestID   int64  `json:"request_id,omitempty"`
	WorkID      int64  `json:"work_id,omitempty"`
	SessionID   int64  `json:"session_id,omitempty"`
	Project     string `json:"project,omitempty"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Location    string `json:"location,omitempty"`
	CheckHint   string `json:"check_hint,omitempty"`
	Assignee    string `json:"assignee,omitempty"`
}

// Actor is who performs a change. Web is true only for an interactive web session.
type Actor struct {
	AccountID int64
	Person    string
	Web       bool
	SessionID int64
}

// Waiting is what the bootstrap shows: done-but-unconfirmed first, then open.
type Waiting struct {
	Done     []Task
	Open     []Task
	MoreDone int
	MoreOpen int
}

func ruleErr(code, msg, res string) error { return requestdomain.NewRuleError(code, msg, res, nil) }

func ValidKind(k string) bool {
	for _, v := range Kinds {
		if v == k {
			return true
		}
	}
	return false
}

// ValidateCreate checks the shape of a new task before anything else.
func ValidateCreate(in CreateInput) error {
	if !ValidKind(in.Kind) {
		return ruleErr("invalid_kind", "task kind must be secret, file, input or action", "choose one of secret, file, input, action")
	}
	if strings.TrimSpace(in.Title) == "" {
		return ruleErr("title_required", "task title is required", "say in a few words what the human has to do")
	}
	if (in.Kind == "secret" || in.Kind == "file") && strings.TrimSpace(in.Location) == "" {
		return ruleErr("location_required", "a secret or file task needs a location", "name where it should be placed (path, vault entry, env file), never the value")
	}
	return nil
}

func InvalidTransition(from, to string) error {
	return ruleErr("invalid_transition", fmt.Sprintf("a task cannot go from %s to %s", from, to), "open goes to done or dropped, done to confirmed, open or dropped, confirmed to open, dropped to open")
}

func ReasonRequired() error {
	return ruleErr("reason_required", "this change needs a reason", "say why in note")
}
