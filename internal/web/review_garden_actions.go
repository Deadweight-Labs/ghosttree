package web

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
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
//
// A note takes its target from its session reference, and the regression note
// also the test from its text, so the page does not act on anything that merely
// looks like a note of the gardener. It acts only when the note was written by
// the account the server names as the gardener's (store.WrittenByGardener; the
// API refuses everyone else the harness), has not been edited since, and when the
// entries its title names are the ones its reference names. The card shows
// the entries from the reference, with title and link, never the ones the title
// claims. The test of a regression note is recorded as the note holds it: the
// server has no checkout to check it against, so it is held to a strict shape,
// shown whole before the button, and bound to a note nobody has changed; the
// request that presses the button carries no test of its own.

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

// errNotGardens: the note is not what it says it is, and no action is taken on it.
var errNotGardens = errors.New("the note does not come from the gardener")

// gardenTestRe is the shape of the test a regression note may name: a relative
// file path and a function name, joined by a colon. No spaces, no "..", nothing
// that is not part of a path or an identifier.
var gardenTestRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.\-]*(?:/[A-Za-z0-9_][A-Za-z0-9_.\-]*)*:[A-Za-z_][A-Za-z0-9_]{1,119}$`)

// validTestID: a test as the gardener names it, "path/to/x_test.go:TestName".
func validTestID(t string) bool {
	if len(t) > 300 || !gardenTestRe.MatchString(t) {
		return false
	}
	file, _, _ := strings.Cut(t, ":")
	for _, seg := range strings.Split(file, "/") {
		if seg == ".." || seg == ".git" || seg == "." {
			return false
		}
	}
	return true
}

// titleIDs reads the entry numbers a note's title names: "[Gärtner] Duplikat?
// #12 und #15: Titel" gives 12 and 15.
func titleIDs(title string) ([]int64, bool) {
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(title), "[Gärtner]"))
	_, after, ok := strings.Cut(rest, " #")
	if !ok {
		return nil, false
	}
	list, _, ok := strings.Cut(after, ": ")
	if !ok {
		return nil, false
	}
	var ids []int64
	for _, part := range strings.Split(list, " und #") {
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id < 1 {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}

// sameIDs: the same set of entry numbers, in any order.
func sameIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// gardenerVouches says whether a note is the gardener's: harness and reference
// are only a claim, so the account that wrote it has to be the one the server
// names as the gardener's, and nobody may have changed it since (a note is
// archived before every change, so a note with a history was edited). A failure
// to look is returned as it is.
func (a *app) gardenerVouches(note store.Knowledge, ids []int64) error {
	return a.vouch(note, func() bool {
		got, ok := titleIDs(note.Title)
		return ok && sameIDs(got, ids)
	})
}

// vouch is gardenerVouches for a note whose title is held to its reference by
// titleHolds, which the kinds of note define for themselves.
func (a *app) vouch(note store.Knowledge, titleHolds func() bool) error {
	if note.Harness != gardenerHarness || !a.store.WrittenByGardener(note) {
		return errNotGardens
	}
	if !titleHolds() {
		return errNotGardens
	}
	history, err := a.store.KnowledgeHistory(note.ID)
	if err != nil {
		return err
	}
	if len(history) > 0 {
		return errNotGardens
	}
	return nil
}

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

// openGardenNote: a note that claims to be the gardener's and that nobody has
// decided yet. That is only the claim; gardenerVouches checks the rest.
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
	if err := a.gardenerVouches(note, ids); err != nil {
		return p, err
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
	if err := a.gardenerVouches(note, []int64{id}); err != nil {
		return p, err
	}
	switch kind {
	case gardenKindExisting:
		p.state, p.test = "covered", gardenNoteLine(note.Body, "Test")
		if !validTestID(p.test) {
			return p, errNotGardens
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

// gardenRef is an entry a note speaks of, with what happens to it. It is always
// an entry the reference of the note names, as the viewer may read it.
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
	if _, _, ok := parseCriterionRef(k.SessionRef); ok {
		a.decorateCriterionRow(l, pa, k, row)
		return
	}
	if strings.HasPrefix(k.SessionRef, gardenGhostRef) {
		a.decorateGhostRow(l, pa, k, row)
		return
	}
	if kind, ids, ok := parseRelationRef(k.SessionRef); ok && kind == gardenKindDuplicate {
		row.CanApprove = false
		plan, err := a.planMerge(pa, k)
		if errors.Is(err, errNotGardens) {
			row.Warn = l.msg("review.garden.unverified")
			a.showTargets(l, pa, ids, row)
			return
		}
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
	id, kind, ok := parseRegressionRef(k.SessionRef)
	if !ok {
		return
	}
	// The pitfall the reference names, whatever the title says.
	a.showTargets(l, pa, []int64{id}, row)
	switch kind {
	case gardenKindSketch:
		row.Lines = gardenSketchLines(k.Body)
		if t := gardenNoteLine(k.Body, "Test"); t != "" {
			row.Lines = append([]string{t}, row.Lines...)
		}
		if err := a.gardenerVouches(k, []int64{id}); errors.Is(err, errNotGardens) {
			row.Warn = l.msg("review.garden.unverified")
		}
	case gardenKindExisting, gardenKindUntestable:
		row.CanApprove = false
		if t := gardenNoteLine(k.Body, "Test"); t != "" {
			row.Lines = []string{t}
		}
		plan, err := a.planCover(pa, k)
		if errors.Is(err, errNotGardens) {
			row.Warn = l.msg("review.garden.unverified")
			return
		}
		if err != nil {
			return
		}
		label := l.msg("review.garden.record_cover")
		row.Lines = nil
		if kind == gardenKindUntestable {
			label = l.msg("review.garden.record_none")
		} else {
			row.Lines = []string{plan.test}
		}
		row.Act = &gardenAct{Path: "cover", Label: label, Token: row.Token, Filter: row.Filter}
	}
}

// showTargets puts the entries a note's reference names on its card, with title
// and link, as far as the viewer may read them.
func (a *app) showTargets(l lang, pa *store.ProjectAccess, ids []int64, row *gardenRow) {
	for _, id := range ids {
		if k, err := a.readEntry(pa, id); err == nil {
			row.Refs = append(row.Refs, entryRef(pa, l.msg("review.garden.about"), k))
		}
	}
}

// refuseGardenAction answers an error of planMerge or planCover; it reports whether it did.
func (a *app) refuseGardenAction(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, errNothingToDo):
		http.Error(w, "nothing to change", http.StatusConflict)
	case errors.Is(err, errNotGardens):
		http.Error(w, "forbidden", http.StatusForbidden)
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
