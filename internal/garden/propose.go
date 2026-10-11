package garden

import (
	"fmt"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const (
	// Harness marks every entry the gardener writes. The origin column only
	// admits agent|distilled|human, so provenance is carried here and in the
	// session reference until a schema change adds "gardener" itself.
	Harness       = "gardener"
	refPrefix     = "gardener:check-knowledge:"
	titlePrefix   = "[Gärtner] "
	proposalState = "staged"
)

// Sink is the slice of the server client that proposing needs. There is
// deliberately no way to patch, verify, deprecate or delete through it.
type Sink interface {
	Remember(k store.Knowledge, autoCtx scope.Axes) (store.Knowledge, error)
}

// ProposalRef is the session reference that ties a proposal to its subject.
func ProposalRef(id int64) string { return fmt.Sprintf("%s%d", refPrefix, id) }

// IsProposalFor reports whether k is a gardener proposal about entry id, in
// whatever state it is now.
func IsProposalFor(k store.Knowledge, id int64) bool {
	return k.Harness == Harness && k.SessionRef == ProposalRef(id)
}

// alreadyProposed says whether this entry has had a gardener proposal that
// still stands for its current wording. A rejected proposal counts too: a
// person said no, and asking again every night would make that meaningless.
// Only a change to the entry after the proposal was made reopens the question.
func alreadyProposed(existing []store.Knowledge, e Entry) bool {
	changed := parseTime(e.UpdatedAt)
	for _, k := range existing {
		if !IsProposalFor(k, e.ID) {
			continue
		}
		if changed.IsZero() || !changed.After(parseTime(k.CreatedAt)) {
			return true
		}
	}
	return false
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// Proposal builds the new entry for a stale or correction verdict. It is a
// separate note in the review queue, staged and never verified; the entry it
// is about is not touched, so rejecting the proposal costs nothing.
func Proposal(project string, v Verdict) (store.Knowledge, bool) {
	if v.Kind != Stale && v.Kind != Correction {
		return store.Knowledge{}, false
	}
	var b strings.Builder
	label := "Veraltet?"
	if v.Kind == Correction {
		label = "Korrektur?"
	}
	fmt.Fprintf(&b, "Vorschlag des Gärtners (Herkunft gardener, nicht bestätigt) zu Eintrag #%d: %s\n\n", v.Entry.ID, v.Entry.Title)
	fmt.Fprintf(&b, "Urteil: %s\n\nBegründung: %s\n", label, v.Reason)
	if len(v.Evidence) > 0 {
		fmt.Fprintf(&b, "\nBelege: %s\n", strings.Join(v.Evidence, ", "))
	}
	if v.Kind == Correction && v.Correction != "" {
		fmt.Fprintf(&b, "\nVorgeschlagener Text:\n%s\n", v.Correction)
	}
	return store.Knowledge{
		Type:       "note",
		Title:      fmt.Sprintf("%s%s #%d: %s", titlePrefix, label, v.Entry.ID, v.Entry.Title),
		Body:       b.String(),
		Scope:      scope.Axes{Project: project},
		Confidence: proposalState,
		Status:     "active",
		Origin:     "distilled",
		Harness:    Harness,
		SessionRef: ProposalRef(v.Entry.ID),
	}, true
}

// Propose files one staged note per stale or correction verdict and returns how
// many it wrote. A subject that already has an open proposal is left alone, so
// a nightly run does not pile up duplicates.
func Propose(sink Sink, project string, existing []store.Knowledge, verdicts []Verdict) (written int, err error) {
	for _, v := range verdicts {
		k, ok := Proposal(project, v)
		if !ok {
			continue
		}
		if alreadyProposed(existing, v.Entry) {
			continue
		}
		if _, err := sink.Remember(k, scope.Axes{Project: project}); err != nil {
			return written, fmt.Errorf("propose for #%d: %w", v.Entry.ID, err)
		}
		written++
	}
	return written, nil
}
