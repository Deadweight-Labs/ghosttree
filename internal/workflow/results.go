package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/knref"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Result is one thing a block wants to create (REQ-695). The block never
// writes: it hands its results to the engine, which keeps only what the
// workflow declared (kind and number), what the owner allows, and what carries
// at least one quote found word for word in the inputs of the run, and writes
// that as a proposal for a person to accept, or at once where the owner switched
// on the automatic approval for the kind.
type Result struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	// Type of a knowledge entry: pitfall, decision or note.
	Type string `json:"type,omitempty"`
	// Evidence are quotes from the inputs of the run; each is checked.
	Evidence []string `json:"evidence"`
	// RequestID is the issue a criterion or a relation is about; OtherID the
	// other issue of a relation, Relation its kind.
	RequestID int64  `json:"request_id,omitempty"`
	OtherID   int64  `json:"other_id,omitempty"`
	Relation  string `json:"relation,omitempty"`
	// IdeaID is the idea a comment, a requirement or a relation to a feature
	// is about; FeatureID the feature of such a relation (Relation "affects").
	IdeaID    int64 `json:"idea_id,omitempty"`
	FeatureID int64 `json:"feature_id,omitempty"`
	// Session names the session (its public address) the evidence comes from:
	// then every quote has to be in what a person typed in that session.
	Session string `json:"session,omitempty"`
	// SameAs is the open issue that already carries a proposed issue: the run
	// points to it instead of proposing a second one.
	SameAs int64 `json:"same_as,omitempty"`
}

// MinEvidenceRunes is the shortest quote that counts as evidence: a word or two
// would be found in almost any input.
const MinEvidenceRunes = 12

// ResultTitlePrefix marks the notes on the review page that stand for a
// proposed issue, criterion or relation of a workflow.
const ResultTitlePrefix = "[Workflow] "

// Harness marks every entry a workflow run writes.
const Harness = "workflow"

func normalizeSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// hasEvidence says whether every quote is long enough and found in the inputs.
func hasEvidence(quotes []string, inputs string) bool {
	if len(quotes) == 0 {
		return false
	}
	in := normalizeSpace(inputs)
	for _, q := range quotes {
		q = normalizeSpace(q)
		if utf8.RuneCountInString(q) < MinEvidenceRunes || !strings.Contains(in, q) {
			return false
		}
	}
	return true
}

// Gate is the engine's part that writes the results of one run.
type Gate struct {
	Store  *store.Store
	Run    store.WorkflowRun
	Def    *Def
	Policy store.WorkflowPolicy
	// Inputs is the full input of the run, against which evidence is checked.
	Inputs string
	// Sources are the texts a result may name as its source (Result.Session),
	// by name: what a person typed in one session.
	Sources map[string]string
}

// GateReport counts what the gate did.
type GateReport struct {
	Proposed, Auto, Rejected int
	// Linked counts results that point to what is already there.
	Linked int
}

// String is the short, lasting summary for the run's result (no model text).
func (g GateReport) String() string {
	s := fmt.Sprintf("%d proposed, %d applied, %d refused", g.Proposed, g.Auto, g.Rejected)
	if g.Linked > 0 {
		s += fmt.Sprintf(", %d already open", g.Linked)
	}
	return s
}

