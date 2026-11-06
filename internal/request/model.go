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
	ID        int64 `json:"id"`
	RequestID int64 `json:"request_id"`
	SessionID int64 `json:"session_id,omitempty"`
	// SessionPublicID: Adresse der Session für Betrachter, die die laufende
	// Nummer nicht kennen dürfen; nur gesetzt, wenn sie das Transkript lesen.
	SessionPublicID string `json:"session_public_id,omitempty"`
	Role            string `json:"role"`
	State           string `json:"state"`
	StartedAt       string `json:"started_at"`
	EndedAt         string `json:"ended_at,omitempty"`
	Summary         string `json:"summary,omitempty"`
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
	// SessionID: die Session, auf die sich der Eintrag bezieht (0 = keine).
	// Nie ausgeliefert; die Sicht des Betrachters entscheidet damit, ob sie
	// den Eintrag zeigen darf.
	SessionID int64 `json:"-"`
}

type Detail struct {
	// Replayed: CreateRequest found the same author's earlier request for the
	// idempotency key instead of inserting one.
	Replayed  bool        `json:"-"`
	Request   Request     `json:"request"`
	Criteria  []Criterion `json:"criteria"`
	Relations []Relation  `json:"relations"`
	Work      []Work      `json:"work"`
	Activity  []Activity  `json:"activity"`
	// Sightings ist bei einem destillierten Eintrag das, was die Beschreibung
	// nicht sein kann: der Wortlaut. Leer bei allem, was ein Mensch geschrieben
	// hat — dort ist die Beschreibung selbst die Quelle.
	Sightings []Sighting `json:"sightings,omitempty"`
	// Pains are the problems this request was written to answer; Tasks are what
	// it waits on from a human. Both are filled per viewer by RequestDetailView.
	Pains []PainRef `json:"pains,omitempty"`
	Tasks []TaskRef `json:"tasks,omitempty"`
}

// PainRef is a pain that motivates a request, as far as the viewer may read it.
type PainRef struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Remaining string `json:"remaining,omitempty"`
}

// TaskRef is a human task hanging on a request, as far as the viewer may read it.
type TaskRef struct {
	ID       int64  `json:"id"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	State    string `json:"state"`
	Location string `json:"location,omitempty"`
}

// Sighting ist eine Stelle, an der jemand den Wunsch geäussert hat: das Zitat
// und genug Adresse, um es im Transkript wiederzufinden.
type Sighting struct {
	SessionID       int64  `json:"session_id,omitempty"`
	SessionPublicID string `json:"session_public_id,omitempty"`
	ChunkSeq        int    `json:"chunk_seq"`
	Quote           string `json:"quote"`
	At              string `json:"at,omitempty"`
	Harness         string `json:"harness,omitempty"`
}

type CreateInput struct {
	Request        Request  `json:"request"`
	Criteria       []string `json:"criteria"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
	// Explanation is the agent's "Kurz erklärt" text. It travels with the call
	// to the server, which stores it beside the request; it is not part of it.
	Explanation string `json:"-"`
	// AuthorID is the writing account, set by the server from the token; zero
	// lets the store resolve the person label of the writer once, at write time.
	AuthorID int64 `json:"-"`
}

type SearchFilter struct {
	Scope scope.Axes
	State string
	Type  string
	// Priority matches the free-text priority of a request exactly.
	Priority string
	Query    string
	Cursor   string
	// FullDescription gibt die Beschreibung ungekürzt zurück. Für eine
	// Trefferliste falsch — 24 volle Beschreibungen sprengen jedes Werkzeuglimit
	// —, für den Dateispiegel notwendig: der gibt sich nicht als Liste aus,
	// sondern als das Dokument selbst, und ein Text, der mitten im Wort endet,
	// sieht dort nicht gekürzt aus, sondern beschädigt.
	FullDescription bool
	Limit           int
	// Restrict begrenzt die Treffer auf Aufträge ohne Projekt und auf die
	// Projekte in Projects (Sichtbarkeit nach Rolle). Das gehört in die
	// Abfrage und nicht in einen Filter danach: sonst stimmen Seitengröße und
	// Cursor nicht mehr.
	Restrict bool
	Projects []string
	// GlobalAccount: das Konto, dessen Aufträge ohne Projekt der Betrachter lesen
	// darf (seine eigenen); GlobalOwner (Instanz-Owner) bekommt zusätzlich die
	// ohne Besitzer. Alle anderen globalen Aufträge fallen unter Restrict weg.
	GlobalAccount int64
	GlobalOwner   bool
	// UnclaimedAuthor: Aufträge dieses Autors in Remotes ohne Projektzeile bleiben
	// sichtbar; UnclaimedAll (Instanz-Admin) zeigt alle dort.
	UnclaimedAuthor string
	UnclaimedAll    bool
	// HideHeld lässt distillierte, noch nicht übernommene Aufträge aus privaten
	// Sessions anderer Konten weg (HeldViewer ist das Konto des Betrachters,
	// HeldInstanceOwner der Besitzer von Altbestand ohne Konto).
	HideHeld          bool
	HeldViewer        int64
	HeldInstanceOwner int64
}

type SearchHit struct {
	Request       Request `json:"request"`
	OpenCriteria  int     `json:"open_criteria"`
	LatestHandoff string  `json:"latest_handoff,omitempty"`
	MatchReason   string  `json:"match_reason,omitempty"`
	// Match is set by a hybrid search, and only to "semantic" for a request that
	// the embedding found alone: its words do not match the query, its meaning
	// does. A request the full-text index found leaves it empty.
	Match string `json:"match,omitempty"`
	// Sightings zählt die unabhängigen Sessions, in denen der Wunsch fiel. Ein
	// Eintrag aus vier Sessions ist eine Anforderung, die nicht gebaut wird;
	// einer aus einer war vielleicht lautes Denken. Null für Handgeschriebenes.
	Sightings int `json:"sightings,omitempty"`
}

type SearchPage struct {
	Results    []SearchHit `json:"results"`
	NextCursor string      `json:"next_cursor,omitempty"`
	// Truncated is set on the last page of a ranked search whose ranking by words
	// was cut at its depth: more requests contain the words than the ranking
	// holds, and no cursor leads to them. Narrow the query or the filter.
	Truncated bool `json:"truncated,omitempty"`
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
