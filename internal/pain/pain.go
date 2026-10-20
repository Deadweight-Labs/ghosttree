package pain

import (
	"fmt"
	"strconv"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

const (
	Active    = "active"
	Mitigated = "mitigated"
	Resolved  = "resolved"
	Obsolete  = "obsolete"
)

var States = []string{Active, Mitigated, Resolved, Obsolete}

var EvidenceKinds = []string{"commit", "test", "file", "decision", "session", "url", "request"}

type Pain struct {
	ID        int64  `json:"id"`
	Project   string `json:"project"`
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
	State     string `json:"state"`
	Remaining string `json:"remaining,omitempty"`
	Person    string `json:"person,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	// AuthorID is the account that wrote the pain; never delivered.
	AuthorID int64 `json:"-"`
	// Match is set by a search and only to "semantic": the embedding found the
	// pain alone, its words do not match the query. A hit of the text match
	// leaves it empty, which is also how every other read leaves it.
	Match string `json:"match,omitempty"`
}

func (p Pain) HumanID() string { return fmt.Sprintf("PAIN-%d", p.ID) }

// Target names what a pain motivates: exactly one of the three.
type Target struct {
	RequestID   int64  `json:"request_id,omitempty"`
	KnowledgeID int64  `json:"knowledge_id,omitempty"`
	ExternalRef string `json:"external_ref,omitempty"`
}

// Link is a stored edge pain -> target.
type Link struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"` // request, knowledge, external
	RequestID   int64  `json:"request_id,omitempty"`
	KnowledgeID int64  `json:"knowledge_id,omitempty"`
	ExternalRef string `json:"external_ref,omitempty"`
	Title       string `json:"title,omitempty"`
}

type Evidence struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

type Event struct {
	ID           int64  `json:"id"`
	Kind         string `json:"kind"`
	FromState    string `json:"from_state,omitempty"`
	ToState      string `json:"to_state,omitempty"`
	Remaining    string `json:"remaining,omitempty"`
	EvidenceKind string `json:"evidence_kind,omitempty"`
	EvidenceRef  string `json:"evidence_ref,omitempty"`
	Note         string `json:"note,omitempty"`
	Data         string `json:"data,omitempty"`
	Person       string `json:"person,omitempty"`
	CreatedAt    string `json:"created_at"`
}

type Detail struct {
	Pain      Pain    `json:"pain"`
	Motivates []Link  `json:"motivates"`
	Events    []Event `json:"events"`
}

// Filter limits SearchPains. State "" means active and mitigated, "all" every state.
type Filter struct {
	Project string
	// Projects, when it is not empty, is searched instead of Project, in one
	// query: one embedding of the query, one share of hits for meaning alone for
	// the whole list. The pains come project after project in the order given,
	// each by state.
	Projects  []string
	State     string
	Query     string
	RequestID int64
	Limit     int
}

func ValidState(s string) bool {
	for _, v := range States {
		if v == s {
			return true
		}
	}
	return false
}

func ruleErr(code, msg, res string) error { return requestdomain.NewRuleError(code, msg, res, nil) }

// ValidateTransition applies the transition table. remaining and note are
// trimmed by the caller.
func ValidateTransition(from, to, remaining string, ev Evidence, note string) error {
	if !ValidState(to) {
		return ruleErr("invalid_transition", fmt.Sprintf("unknown pain state %q", to), "use active, mitigated, resolved or obsolete")
	}
	needEvidence, needRemaining, needNote := false, false, false
	switch {
	case from == Active && to == Mitigated, from == Mitigated && to == Mitigated:
		needEvidence, needRemaining = true, true
	case (from == Active || from == Mitigated) && to == Resolved:
		needEvidence = true
	case to == Obsolete && from != Obsolete:
		needNote = true
	case to == Active && from != Active:
		needNote = true
	default:
		return ruleErr("invalid_transition", fmt.Sprintf("a pain cannot go from %s to %s", from, to), "check the allowed transitions: active to mitigated, resolved or obsolete; mitigated to resolved, obsolete or active; resolved or obsolete back to active")
	}
	if needRemaining && strings.TrimSpace(remaining) == "" {
		return ruleErr("remaining_required", "a mitigated pain must say what remains", "name what is still unsolved in remaining")
	}
	if needEvidence {
		if strings.TrimSpace(ev.Kind) == "" && strings.TrimSpace(ev.Ref) == "" {
			return ruleErr("evidence_required", "this transition needs evidence", "give evidence_kind and evidence_ref: commit, test, file, decision, session, url or request")
		}
		if err := ValidateEvidence(ev); err != nil {
			return err
		}
	}
	if needNote && strings.TrimSpace(note) == "" {
		return ruleErr("note_required", "this transition needs a reason", "say why in note")
	}
	return nil
}

func ValidateEvidence(ev Evidence) error {
	ok := false
	for _, k := range EvidenceKinds {
		ok = ok || k == ev.Kind
	}
	if !ok || strings.TrimSpace(ev.Ref) == "" {
		return ruleErr("evidence_invalid", "evidence needs a known kind and a reference", "kinds: commit, test, file, decision, session, url, request")
	}
	return nil
}

// ParseNumberRef reads "REQ-12", "#12", "PAIN-3" or "12" for the given prefix.
func ParseNumberRef(ref, prefix string) (int64, bool) {
	ref = strings.TrimSpace(ref)
	if len(ref) > len(prefix) && strings.EqualFold(ref[:len(prefix)], prefix) {
		ref = ref[len(prefix):]
	}
	ref = strings.TrimPrefix(ref, "#")
	n, err := strconv.ParseInt(ref, 10, 64)
	return n, err == nil && n > 0
}

func ReasonRequired() error {
	return ruleErr("reason_required", "this change needs a reason", "say why in reason")
}

func TitleRequired() error {
	return ruleErr("title_required", "pain title is required", "say the problem in one line")
}

// Actor is who changes a pain.
type Actor struct {
	AccountID int64
	Person    string
	SessionID int64
}