// Apply checks and writes the results. A result that is refused is recorded
// with its reason, so that the run page says why.
func (g Gate) Apply(results []Result) (GateReport, error) {
	var rep GateReport
	// Only an account that still holds the right to act for the project
	// (active, lead or owner there) lets the run write in its name: the rights
	// the author had when saving the version may be gone by now.
	account := g.Store.WorkflowRunActor(g.Run)
	person := ""
	if acc, err := g.Store.AccountByPrincipalID(fmt.Sprintf("person:%d", account)); err == nil && account > 0 {
		person = acc.Name
	}
	used := map[string]int{}
	for _, r := range results {
		rec := store.WorkflowResult{RunID: g.Run.ID, Project: g.Run.Project, Kind: r.Kind}
		reason := ""
		switch {
		case g.Def == nil || g.Def.MaxOf(r.Kind) == 0:
			reason = store.ResultNotDeclared
		case used[r.Kind] >= g.Def.MaxOf(r.Kind):
			reason = store.ResultOverMax
		case slices.Contains(g.Policy.DenyKinds, r.Kind):
			reason = store.ResultPolicy
		case !hasEvidence(r.Evidence, g.Inputs):
			reason = store.ResultNoEvidence
		case r.Session != "" && !hasEvidence(r.Evidence, g.Sources[r.Session]):
			// The quote has to be what a person typed in exactly that session.
			reason = store.ResultNoEvidence
		case account == 0 || person == "":
			reason = store.ResultInvalid
		case r.Kind == ResultIssue && r.SameAs > 0:
			// The wish is already an open issue: the record points to it, and
			// nothing is proposed a second time.
			if openRequestIn(g.Store, g.Run.Project, r.SameAs) {
				rec.State, rec.Reason, rec.Ref = store.ResultRejected, store.ResultExists, fmt.Sprintf("REQ-%d", r.SameAs)
				if _, err := g.Store.RecordWorkflowResult(rec); err != nil {
					return rep, err
				}
				rep.Linked++
				continue
			}
			reason = store.ResultBadTarget
		default:
			reason = validResult(g.Store, g.Run.Project, r)
		}
		if reason != "" {
			rec.State, rec.Reason = store.ResultRejected, reason
			if _, err := g.Store.RecordWorkflowResult(rec); err != nil {
				return rep, err
			}
			rep.Rejected++
			continue
		}
		used[r.Kind]++
		payload, _ := json.Marshal(r)
		rec.Payload = string(payload)
		w := writer{st: g.Store, run: g.Run, person: person, account: account}
		// A comment and a requirement always wait for a person, and so does a
		// relation of an idea to a feature: the owner's automatic approval
		// covers knowledge, issues, criteria and relations between issues.
		if g.Policy.Auto(r.Kind) && r.IdeaID == 0 {
			ref, err := w.apply(r, 0)
			if err != nil {
				rec.State, rec.Reason, rec.Payload = store.ResultRejected, store.ResultInvalid, ""
				_, _ = g.Store.RecordWorkflowResult(rec)
				rep.Rejected++
				continue
			}
			rec.State, rec.Ref = store.ResultAuto, ref
			if _, err := g.Store.RecordWorkflowResult(rec); err != nil {
				return rep, err
			}
			rep.Auto++
			continue
		}
		noteID, err := w.propose(r)
		if err != nil {
			return rep, err
		}
		rec.State, rec.NoteID, rec.Ref = store.ResultProposed, noteID, knref.Format(noteID)
		if _, err := g.Store.RecordWorkflowResult(rec); err != nil {
			return rep, err
		}
		rep.Proposed++
	}
	return rep, nil
}

