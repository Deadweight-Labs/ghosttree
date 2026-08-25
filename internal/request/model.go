// Package request defines Ghosttree's work-ledger domain.
package request

import (
	"errors"
	"fmt"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

type Request struct {
	ID             int64      `json:"id"`
	Type           string     `json:"type"`
	Title          string     `json:"title"`
	Description    string     `json:"description"`
	State          string     `json:"state"`
	Priority       string     `json:"priority,omitempty"`
	Scope          scope.Axes `json:"scope"`
	Origin         string     `json:"origin"`
	Person         string     `json:"person"`
	SessionRef     string     `json:"session_ref,omitempty"`
	IdempotencyKey string     `json:"-"`
	CreatedAt      string     `json:"created_at"`
	UpdatedAt      string     `json:"updated_at"`
}

func (r Request) HumanID() string { return fmt.Sprintf("REQ-%d", r.ID) }

type Criterion struct {
	ID          int64      `json:"id"`
	RequestID   int64      `json:"request_id"`
	Number      int        `json:"number"`
	Description string     `json:"description"`
	State       string     `json:"state"`
	Evidence    []Evidence `json:"evidence,omitempty"`
	CreatedAt   string     `json:"created_at"`
	UpdatedAt   string     `json:"updated_at"`
}

func (c Criterion) HumanID() string { return fmt.Sprintf("AC-%d.%d", c.RequestID, c.Number) }

type Evidence struct {
	ID          int64  `json:"id"`
	RequestID   int64  `json:"request_id"`
	CriterionID int64  `json:"criterion_id,omitempty"`
	Kind        string `json:"kind"`
	Ref         string `json:"ref"`
	Person      string `json:"person"`
	CreatedAt   string `json:"created_at"`
}

type Work struct {
	ID        int64  `json:"id"`
	RequestID int64  `json:"request_id"`
	SessionID int64  `json:"session_id"`
	Role      string `json:"role"`
	State     string `json:"state"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

type Relation struct {
	ID             int64  `json:"id"`
	RequestID      int64  `json:"request_id"`
	OtherRequestID int64  `json:"other_request_id,omitempty"`
	KnowledgeID    int64  `json:"knowledge_id,omitempty"`
	Kind           string `json:"kind"`
	ExternalRef    string `json:"external_ref,omitempty"`
	CreatedAt      string `json:"created_at"`
}

type Activity struct {
	ID        int64  `json:"id"`
	RequestID int64  `json:"request_id"`
	Kind      string `json:"kind"`
	Person    string `json:"person"`
	Data      string `json:"data,omitempty"`
	CreatedAt string `json:"created_at"`
}

type Detail struct {
	Request   Request     `json:"request"`
	Criteria  []Criterion `json:"criteria"`
	Relations []Relation  `json:"relations"`
	Work      []Work      `json:"work"`
	Activity  []Activity  `json:"activity"`
	// Sightings ist bei einem destillierten Eintrag das, was die Beschreibung
	// nicht sein kann: der Wortlaut. Leer bei allem, was ein Mensch geschrieben
	// hat — dort ist die Beschreibung selbst die Quelle.
	Sightings []Sighting `json:"sightings,omitempty"`
}

// Sighting ist eine Stelle, an der jemand den Wunsch geäussert hat: das Zitat
// und genug Adresse, um es im Transkript wiederzufinden.
type Sighting struct {
	SessionID int64  `json:"session_id"`
	ChunkSeq  int    `json:"chunk_seq"`
	Quote     string `json:"quote"`
	At        string `json:"at,omitempty"`
	Harness   string `json:"harness,omitempty"`
}

type CreateInput struct {
	Request        Request  `json:"request"`
	Criteria       []string `json:"criteria"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
}

type SearchFilter struct {
	Scope  scope.Axes
	State  string
	Type   string
	Query  string
	Cursor string
	// FullDescription gibt die Beschreibung ungekürzt zurück. Für eine
	// Trefferliste falsch — 24 volle Beschreibungen sprengen jedes Werkzeuglimit
	// —, für den Dateispiegel notwendig: der gibt sich nicht als Liste aus,
	// sondern als das Dokument selbst, und ein Text, der mitten im Wort endet,
	// sieht dort nicht gekürzt aus, sondern beschädigt.
	FullDescription bool
	Limit           int
}

type SearchHit struct {
	Request       Request `json:"request"`
	OpenCriteria  int     `json:"open_criteria"`
	LatestHandoff string  `json:"latest_handoff,omitempty"`
	MatchReason   string  `json:"match_reason,omitempty"`
	// Sightings zählt die unabhängigen Sessions, in denen der Wunsch fiel. Ein
	// Eintrag aus vier Sessions ist eine Anforderung, die nicht gebaut wird;
	// einer aus einer war vielleicht lautes Denken. Null für Handgeschriebenes.
	Sightings int `json:"sightings,omitempty"`
}

type SearchPage struct {
	Results    []SearchHit `json:"results"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

type SessionHit struct {
	SessionID int64  `json:"session_id"`
	ChunkSeq  int64  `json:"chunk_seq"`
	Harness   string `json:"harness"`
	Outcome   string `json:"outcome"`
	Handoff   string `json:"handoff,omitempty"`
	Snippet   string `json:"snippet"`
}

type SessionPage struct {
	Results    []SessionHit `json:"results"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

type RuleError struct {
	ErrorCode  string   `json:"code"`
	Message    string   `json:"message"`
	Resolution string   `json:"resolution"`
	IDs        []string `json:"ids,omitempty"`
}

func (e *RuleError) Error() string { return e.Message }

func NewRuleError(code, message, resolution string, ids []string) error {
	return &RuleError{ErrorCode: code, Message: message, Resolution: resolution, IDs: ids}
}

func Code(err error) string {
	var rule *RuleError
	if errors.As(err, &rule) {
		return rule.ErrorCode
	}
	return ""
}
