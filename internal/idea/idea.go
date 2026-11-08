// Package idea beschreibt Ideen (IDEA-n, REQ-606): der leichte Eingang neben
// den Pains. Ein Pain ist ein Problem ohne Lösung, eine Idee ein Wunsch ohne
// Ausarbeitung. Beide können Issues (Requests) hervorbringen. Eine Idee hat
// Titel, einen kurzen Text und drei Zustände; mehr nicht: keinen Typ, keine
// Priorität, keine Kriterien.
package idea

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// Zustände einer Idee.
const (
	Open      = "open"
	Accepted  = "accepted"
	Dismissed = "dismissed"
)

// States sind alle Zustände.
var States = []string{Open, Accepted, Dismissed}

// ValidState sagt, ob s ein Zustand ist.
func ValidState(s string) bool {
	for _, v := range States {
		if v == s {
			return true
		}
	}
	return false
}

// Idea ist ein Wunsch.
type Idea struct {
	ID      int64  `json:"id"`
	Project string `json:"project"`
	Title   string `json:"title"`
	Body    string `json:"body,omitempty"`
	State   string `json:"state"`
	// Phase ist der Stand der Ausarbeitung (REQ-611): raw, concept, discussion,
	// approved, building, done oder dismissed. Ohne Ausarbeitung bleibt sie raw.
	Phase string `json:"phase"`
	// OpenDecisions zählt die offenen Entscheidungen an der Idee, HasConcept sagt,
	// ob ein Konzept vorliegt.
	OpenDecisions int  `json:"open_decisions,omitempty"`
	HasConcept    bool `json:"has_concept,omitempty"`
	// Pains sind die Probleme, die diese Idee motiviert haben (Relation „motiviert“
	// Pain → Idee, REQ-606), soweit der Betrachter sie lesen darf.
	Pains []PainRef `json:"pains,omitempty"`
	// UncoveredRequirements zählt die Anforderungen des Konzepts, die noch kein
	// erfülltes Kriterium haben und nicht erlassen sind. Sie steht nur bei einer
	// Idee in Umsetzung oder umgesetzt.
	UncoveredRequirements int `json:"uncovered_requirements,omitempty"`
	// Hint ist der einmalige, nicht blockierende Vorschlag, die Idee
	// auszuarbeiten. Er steht nur in der Antwort auf das Anlegen.
	Hint string `json:"hint,omitempty"`
	// Reason ist der Grund eines Verwerfens (Pflicht dort).
	Reason    string `json:"reason,omitempty"`
	Person    string `json:"person,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	// Requests sind die Issues, die aus der Idee entstanden sind.
	Requests []int64 `json:"requests,omitempty"`
	Labels   []Label `json:"labels,omitempty"`
	// GitAnchor und OriginContext sind der Git-Stand und der Entstehungskontext
	// beim Anlegen (REQ-597, REQ-600); nil ohne.
	GitAnchor     *gitanchor.Anchor  `json:"git_anchor,omitempty"`
	OriginContext *originctx.Context `json:"origin_context,omitempty"`
	// AuthorID ist das Konto des Autors, es verlässt den Server nie.
	AuthorID int64 `json:"-"`
	// Match ist "semantic", wenn nur die Bedeutung die Idee fand; sonst leer.
	Match string `json:"match,omitempty"`
}

// Label ist die Kurzform eines Labels an einem Objekt.
type Label struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// PainRef nennt ein Problem, das eine Idee motiviert hat.
type PainRef struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

// HumanID ist IDEA-n.
func (i Idea) HumanID() string { return fmt.Sprintf("IDEA-%d", i.ID) }

// Filter begrenzt eine Suche. State "" heißt offene, "all" alle.
type Filter struct {
	Project string
	State   string
	Query   string
	Limit   int
}

// Actor ist der Schreiber, vom Server aus dem Token gebildet.
type Actor struct {
	AccountID int64
	Person    string
	SessionID int64
	// SessionRef ist die Kennung der Session (Wissen verweist so).
	SessionRef string
	// Agent sagt, dass ein Agent schreibt (die Anfrage trägt eine eigene Session).
	// Es ist eine Angabe, keine Sicherheitsgrenze: sie bestimmt, wer eine
	// Entscheidung als „vom Agenten“ trifft.
	Agent     bool
	GitAnchor *gitanchor.Anchor
	Origin    *originctx.Context
}

// Maße.
const (
	MaxTitle = 200
	MaxBody  = 2000
)

// CleanTitle prüft einen Titel: nicht leer, eine Zeile, ohne Steuerzeichen.
func CleanTitle(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", requestdomain.NewRuleError("title_required", "an idea needs a title", "say the wish in one line", nil)
	}
	if len([]rune(s)) > MaxTitle {
		return "", requestdomain.NewRuleError("title_too_long", fmt.Sprintf("the title is longer than %d characters", MaxTitle), "keep the wish short; the details belong in a request", nil)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", requestdomain.NewRuleError("title_invalid", "the title is one line without control characters", "remove line breaks from the title", nil)
		}
	}
	return s, nil
}

// CleanBody prüft den Text: höchstens MaxBody Zeichen, Zeilenumbrüche erlaubt.
func CleanBody(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > MaxBody {
		return "", requestdomain.NewRuleError("body_too_long", fmt.Sprintf("the text is longer than %d characters", MaxBody), "an idea is a few sentences; write a request for the full story", nil)
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' {
			return "", requestdomain.NewRuleError("body_invalid", "the text has control characters", "remove them", nil)
		}
	}
	return s, nil
}

// CleanReason prüft den Grund eines Verwerfens: eine Zeile, höchstens 500
// Zeichen, ohne Steuerzeichen. Er landet in Listen und Briefings.
func CleanReason(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 500 {
		return "", requestdomain.NewRuleError("reason_too_long", "the reason is longer than 500 characters", "say it in a sentence", nil)
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return "", requestdomain.NewRuleError("reason_invalid", "the reason is one line without control characters", "remove line breaks", nil)
		}
	}
	return s, nil
}

// ValidateTransition: open darf angenommen oder verworfen werden (verwerfen
// braucht einen Grund), ein verworfene Idee darf wieder geöffnet werden; eine
// angenommene bleibt angenommen, denn aus ihr entstand ein Request.
func ValidateTransition(from, to, reason string) error {
	if !ValidState(to) {
		return requestdomain.NewRuleError("invalid_transition", fmt.Sprintf("unknown idea state %q", to), "use open, accepted or dismissed", nil)
	}
	switch {
	case from == Open && to == Accepted, from == Open && to == Dismissed, from == Dismissed && to == Open, from == Dismissed && to == Accepted:
	case from == to:
		return requestdomain.NewRuleError("invalid_transition", fmt.Sprintf("the idea is already %s", from), "nothing to do", nil)
	default:
		return requestdomain.NewRuleError("invalid_transition", fmt.Sprintf("an idea cannot go from %s to %s", from, to), "an accepted idea stays accepted; its requests carry on", nil)
	}
	if to == Dismissed && strings.TrimSpace(reason) == "" {
		return requestdomain.NewRuleError("reason_required", "dismissing an idea needs a reason", "say why in reason", nil)
	}
	return nil
}

// ParseRef liest IDEA-3 oder 3.
func ParseRef(s string) (int64, bool) {
	s = strings.TrimSpace(strings.ToUpper(s))
	s = strings.TrimPrefix(s, "IDEA-")
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n <= 0 || fmt.Sprint(n) != s {
		return 0, false
	}
	return n, true
}