// validResult checks what a result is about: a title, a kind of knowledge, an
// issue of the project for a criterion or a relation.
func validResult(st *store.Store, project string, r Result) string {
	if strings.TrimSpace(r.Title) == "" && r.Kind != ResultRelation && r.Kind != ResultComment {
		return store.ResultInvalid
	}
	inProject := func(id int64) bool { return openRequestIn(st, project, id) }
	ideaOK := func(id int64) bool {
		i, err := st.IdeaByID(id)
		return err == nil && i.Project == project && i.State != "dismissed"
	}
	if r.IdeaID > 0 && !ideaOK(r.IdeaID) {
		return store.ResultBadTarget
	}
	switch r.Kind {
	case ResultKnowledge:
		if r.Type != "" && r.Type != "pitfall" && r.Type != "decision" && r.Type != "note" {
			return store.ResultInvalid
		}
	case ResultIssue:
	case ResultCriterion:
		if !inProject(r.RequestID) {
			return store.ResultBadTarget
		}
	case ResultRelation:
		if r.IdeaID > 0 {
			// An idea affects a feature of the same project.
			fp, ok := st.FeatureProjectOf(r.FeatureID)
			if !ok || fp != project || r.Relation != "affects" {
				return store.ResultBadTarget
			}
			return ""
		}
		if !inProject(r.RequestID) || !inProject(r.OtherID) || r.RequestID == r.OtherID || !slices.Contains([]string{"related", "blocks", "duplicates", "supersedes", "derived"}, r.Relation) {
			return store.ResultBadTarget
		}
	case ResultComment:
		if r.IdeaID == 0 || strings.TrimSpace(r.Body) == "" {
			return store.ResultBadTarget
		}
	case ResultRequirement:
		if r.IdeaID == 0 {
			return store.ResultBadTarget
		}
		if utf8.RuneCountInString(strings.TrimSpace(r.Title)) > idea.MaxRequirement {
			return store.ResultInvalid
		}
	default:
		// A kind the engine cannot write yet (a description of a file is the
		// gardener's, through its own unit).
		return store.ResultInvalid
	}
	return ""
}

// writer writes on behalf of a run: everything carries the run's reference,
// so that the chain guard sees the run as the origin of what follows.
type writer struct {
	st      *store.Store
	run     store.WorkflowRun
	person  string
	account int64
}

func (w writer) ref() string { return store.WorkflowRunRefPrefix + strconv.FormatInt(w.run.ID, 10) }

// access is the rights of the account the run acts for: a comment, a concept
// and a link of a feature are written through them, as that account would.
func (w writer) access() *store.ProjectAccess {
	return w.st.Access(store.Principal{ID: fmt.Sprintf("person:%d", w.account), Label: w.person})
}

// sessionLine points to the session a wish was typed in.
func sessionLine(ref string) string {
	return "Gesagt in [Session " + ref + "](/ui/sessions/" + url.PathEscape(ref) + ")."
}

// openRequestIn says whether an issue is open and belongs to the project.
func openRequestIn(st *store.Store, project string, id int64) bool {
	d, err := st.RequestByID(id)
	return err == nil && d.Request.Scope.Project == project && d.Request.State == "open"
}

// actor is how the history of a criterion or a relation names who added it:
// the account the run acts for, and the run. Neither carries a session
// reference of its own, so the run's reference stands with the person, where
// whoever reads the history of the issue sees that a workflow wrote it.
func (w writer) actor() string { return w.person + " (" + w.ref() + ")" }

// propose writes the staged entry a person accepts on the review page: the
// knowledge entry itself, or a note that stands for an issue, a criterion or a
// relation.
func (w writer) propose(r Result) (int64, error) {
	return w.st.InsertKnowledge(w.note(r))
}

// note is the staged entry of a proposal. Acceptance compares the entry with
// it again, so that what a person read is exactly what is applied.
func (w writer) note(r Result) store.Knowledge {
	k := store.Knowledge{Type: "note", Title: r.Title, Body: r.Body, Scope: scope.Axes{Project: w.run.Project}, Confidence: "staged", Status: "active",
		Origin: "distilled", Person: w.person, Harness: Harness, SessionRef: w.ref(), AuthorID: w.account}
	switch r.Kind {
	case ResultKnowledge:
		if r.Type != "" {
			k.Type = r.Type
		}
	case ResultIssue:
		k.Title = ResultTitlePrefix + "Issue? " + r.Title
	case ResultCriterion:
		k.Title = fmt.Sprintf("%sKriterium? REQ-%d: %s", ResultTitlePrefix, r.RequestID, r.Title)
	case ResultRelation:
		k.Title = fmt.Sprintf("%sBeziehung? REQ-%d %s REQ-%d", ResultTitlePrefix, r.RequestID, r.Relation, r.OtherID)
		if r.IdeaID > 0 {
			k.Title = fmt.Sprintf("%sBeziehung? IDEA-%d %s FEAT-%d", ResultTitlePrefix, r.IdeaID, r.Relation, r.FeatureID)
		}
	case ResultComment:
		k.Title = fmt.Sprintf("%sKommentar? IDEA-%d", ResultTitlePrefix, r.IdeaID)
	case ResultRequirement:
		k.Title = fmt.Sprintf("%sAnforderung? IDEA-%d: %s", ResultTitlePrefix, r.IdeaID, r.Title)
	}
	if k.Body == "" {
		k.Body = r.Title
	}
	k.Body += "\n\nBelege:\n> " + strings.Join(r.Evidence, "\n> ")
	if r.Session != "" {
		k.Body += "\n\n" + sessionLine(r.Session)
	}
	k.Body += fmt.Sprintf("\n\nVorschlag des Workflows %s, Lauf %d.", w.run.Workflow, w.run.ID)
	return k
}

