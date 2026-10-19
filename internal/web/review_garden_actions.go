package web

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The two things a person can do with a note of the gardener besides approving
// or rejecting it, and the only way the gardener's findings ever become a change
// to other entries:
//
//   - "Merge" on a duplicate note: the older entry is superseded by the newer one,
//     the same store call and the same right as context_supersede (the right to
//     change every entry involved);
//   - "Record" on a regression note about an existing test or an untestable
//     pitfall: the pitfall gets its regression verdict (covered with the test, or
//     not applicable), the same call and the same right as the regression tool.
//
// Both close the note as confirmed by the person. Nothing here is reachable
// without a person: the gardener has no call for it, and the page offers the
// button only to someone who may do what it does.

const (
	// The session references the gardener writes (garden.RelationRef and
	// garden.CoverRef; a test keeps both in step, so the web package need not import
	// the gardener).
	gardenRelationRef   = "gardener:relation:"
	gardenRegressionRef = "gardener:regression:"

	gardenKindDuplicate  = "duplicate"
	gardenKindExisting   = "existing"
	gardenKindSketch     = "sketch"
	gardenKindUntestable = "untestable"
)

// errNothingToDo: the note no longer asks for anything (the entries changed, or
// somebody was quicker).
var errNothingToDo = errors.New("nothing to change")

// parseRelationRef reads "gardener:relation:<kind>:<id>-<id>".
func parseRelationRef(ref string) (kind string, ids []int64, ok bool) {
	rest, found := strings.CutPrefix(ref, gardenRelationRef)
	if !found {
		return "", nil, false
	}
	kind, list, found := strings.Cut(rest, ":")
	if !found {
		return "", nil, false
	}
	for _, part := range strings.Split(list, "-") {
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id < 1 {
			return "", nil, false
		}
		ids = append(ids, id)
	}
	return kind, ids, len(ids) > 0
}

