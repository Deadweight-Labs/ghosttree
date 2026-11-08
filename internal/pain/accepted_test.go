package pain

import (
	"errors"
	"slices"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

func code(err error) string {
	var r *requestdomain.RuleError
	if errors.As(err, &r) {
		return r.ErrorCode
	}
	return ""
}

// REQ-616: der Zustand accepted steht an seinem Platz, zählt als geschlossen und
// verlangt eine Begründung.
func TestAcceptedHasItsPlaceInTheOrderAndCountsAsClosed(t *testing.T) {
	if want := []string{"active", "mitigated", "accepted", "resolved", "obsolete"}; !slices.Equal(States, want) {
		t.Fatalf("States = %v, want %v", States, want)
	}
	if !ValidState(Accepted) {
		t.Fatal("accepted is a state")
	}
	for state, closed := range map[string]bool{Active: false, Mitigated: false, Accepted: true, Resolved: true, Obsolete: true} {
		if Closed(state) != closed {
			t.Errorf("Closed(%s) = %v, want %v", state, !closed, closed)
		}
	}
}

func TestAcceptedTransitions(t *testing.T) {
	ev := Evidence{Kind: "commit", Ref: "abc1234"}
	cases := []struct {
		name, from, to, remaining, note string
		ev                              Evidence
		want                            string
	}{
		{"accept needs a reason", Active, Accepted, "", "", Evidence{}, "accepted_reason_required"},
		{"a blank reason is none", Active, Accepted, "", "   ", Evidence{}, "accepted_reason_required"},
		{"accept from active", Active, Accepted, "", "kostet mehr als es bringt", Evidence{}, ""},
		{"accept from mitigated", Mitigated, Accepted, "", "bleibt so", Evidence{}, ""},
		{"restate the reason", Accepted, Accepted, "", "neuer Grund", Evidence{}, ""},
		{"restate without reason", Accepted, Accepted, "", "", Evidence{}, "accepted_reason_required"},
		{"back to active needs a note", Accepted, Active, "", "", Evidence{}, "note_required"},
		{"back to active with a note", Accepted, Active, "", "Lage hat sich geändert", Evidence{}, ""},
		{"resolved needs evidence", Accepted, Resolved, "", "", Evidence{}, "evidence_required"},
		{"resolved with evidence", Accepted, Resolved, "", "", ev, ""},
		{"mitigated needs remaining", Accepted, Mitigated, "", "", ev, "remaining_required"},
		{"mitigated with remaining", Accepted, Mitigated, "Rest", "", ev, ""},
		{"obsolete needs a note", Accepted, Obsolete, "", "", Evidence{}, "note_required"},
		{"obsolete with a note", Accepted, Obsolete, "", "weg", Evidence{}, ""},
		{"resolved cannot be accepted", Resolved, Accepted, "", "grund", Evidence{}, "invalid_transition"},
		{"obsolete cannot be accepted", Obsolete, Accepted, "", "grund", Evidence{}, "invalid_transition"},
	}
	for _, c := range cases {
		if got := code(ValidateTransition(c.from, c.to, c.remaining, c.ev, c.note)); got != c.want {
			t.Errorf("%s: %s -> %s gave %q, want %q", c.name, c.from, c.to, got, c.want)
		}
	}
}
