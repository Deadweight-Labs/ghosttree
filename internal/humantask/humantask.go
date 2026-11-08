package humantask

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"

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
	// Steps is the walk-through for the human (REQ-603): numbered, each with an
	// optional link. The interface shows it as a checklist.
	Steps       []Step `json:"steps,omitempty"`
	ConfirmedAt string `json:"confirmed_at,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	// Handoff is the summary of the work the task hangs on, when there is one.
	Handoff string `json:"-"`
	// HandoffSession is the session that wrote the handoff; the bootstrap shows
	// the handoff only where the viewer may read that session.
	HandoffSession int64 `json:"-"`
	// CanConfirm tells the bootstrap whether the viewer may confirm or reopen
	// this task; it is set per viewer and never stored or delivered as JSON.
	CanConfirm bool `json:"-"`
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
	// Everyone leaves the task to whoever of the project can do it. Without it
	// and without an Assignee a task belongs to the person who creates it (the
	// human whose session the agent is), because "anyone" on a project of two
	// is two people each assuming the other one will do it.
	Everyone bool `json:"everyone,omitempty"`
	// Steps is the optional walk-through: at most MaxSteps one-line steps, each
	// with an optional http(s) link. See ValidateSteps.
	Steps []Step `json:"steps,omitempty"`
	// Explanation is the agent's "Kurz erklärt" text for the person who has to
	// act. The server stores it beside the task; it is not part of the task.
	Explanation string `json:"explanation,omitempty"`
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
	if in.Everyone && strings.TrimSpace(in.Assignee) != "" {
		return ruleErr("assignee_conflict", "a task is either for one person or for everyone", "give assignee or everyone, not both")
	}
	if (in.Kind == "secret" || in.Kind == "file") && strings.TrimSpace(in.Location) == "" {
		return ruleErr("location_required", "a secret or file task needs a location", "name where it should be placed (path, vault entry, env file), never the value")
	}
	return ValidateSteps(in.Steps)
}

func InvalidTransition(from, to string) error {
	return ruleErr("invalid_transition", fmt.Sprintf("a task cannot go from %s to %s", from, to), "open goes to done or dropped, done to confirmed, open or dropped, confirmed to open, dropped to open")
}

func ReasonRequired() error {
	return ruleErr("reason_required", "this change needs a reason", "give a reason")
}

// Step is one line of a task's walk-through; URL is optional.
type Step struct {
	Text string `json:"text"`
	URL  string `json:"url,omitempty"`
}

const (
	MaxSteps    = 12
	MaxStepText = 240
	MaxStepURL  = 600
)

// plainLine reports whether s is a single line of visible text: no control,
// format (unicode.Cf, which hides direction overrides and zero-width marks),
// line or paragraph separator characters. Tabs count as control.
func plainLine(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return false
		}
	}
	return true
}

// ValidateSteps checks the walk-through before it is stored. Steps land in an
// agent's context and in the browser, so they are refused, not repaired: one
// line each, no control or format characters, links only http(s) without
// userinfo.
func ValidateSteps(steps []Step) error {
	if len(steps) > MaxSteps {
		return ruleErr("steps_invalid", fmt.Sprintf("a task has at most %d steps", MaxSteps), "keep the walk-through short: one line per step")
	}
	for i, st := range steps {
		n := i + 1
		text := strings.TrimSpace(st.Text)
		switch {
		case text == "":
			return ruleErr("steps_invalid", fmt.Sprintf("step %d has no text", n), "write what the human does in this step, in one line")
		case len([]rune(text)) > MaxStepText:
			return ruleErr("steps_invalid", fmt.Sprintf("step %d is longer than %d characters", n, MaxStepText), "one short line per step")
		case !plainLine(st.Text):
			return ruleErr("steps_invalid", fmt.Sprintf("step %d has a line break, control or format character", n), "one line of plain text per step")
		}
		if st.URL == "" {
			continue
		}
		if len(st.URL) > MaxStepURL || !plainLine(st.URL) || strings.ContainsAny(st.URL, " \t") {
			return ruleErr("steps_invalid", fmt.Sprintf("the link of step %d is not a plain address", n), "give one http(s) address without spaces")
		}
		u, err := url.Parse(st.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return ruleErr("steps_invalid", fmt.Sprintf("the link of step %d must be an http(s) address without credentials", n), "give one http(s) address, no token or login in it")
		}
	}
	return nil
}

// CleanSteps trims the text of each step; call it after ValidateSteps.
func CleanSteps(steps []Step) []Step {
	out := make([]Step, 0, len(steps))
	for _, st := range steps {
		out = append(out, Step{Text: strings.TrimSpace(st.Text), URL: st.URL})
	}
	return out
}

// StepFields lists name and text of every step and link, for CheckFields.
func (in CreateInput) StepFields() []string {
	var f []string
	for i, st := range in.Steps {
		f = append(f, fmt.Sprintf("step %d", i+1), st.Text)
		if st.URL != "" {
			f = append(f, fmt.Sprintf("step %d link", i+1), st.URL)
		}
	}
	return f
}