// parseRegressionRef reads "gardener:regression:<id>:<kind>".
func parseRegressionRef(ref string) (id int64, kind string, ok bool) {
	rest, found := strings.CutPrefix(ref, gardenRegressionRef)
	if !found {
		return 0, "", false
	}
	raw, kind, found := strings.Cut(rest, ":")
	if !found {
		return 0, "", false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, kind, err == nil && id > 0
}

// gardenNoteLine is the value of the first line "<label>: value" of a note.
func gardenNoteLine(body, label string) string {
	for _, line := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), label+": "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// gardenSketchLines is the steps of a test sketch, as the note holds them.
func gardenSketchLines(body string) []string {
	_, after, ok := strings.Cut(body, "Schritte:\n")
	if !ok {
		return nil
	}
	if i := strings.Index(after, "\n\n"); i >= 0 {
		after = after[:i]
	}
	var out []string
	for _, l := range strings.Split(after, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// openGardenNote: a note of the gardener that nobody has decided yet.
func openGardenNote(k store.Knowledge) bool {
	return k.Harness == gardenerHarness && k.Confidence == "staged" && k.Status == "active"
}

// current: an entry that is still in force.
func currentEntry(k store.Knowledge) bool { return k.Status == "active" || k.Status == "stale" }

// readEntry loads an entry a note names, for a viewer who must be allowed to read it.
func (a *app) readEntry(pa *store.ProjectAccess, id int64) (store.Knowledge, error) {
	k, err := a.store.KnowledgeByID(id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Knowledge{}, errNothingToDo
	}
	if err != nil {
		return store.Knowledge{}, err
	}
	if err := pa.CheckKnowledge(k, store.ActRead); err != nil {
		return store.Knowledge{}, err
	}
	return k, nil
}

// mergePlan is what confirming a duplicate note does: the newest entry stays and
// supersedes the others.
type mergePlan struct {
	note    store.Knowledge
	keep    store.Knowledge
	replace []store.Knowledge
}

// newer says whether a was written after b.
func newer(a, b store.Knowledge) bool {
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt > b.CreatedAt
	}
	return a.ID > b.ID
}

// planMerge works out the merge of a duplicate note for the viewer. Its error is
// an access error (404 or 403, as for any other action), errNothingToDo, or a
// failure to read. The checks are those of context_supersede: every entry is
// current and has exactly the scope of the others, the one that stays can be
// delivered, and the viewer may change every entry involved. Closing the note
// takes the right to verify it.
func (a *app) planMerge(pa *store.ProjectAccess, note store.Knowledge) (mergePlan, error) {
	var p mergePlan
	kind, ids, ok := parseRelationRef(note.SessionRef)
	if !ok || kind != gardenKindDuplicate || len(ids) < 2 || !openGardenNote(note) {
		return p, errNothingToDo
	}
	var entries []store.Knowledge
	for _, id := range ids {
		k, err := a.readEntry(pa, id)
		if err != nil {
			return p, err
		}
		if !currentEntry(k) || k.Scope.Project != note.Scope.Project || (len(entries) > 0 && !k.Scope.Same(entries[0].Scope)) {
			return p, errNothingToDo
		}
		entries = append(entries, k)
	}
	p.note, p.keep = note, entries[0]
	for _, k := range entries[1:] {
		if newer(k, p.keep) {
			p.keep = k
		}
	}
	for _, k := range entries {
		if k.ID != p.keep.ID {
			p.replace = append(p.replace, k)
		}
	}
	if p.keep.Confidence != "trusted" && p.keep.Confidence != "verified" {
		return p, errNothingToDo
	}
	for _, k := range entries {
		if err := pa.CheckKnowledge(k, store.ActEdit); err != nil {
			return p, err
		}
	}
	return p, pa.CheckKnowledge(note, store.ActVerify)
}

// coverPlan is what confirming a regression note does.
type coverPlan struct {
	note, pitfall store.Knowledge
	state, test   string
}

// planCover works out the verdict a regression note proposes. Only the two kinds
// that name an answer have one: an existing test (covered, with the test) and an
// untestable pitfall (not applicable). It needs the right to verify the pitfall,
// the one the regression tool needs, and the right to verify the note.
func (a *app) planCover(pa *store.ProjectAccess, note store.Knowledge) (coverPlan, error) {
	p := coverPlan{note: note}
	id, kind, ok := parseRegressionRef(note.SessionRef)
	if !ok || !openGardenNote(note) {
		return p, errNothingToDo
	}
	switch kind {
	case gardenKindExisting:
		p.state, p.test = "covered", gardenNoteLine(note.Body, "Test")
		if p.test == "" || strings.ContainsAny(p.test, " \t") {
			return p, errNothingToDo
		}
	case gardenKindUntestable:
		p.state = "not_applicable"
	default:
		return p, errNothingToDo
	}
	k, err := a.readEntry(pa, id)
	if err != nil {
		return p, err
	}
	// Somebody judged it meanwhile: the note is out of date.
	if k.Type != "pitfall" || !currentEntry(k) || k.Scope.Project != note.Scope.Project || k.RegressionState != "" {
		return p, errNothingToDo
	}
	p.pitfall = k
	if err := pa.CheckKnowledge(k, store.ActVerify); err != nil {
		return p, err
	}
	return p, pa.CheckKnowledge(note, store.ActVerify)
}

// gardenAct is the one action a note offers besides rejecting it.
type gardenAct struct {
	Path, Label string
	Token       string
	Filter      string
}

// gardenRef is an entry a note speaks of, with what happens to it.
type gardenRef struct {
	Role, Text, Href string
}

func entryRef(pa *store.ProjectAccess, role string, k store.Knowledge) gardenRef {
	return gardenRef{Role: role, Text: fmt.Sprintf("#%d %s", k.ID, pa.KnowledgeView(k).Title), Href: "/ui/knowledge/" + strconv.FormatInt(k.ID, 10)}
}

// decorateGardenRow adds what a duplicate or regression note needs on its card:
// what the action does before it is taken, and the action itself when the viewer
// may take it. A note that has an action does not offer plain approval, which
// would confirm the note and change nothing.
func (a *app) decorateGardenRow(l lang, pa *store.ProjectAccess, k store.Knowledge, row *gardenRow) {
	if kind, _, ok := parseRelationRef(k.SessionRef); ok && kind == gardenKindDuplicate {
		row.CanApprove = false
		plan, err := a.planMerge(pa, k)
		if err != nil {
			return
		}
		row.Refs = append(row.Refs, entryRef(pa, l.msg("review.garden.stays"), plan.keep))
		for _, old := range plan.replace {
			row.Refs = append(row.Refs, entryRef(pa, l.msg("review.garden.replaced"), old))
		}
		row.Act = &gardenAct{Path: "merge", Label: l.msg("review.garden.merge"), Token: row.Token, Filter: row.Filter}
		return
	}
	_, kind, ok := parseRegressionRef(k.SessionRef)
	if !ok {
		return
	}
	switch kind {
	case gardenKindSketch:
		row.Lines = gardenSketchLines(k.Body)
		if t := gardenNoteLine(k.Body, "Test"); t != "" {
			row.Lines = append([]string{t}, row.Lines...)
		}
	case gardenKindExisting, gardenKindUntestable:
		row.CanApprove = false
		if t := gardenNoteLine(k.Body, "Test"); t != "" {
			row.Lines = []string{t}
		}
		if _, err := a.planCover(pa, k); err != nil {
			return
		}
		label := l.msg("review.garden.record_cover")
		if kind == gardenKindUntestable {
			label = l.msg("review.garden.record_none")
		}
		row.Act = &gardenAct{Path: "cover", Label: label, Token: row.Token, Filter: row.Filter}
	}
}

// refuseGardenAction answers an error of planMerge or planCover; it reports whether it did.
func (a *app) refuseGardenAction(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, errNothingToDo):
		http.Error(w, "nothing to change", http.StatusConflict)
	case errors.Is(err, store.ErrAccessForbidden), errors.Is(err, store.ErrAccessNotFound):
		a.accessDenied(w, r, err)
	default:
		http.Error(w, "could not read", http.StatusInternalServerError)
	}
	return true
}

