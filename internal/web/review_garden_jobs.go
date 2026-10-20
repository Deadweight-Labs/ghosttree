package web

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The two further things a person can do with a note of the gardener (REQ-457,
// jobs 4 and 1), next to merging duplicates and recording regression cover:
//
//   - "Record criterion as met" on a criterion note: the criterion of the open
//     request gets its state "met" with the proof the note names, the same store
//     call (and so the same evidence) as request_record_progress criterion_met,
//     and the right to work on the request;
//   - "Adopt description" on a description note: the path gets the proposed
//     description, the same store call as context_describe_file, and the right
//     to describe in the project.
//
// Both close the note as confirmed by the person, and both need the right to
// verify the note, like the actions of part A. The page offers the button only to
// someone who may do what it does, and the gardener has no call for either.
//
// What the actions act on comes from the server's own data, never from the
// browser: the target is read from the session reference of the note (the
// request and criterion numbers, the path), checked against the title and the
// request or ghost the server holds, and the proof, the text and the content
// hashes are the ones the note holds, which only the gardener's account could
// write and nobody has edited since. The request that presses the button carries
// no form value but its CSRF token. The server has no checkout, so a proof and a
// description are held to their shape here; ctx checked them against the
// checkout before it filed the note, and the card shows them whole.

const (
	gardenCriterionRef = "gardener:criterion:"
	gardenGhostRef     = "gardener:ghost:"
	// gardenGhostWrittenRef is the reference a ghost carries once a person adopted
	// a proposal of the gardener: the note it came from.
	gardenGhostWrittenRef = "gardener:ghost-note:"

	gardenKindCommit = "commit"
	gardenKindTest   = "test"
	gardenKindFile   = "file"

	proofLinePrefix = "Beleg: "
	ghostMarker     = "\nBeschreibung:\n"
)

// proofRefRes are the shapes of proof a criterion note may carry (garden.ValidProofRef
// has the same; a test keeps both in step).
var proofRefRes = map[string]*regexp.Regexp{
	gardenKindCommit: regexp.MustCompile(`^[0-9a-f]{8,40}(?: \(#[0-9]{1,7}\))?$`),
	gardenKindTest:   gardenTestRe,
	gardenKindFile:   regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.\-]*(?:/[A-Za-z0-9_][A-Za-z0-9_.\-]*)*$`),
}

func validProofRef(kind, ref string) bool {
	re, ok := proofRefRes[kind]
	if !ok || len(ref) > 300 || !re.MatchString(ref) {
		return false
	}
	if kind == gardenKindCommit {
		return true
	}
	file, _, _ := strings.Cut(ref, ":")
	for _, seg := range strings.Split(file, "/") {
		if seg == ".." || seg == ".git" || seg == "." {
			return false
		}
	}
	return true
}

// proofItem is one line of proof of a criterion note.
type proofItem struct{ Kind, Ref string }

// parseProofItems reads the "Beleg: <kind> <ref>" lines of a criterion note. A
// line that is not of an allowed shape makes the whole note unusable, and so does
// a note without a line or with too many.
func parseProofItems(body string) ([]proofItem, bool) {
	var out []proofItem
	for _, l := range strings.Split(body, "\n") {
		rest, ok := strings.CutPrefix(l, proofLinePrefix)
		if !ok {
			continue
		}
		kind, ref, ok := strings.Cut(rest, " ")
		if !ok || !validProofRef(kind, ref) {
			return nil, false
		}
		out = append(out, proofItem{kind, ref})
	}
	return out, len(out) > 0 && len(out) <= 4
}

// recordedProof is the proof as it is recorded as evidence: the kind of the first
// item (the notes list commits before tests before files) and all items in one
// reference, the first as it is and the others with their kind in front.
func recordedProof(items []proofItem) (kind, ref string) {
	kind, ref = items[0].Kind, items[0].Ref
	for _, it := range items[1:] {
		ref += "; " + it.Kind + " " + it.Ref
	}
	return kind, ref
}

// parseCriterionRef reads "gardener:criterion:<request>:<criterion>".
func parseCriterionRef(ref string) (request, criterion int64, ok bool) {
	rest, found := strings.CutPrefix(ref, gardenCriterionRef)
	if !found {
		return 0, 0, false
	}
	a, b, found := strings.Cut(rest, ":")
	if !found {
		return 0, 0, false
	}
	request, err1 := strconv.ParseInt(a, 10, 64)
	criterion, err2 := strconv.ParseInt(b, 10, 64)
	return request, criterion, err1 == nil && err2 == nil && request > 0 && criterion > 0
}

// parseCriterionTitle reads "[Gärtner] Erfüllt? #<request> Kriterium <n>: <text>".
func parseCriterionTitle(title string) (request int64, number int, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(title), "[Gärtner] Erfüllt? #")
	if !found {
		return 0, 0, false
	}
	head, _, found := strings.Cut(rest, ": ")
	if !found {
		return 0, 0, false
	}
	a, b, found := strings.Cut(head, " Kriterium ")
	if !found {
		return 0, 0, false
	}
	request, err1 := strconv.ParseInt(a, 10, 64)
	n, err2 := strconv.Atoi(b)
	return request, n, err1 == nil && err2 == nil && request > 0 && n > 0
}

// criterionPlan is what confirming a criterion note does.
type criterionPlan struct {
	note store.Knowledge
	// request is the request as the server holds it, criterion the criterion the
	// note's reference names, as the server holds it.
	request   requestdomain.Detail
	criterion requestdomain.Criterion
	ref       store.ObjectRef
	items     []proofItem
	// kind and evidence are what is recorded, without the line about the origin.
	kind, evidence string
}

// planCriterion works out what a criterion note proposes for the viewer. Its
// error is an access error (404 or 403, as for any other action), errNothingToDo
// (the request or the criterion is settled meanwhile), errNotGardens, or a
// failure to read. The checks, in order: the note is the gardener's account's and
// unedited; the criterion exists and belongs to the request the reference names,
// in the project of the note; the viewer may read the request and work on it; the
// title names the request and the number of that criterion; the criterion and
// the request are open; the proof is of the allowed shapes; and the viewer may
// verify the note.
func (a *app) planCriterion(pa *store.ProjectAccess, note store.Knowledge) (criterionPlan, error) {
	p := criterionPlan{note: note}
	reqID, critID, ok := parseCriterionRef(note.SessionRef)
	if !ok || !openGardenNote(note) {
		return p, errNothingToDo
	}
	if err := a.gardenerNote(note); err != nil {
		return p, err
	}
	ref, err := a.store.RequestRef(store.RefCriterion, critID)
	if errors.Is(err, sql.ErrNoRows) {
		return p, errNothingToDo
	}
	if err != nil {
		return p, err
	}
	// What the viewer may read and work on comes first, so that a note about
	// something they cannot see answers like an unknown number.
	obj, visible := pa.RequestObject(ref)
	if !visible {
		return p, store.ErrAccessNotFound
	}
	if err := pa.Check(ref.Project, store.ResRequest, store.ActRead, obj); err != nil {
		return p, err
	}
	if err := pa.Check(ref.Project, store.ResRequest, store.ActWork, obj); err != nil {
		return p, err
	}
	if ref.ID != reqID || ref.Project == "" || ref.Project != note.Scope.Project {
		return p, errNotGardens
	}
	detail, err := a.store.RequestByID(reqID)
	if errors.Is(err, sql.ErrNoRows) {
		return p, errNothingToDo
	}
	if err != nil {
		return p, err
	}
	var crit *requestdomain.Criterion
	for i := range detail.Criteria {
		if detail.Criteria[i].ID == critID {
			crit = &detail.Criteria[i]
		}
	}
	if crit == nil {
		return p, errNothingToDo
	}
	if titleReq, titleNo, ok := parseCriterionTitle(note.Title); !ok || titleReq != reqID || titleNo != crit.Number {
		return p, errNotGardens
	}
	items, ok := parseProofItems(note.Body)
	if !ok {
		return p, errNotGardens
	}
	// A criterion somebody settled meanwhile, or a request that is closed: the
	// note is out of date, and nothing is recorded twice.
	if detail.Request.State != "open" || crit.State != "open" {
		return p, errNothingToDo
	}
	p.request, p.criterion, p.ref, p.items = detail, *crit, ref, items
	p.kind, p.evidence = recordedProof(items)
	return p, pa.CheckKnowledge(note, store.ActVerify)
}

// gardenerNote says whether a note is the gardener's: harness and reference are
// only a claim, so the account that wrote it has to be the one the server names
// as the gardener's, and nobody may have changed it since (a note is archived
// before every change, so a note with a history was edited).
func (a *app) gardenerNote(note store.Knowledge) error {
	return a.vouch(note, func() bool { return true })
}

// reviewCriterion records the criterion a note of the gardener proposes as met,
// with the proof the note holds. The call is that of request_record_progress
// criterion_met; the evidence says whose proposal it was and who confirmed it.
func (a *app) reviewCriterion(w http.ResponseWriter, r *http.Request) {
	note, ok := a.lookupKnowledge(w, r, store.ActRead)
	if !ok {
		return
	}
	pa := a.access(r)
	plan, err := a.planCriterion(pa, note)
	if a.refuseGardenAction(w, r, err) {
		return
	}
	person := personOf(r)
	if pa.RequestHeldOwner(plan.ref) {
		// The owner of the session a request came from takes it over by working on it,
		// as in every other path that sets a criterion.
		_ = a.store.ClaimDistilledRequest(plan.criterion.ID, store.RefCriterion, person)
	}
	evidence := fmt.Sprintf("%s (Gärtner-Vorschlag #%d, bestätigt von %s)", plan.evidence, note.ID, person)
	err = a.store.SetCriterionState(plan.criterion.ID, "met", requestdomain.Evidence{Kind: plan.kind, Ref: evidence, Person: person})
	switch {
	case err == nil:
	case requestdomain.Code(err) != "", errors.Is(err, sql.ErrNoRows):
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
	http.Redirect(w, r, a.afterDecision(r, note, "criterion"), http.StatusSeeOther)
}

// ghostFacts is what a description note holds besides its text.
type ghostFacts struct {
	SHA, Blob string
	Lines     int
	// Was is the hash of the description the proposal would replace, "keine" for none.
	Was         string
	Description string
}

var (
	ghostSHARe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	ghostBlobRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
	ghostWasRe  = regexp.MustCompile(`^(?:keine|[0-9a-f]{12})$`)
)

// parseGhostNote reads a description note: the header lines before the marker, and
// everything after the marker as the proposed text. Anything that does not have
// the shape makes the note unusable.
func parseGhostNote(body string) (ghostFacts, bool) {
	var f ghostFacts
	head, text, found := strings.Cut(body, ghostMarker)
	if !found {
		return f, false
	}
	var gotStand, gotWas bool
	for _, l := range strings.Split(head, "\n") {
		if rest, ok := strings.CutPrefix(l, "Stand: "); ok && !gotStand {
			fields := strings.Fields(rest)
			if len(fields) != 3 {
				return f, false
			}
			sha, ok1 := strings.CutPrefix(fields[0], "sha=")
			blob, ok2 := strings.CutPrefix(fields[1], "blob=")
			lines, ok3 := strings.CutPrefix(fields[2], "zeilen=")
			n, err := strconv.Atoi(lines)
			if !ok1 || !ok2 || !ok3 || err != nil || n < 0 || n > 50_000_000 || !ghostSHARe.MatchString(sha) || !ghostBlobRe.MatchString(blob) {
				return f, false
			}
			f.SHA, f.Blob, f.Lines, gotStand = sha, blob, n, true
		}
		if rest, ok := strings.CutPrefix(l, "Bisher: "); ok && !gotWas {
			if !ghostWasRe.MatchString(rest) {
				return f, false
			}
			f.Was, gotWas = rest, true
		}
	}
	desc, why := store.CleanGhostDescription(text)
	if !gotStand || !gotWas || why != "" {
		return f, false
	}
	f.Description = desc
	return f, true
}

// parseGhostTitle: the title of a description note is the question and the path.
func ghostTitleOf(path string) string { return "[Gärtner] Beschreibung? " + path }

// describePlan is what confirming a description note does.
type describePlan struct {
	note    store.Knowledge
	path    string
	facts   ghostFacts
	old     store.GhostFile
	hadOld  bool
	project string
}

// planDescribe works out what a description note proposes for the viewer, with
// the errors of planCriterion. The path is the one of the reference and has to
// be the one of the title; the text and the content hashes are the note's; the
// description at the path has to be the one the note was written against (a
// person who wrote another meanwhile is not overwritten); and the viewer needs
// the right to describe in the project and to verify the note.
func (a *app) planDescribe(pa *store.ProjectAccess, note store.Knowledge) (describePlan, error) {
	p := describePlan{note: note}
	path, found := strings.CutPrefix(note.SessionRef, gardenGhostRef)
	if !found || !store.ValidGhostPath(path) || !openGardenNote(note) {
		return p, errNothingToDo
	}
	if err := a.vouch(note, func() bool { return note.Title == ghostTitleOf(path) }); err != nil {
		return p, err
	}
	facts, ok := parseGhostNote(note.Body)
	if !ok || note.Scope.Project == "" {
		return p, errNotGardens
	}
	if err := pa.Check(note.Scope.Project, store.ResGhost, store.ActCreate, store.Object{}); err != nil {
		return p, err
	}
	old, err := a.store.GhostFileByPath(note.Scope.Project, path)
	switch {
	case err == nil:
		p.old, p.hadOld = old, true
	case !errors.Is(err, sql.ErrNoRows):
		return p, err
	}
	// The note was written against the description that stood then. If another
	// one stands now, or it is not a file description, the note is out of date.
	was := "keine"
	if p.hadOld && strings.TrimSpace(old.Description) != "" {
		was = store.GhostDescriptionHash(old.Description)
	}
	if p.hadOld && old.Kind == "dir" || was != facts.Was {
		return p, errNothingToDo
	}
	p.path, p.facts, p.project = path, facts, note.Scope.Project
	return p, pa.CheckKnowledge(note, store.ActVerify)
}

// reviewDescribe writes the description a note of the gardener proposes. The call
// is that of context_describe_file (PutGhostFile); the ghost carries the mark
// that says it was proposed by the gardener and confirmed by this person.
func (a *app) reviewDescribe(w http.ResponseWriter, r *http.Request) {
	note, ok := a.lookupKnowledge(w, r, store.ActRead)
	if !ok {
		return
	}
	plan, err := a.planDescribe(a.access(r), note)
	if a.refuseGardenAction(w, r, err) {
		return
	}
	person := personOf(r)
	_, err = a.store.PutGhostFile(store.GhostFile{
		Project: plan.project, Path: plan.path, Kind: "file", Description: plan.facts.Description,
		ContentSHA: plan.facts.SHA, GitBlob: plan.facts.Blob, LineCount: plan.facts.Lines,
		Person: person, Harness: gardenerHarness, SessionRef: fmt.Sprintf("%s%d", gardenGhostWrittenRef, note.ID),
	})
	if err != nil {
		http.Error(w, "could not save", http.StatusInternalServerError)
		return
	}
	if err := a.confirmGardenNote(note, person); err != nil {
		http.Error(w, "could not save", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, a.afterDecision(r, note, "described"), http.StatusSeeOther)
}

// paragraphs splits a text into its non-empty lines, for the card.
func paragraphs(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// decorateCriterionRow adds what a criterion note needs on its card: the request
// and the criterion as the server holds them, the proof the action will record,
// and the action when the viewer may take it.
func (a *app) decorateCriterionRow(l lang, pa *store.ProjectAccess, k store.Knowledge, row *gardenRow) {
	row.CanApprove = false
	plan, err := a.planCriterion(pa, k)
	if errors.Is(err, errNotGardens) {
		row.Warn = l.msg("review.garden.unverified")
		if reqID, _, ok := parseCriterionRef(k.SessionRef); ok && a.requestReadable(pa, reqID) {
			if d, derr := a.store.RequestByID(reqID); derr == nil {
				row.Refs = append(row.Refs, gardenRef{Role: l.msg("review.garden.about"), Text: fmt.Sprintf("%s %s", d.Request.HumanID(), d.Request.Title), Href: requestURL(reqID)})
			}
		}
		return
	}
	if err != nil {
		return
	}
	row.Refs = append(row.Refs, gardenRef{Role: l.msg("review.garden.about"), Text: fmt.Sprintf("%s %s", plan.request.Request.HumanID(), plan.request.Request.Title), Href: requestURL(plan.request.Request.ID)})
	row.Lines = []string{l.msg("review.garden.criterion", plan.criterion.Number, plan.criterion.Description)}
	for _, it := range plan.items {
		row.Lines = append(row.Lines, l.msg("review.garden.evidence", it.Kind, it.Ref))
	}
	row.Act = &gardenAct{Path: "criterion", Label: l.msg("review.garden.record_proof"), Token: row.Token, Filter: row.Filter}
}

// decorateGhostRow adds what a description note needs on its card: the path, the
// description that stands and the one proposed, side by side, and the action when
// the viewer may take it.
func (a *app) decorateGhostRow(l lang, pa *store.ProjectAccess, k store.Knowledge, row *gardenRow) {
	row.CanApprove = false
	plan, err := a.planDescribe(pa, k)
	if errors.Is(err, errNotGardens) {
		row.Warn = l.msg("review.garden.unverified")
		return
	}
	if err != nil {
		return
	}
	old := []string{l.msg("review.garden.no_old")}
	if plan.hadOld && strings.TrimSpace(plan.old.Description) != "" {
		old = paragraphs(plan.old.Description)
	}
	row.Texts = []gardenText{
		{Label: l.msg("review.garden.old_text"), Lines: old},
		{Label: l.msg("review.garden.new_text"), Lines: paragraphs(plan.facts.Description)},
	}
	row.Act = &gardenAct{Path: "describe", Label: l.msg("review.garden.adopt"), Token: row.Token, Filter: row.Filter}
}
