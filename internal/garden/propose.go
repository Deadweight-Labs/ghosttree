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

// RelationRef is the session reference of a proposal about entries that belong
// together (duplicate or contradiction); it names the kind and the entries.
func RelationRef(r Relation) string {
	ids := make([]string, len(r.IDs))
	for i, id := range r.IDs {
		ids[i] = fmt.Sprint(id)
	}
	return fmt.Sprintf("gardener:relation:%s:%s", r.Kind, strings.Join(ids, "-"))
}

// RelationLabel is the question a relation asks, as the title and the card say it.
func RelationLabel(kind string) string {
	if kind == Contradiction {
		return "Widerspruch?"
	}
	return "Duplikat?"
}

// relationText is what a relation says: the entries with the verbatim quote of
// each, the reason and the evidence. It is the body of the staged note and the
// first post of a contradiction thread.
func relationText(r Relation, titles map[int64]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Vorschlag des Gärtners (Herkunft gardener, nicht bestätigt): %s\n\n", RelationLabel(r.Kind))
	for _, id := range r.IDs {
		fmt.Fprintf(&b, "- #%d: %s\n  Zitat: \"%s\"\n", id, titles[id], r.Quotes[id])
	}
	fmt.Fprintf(&b, "\nBegründung: %s\n", r.Reason)
	if len(r.Evidence) > 0 {
		fmt.Fprintf(&b, "\nBelege: %s\n", strings.Join(r.Evidence, ", "))
	}
	return b.String()
}

// RelationProposal builds the staged note for a duplicate or a contradiction.
// titles maps entry id to title.
func RelationProposal(project string, r Relation, titles map[int64]string) store.Knowledge {
	var ids []string
	for _, id := range r.IDs {
		ids = append(ids, fmt.Sprintf("#%d", id))
	}
	return store.Knowledge{
		Type:       "note",
		Title:      fmt.Sprintf("%s%s %s: %s", titlePrefix, RelationLabel(r.Kind), strings.Join(ids, " und "), titles[r.IDs[0]]),
		Body:       relationText(r, titles),
		Scope:      scope.Axes{Project: project},
		Confidence: proposalState,
		Status:     "active",
		Origin:     "distilled",
		Harness:    Harness,
		SessionRef: RelationRef(r),
	}
}

// RelationProposed says whether a note about this very relation exists, in any
// state: a person has seen it, and asking again would make their answer count for
// nothing.
func RelationProposed(existing []store.Knowledge, r Relation) bool {
	ref := RelationRef(r)
	for _, k := range existing {
		if k.Harness == Harness && k.SessionRef == ref {
			return true
		}
	}
	return false
}

// ProposeRelations files one staged note per relation not already proposed
// (in any state, so a rejection stays a rejection) and returns how many.
func ProposeRelations(sink Sink, project string, existing []store.Knowledge, rels []Relation, titles map[int64]string) (int, error) {
	have := map[string]bool{}
	for _, k := range existing {
		if k.Harness == Harness && strings.HasPrefix(k.SessionRef, "gardener:relation:") {
			have[k.SessionRef] = true
		}
	}
	written := 0
	for _, r := range rels {
		ref := RelationRef(r)
		if have[ref] {
			continue
		}
		have[ref] = true
		if _, err := sink.Remember(RelationProposal(project, r, titles), scope.Axes{Project: project}); err != nil {
			return written, fmt.Errorf("propose %s: %w", ref, err)
		}
		written++
	}
	return written, nil
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