// apply makes a result effective: the knowledge entry, the issue, the
// criterion or the relation. noteID is the accepted note, 0 for an automatic
// approval.
func (w writer) apply(r Result, noteID int64) (string, error) {
	switch r.Kind {
	case ResultKnowledge:
		if noteID > 0 {
			return knref.Format(noteID), nil
		}
		t := r.Type
		if t == "" {
			t = "note"
		}
		id, err := w.st.InsertKnowledge(store.Knowledge{Type: t, Title: r.Title, Body: r.Body, Scope: scope.Axes{Project: w.run.Project}, Status: "active",
			Origin: "distilled", Person: w.person, Harness: Harness, SessionRef: w.ref(), AuthorID: w.account})
		return knref.Format(id), err
	case ResultIssue:
		desc := r.Body
		if r.Session != "" {
			desc += "\n\n> " + strings.Join(r.Evidence, "\n> ") + "\n\n" + sessionLine(r.Session)
		}
		d, err := w.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: r.Title, Description: strings.TrimSpace(desc),
			Scope: scope.Axes{Project: w.run.Project}, Person: w.person, SessionRef: w.ref()}, AuthorID: w.account})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("REQ-%d", d.Request.ID), nil
	case ResultCriterion:
		c, err := w.st.AddCriterion(r.RequestID, r.Title, w.actor())
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("REQ-%d#%d", r.RequestID, c.ID), nil
	case ResultRelation:
		if r.IdeaID > 0 {
			if _, err := w.access().LinkFeature(r.FeatureID, "idea", r.IdeaID, r.Relation, w.actor()); err != nil {
				return "", err
			}
			return fmt.Sprintf("FEAT-%d", r.FeatureID), nil
		}
		rel, err := w.st.AddRequestRelation(r.RequestID, requestdomain.Relation{Kind: r.Relation, OtherRequestID: r.OtherID}, w.actor())
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("relation:%d", rel.ID), nil
	case ResultComment:
		if _, err := w.access().AddComment(store.CommentOnIdea, r.IdeaID, r.Body, store.CommentActor{Person: w.person}); err != nil {
			return "", err
		}
		return fmt.Sprintf("IDEA-%d", r.IdeaID), nil
	case ResultRequirement:
		pa := w.access()
		cur, _, err := pa.IdeaConcept(r.IdeaID, 0)
		if err != nil {
			return "", err
		}
		reqs := append(append([]string{}, cur.Concept.Requirements...), strings.TrimSpace(r.Title))
		if _, _, err := pa.PutIdeaConcept(r.IdeaID, store.PutIdeaConceptInput{Patch: idea.ConceptPatch{Requirements: &reqs}, Base: cur.Revision,
			Message: fmt.Sprintf("Anforderung aus Workflow-Lauf %d", w.run.ID),
			Actor:   idea.Actor{AccountID: w.account, Person: w.person, SessionRef: w.ref(), Agent: true}}); err != nil {
			return "", err
		}
		return fmt.Sprintf("IDEA-%d", r.IdeaID), nil
	}
	return "", errors.New("unknown kind of result")
}

// ErrNotProposal: the entry is no proposal of a workflow that waits.
var ErrNotProposal = errors.New("not a waiting proposal of a workflow")