// confirmGardenNote closes the note as confirmed by the person, the way approving
// it does.
func (a *app) confirmGardenNote(note store.Knowledge, person string) error {
	return a.store.UpdateKnowledgeBy(note.ID, map[string]string{"confidence": "verified", "confirmed_by": person}, person)
}

// reviewMerge merges the duplicates a note of the gardener names: the newest entry
// supersedes the older ones, with the reason "Gärtner-Vorschlag #<note>, bestätigt
// von <person>". The right is that of superseding; the store call is that of
// context_supersede.
func (a *app) reviewMerge(w http.ResponseWriter, r *http.Request) {
	note, ok := a.lookupKnowledge(w, r, store.ActRead)
	if !ok {
		return
	}
	pa := a.access(r)
	plan, err := a.planMerge(pa, note)
	if a.refuseGardenAction(w, r, err) {
		return
	}
	person := personOf(r)
	var old []int64
	for _, k := range plan.replace {
		old = append(old, k.ID)
	}
	reason := fmt.Sprintf("Gärtner-Vorschlag #%d, bestätigt von %s", note.ID, person)
	account, _ := store.AccountNumericID(browserPrincipal(r).ID)
	err = a.store.SupersedeKnowledge(plan.keep.ID, old, reason, store.RelationActor{Name: person, AccountID: account, Via: "web"})
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotCurrent), errors.Is(err, store.ErrNotDeliverable), errors.Is(err, store.ErrScopeMismatch),
		errors.Is(err, store.ErrRelationInvalid), errors.Is(err, store.ErrRelationProject), errors.Is(err, store.ErrRelationExists), errors.Is(err, store.ErrRelationCycle), errors.Is(err, store.ErrRelationConflict):
		http.Error(w, "nothing to change", http.StatusConflict)
		return
	default:
		http.Error(w, "could not save", http.StatusInternalServerError)
		return
	}
	if err := a.confirmGardenNote(note, person); err != nil {
		http.Error(w, "could not save", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, a.afterDecision(r, plan.keep, "merged"), http.StatusSeeOther)
}

// reviewCover records the regression verdict a note of the gardener proposes:
// covered by the test it names, or not applicable. The call and the right are
// those of the regression tool.
func (a *app) reviewCover(w http.ResponseWriter, r *http.Request) {
	note, ok := a.lookupKnowledge(w, r, store.ActRead)
	if !ok {
		return
	}
	pa := a.access(r)
	plan, err := a.planCover(pa, note)
	if a.refuseGardenAction(w, r, err) {
		return
	}
	if err := a.store.SetRegressionCover(plan.pitfall.ID, plan.state, plan.test); err != nil {
		http.Error(w, "could not save", http.StatusBadRequest)
		return
	}
	if err := a.confirmGardenNote(note, personOf(r)); err != nil {
		http.Error(w, "could not save", http.StatusInternalServerError)
		return
	}
	done := "covered"
	if plan.state == "not_applicable" {
		done = "untestable"
	}
	http.Redirect(w, r, a.afterDecision(r, plan.pitfall, done), http.StatusSeeOther)
}