// ErrProposalChanged: the entry of the proposal was edited after the run
// proposed it, or its target is gone; nothing is applied.
var ErrProposalChanged = errors.New("the proposal changed since the run made it")

// AcceptProposal makes the proposal a staged entry stands for effective, after
// a person accepted the entry on the review page. The issue, criterion or
// relation is written on behalf of the run (its reference, its account); the
// person who accepted is recorded on the result. A proposal whose text went with
// the retention can no longer be applied.
func AcceptProposal(st *store.Store, noteID, by int64) (string, error) {
	res, err := st.WorkflowResultOfNote(noteID)
	if err != nil {
		return "", ErrNotProposal
	}
	var r Result
	if res.Payload == "" || json.Unmarshal([]byte(res.Payload), &r) != nil {
		return "", ErrNotProposal
	}
	run, _, err := st.WorkflowRunByID(res.RunID)
	if err != nil {
		return "", err
	}
	w := writer{st: st, run: run, account: st.WorkflowRunAccount(run)}
	if acc, err := st.AccountByPrincipalID(fmt.Sprintf("person:%d", w.account)); err == nil {
		w.person = acc.Name
	}
	// What the person accepted is the entry as it reads now: if it was changed
	// after the run proposed it, the stored proposal is not what they read.
	k, err := st.KnowledgeByID(noteID)
	want := w.note(r)
	if err != nil || k.Title != want.Title || strings.TrimSpace(k.Body) != strings.TrimSpace(want.Body) {
		return "", ErrProposalChanged
	}
	if validResult(st, run.Project, r) != "" {
		return "", ErrProposalChanged
	}
	ref, err := w.apply(r, noteID)
	if err != nil {
		return "", err
	}
	return ref, st.SettleWorkflowResult(res.ID, store.ResultProposed, store.ResultAccepted, ref, by)
}

// UndoAuto takes back an automatic approval: the knowledge entry is retired,
// the issue dropped, the criterion waived, the relation removed. Whoever may
// review the project's proposals may take one back (owner, lead, or a member
// with the right to review): the automatic approval stood in for exactly that
// review. The caller checks (the handler of /ui/workflows/undo).
func UndoAuto(st *store.Store, resultID, by int64, person string) error {
	res, err := st.WorkflowResultByID(resultID)
	if err != nil || res.State != store.ResultAuto {
		return store.ErrWorkflowResult
	}
	reason := fmt.Sprintf("automatic approval of workflow run %d undone", res.RunID)
	switch res.Kind {
	case ResultKnowledge:
		id, ok := knref.Parse(res.Ref)
		if !ok {
			return store.ErrWorkflowResult
		}
		err = st.UpdateKnowledgeFromWeb(id, map[string]string{"status": "deprecated"}, person)
	case ResultIssue:
		id, perr := strconv.ParseInt(strings.TrimPrefix(res.Ref, "REQ-"), 10, 64)
		if perr != nil {
			return store.ErrWorkflowResult
		}
		err = st.DropRequest(id, reason, person)
	case ResultCriterion:
		_, c, ok := strings.Cut(res.Ref, "#")
		id, perr := strconv.ParseInt(c, 10, 64)
		if !ok || perr != nil {
			return store.ErrWorkflowResult
		}
		err = st.SetCriterionState(id, "waived", requestdomain.Evidence{Kind: "session", Ref: store.WorkflowRunRefPrefix + strconv.FormatInt(res.RunID, 10), Person: person})
	case ResultRelation:
		id, perr := strconv.ParseInt(strings.TrimPrefix(res.Ref, "relation:"), 10, 64)
		if perr != nil {
			return store.ErrWorkflowResult
		}
		err = st.RemoveRequestRelation(id, person, reason)
	default:
		return store.ErrWorkflowResult
	}
	if err != nil {
		return err
	}
	return st.SettleWorkflowResult(resultID, store.ResultAuto, store.ResultUndone, "", by)
}
