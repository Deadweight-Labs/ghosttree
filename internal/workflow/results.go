package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

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
}

// GateReport counts what the gate did.
type GateReport struct {
	Proposed, Auto, Rejected int
}

// String is the short, lasting summary for the run's result (no model text).
func (g GateReport) String() string {
	return fmt.Sprintf("%d proposed, %d applied, %d refused", g.Proposed, g.Auto, g.Rejected)
}

// Apply checks and writes the results. A result that is refused is recorded
// with its reason, so that the run page says why.
func (g Gate) Apply(results []Result) (GateReport, error) {
	var rep GateReport
	account := g.Store.WorkflowRunAccount(g.Run)
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
		case account == 0 || person == "":
			reason = store.ResultInvalid
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
		if g.Policy.Auto(r.Kind) {
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
	if strings.TrimSpace(r.Title) == "" && r.Kind != ResultRelation {
		return store.ResultInvalid
	}
	inProject := func(id int64) bool {
		d, err := st.RequestByID(id)
		return err == nil && d.Request.Scope.Project == project && d.Request.State == "open"
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
		if !inProject(r.RequestID) || !inProject(r.OtherID) || r.RequestID == r.OtherID || !slices.Contains([]string{"related", "blocks", "duplicates", "supersedes", "derived"}, r.Relation) {
			return store.ResultBadTarget
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
	}
	if k.Body == "" {
		k.Body = r.Title
	}
	k.Body += "\n\nBelege:\n> " + strings.Join(r.Evidence, "\n> ") + fmt.Sprintf("\n\nVorschlag des Workflows %s, Lauf %d.", w.run.Workflow, w.run.ID)
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
		d, err := w.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: r.Title, Description: r.Body,
			Scope: scope.Axes{Project: w.run.Project}, Person: w.person, SessionRef: w.ref()}, AuthorID: w.account})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("REQ-%d", d.Request.ID), nil
	case ResultCriterion:
		c, err := w.st.AddCriterion(r.RequestID, r.Title, w.person)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("REQ-%d#%d", r.RequestID, c.ID), nil
	case ResultRelation:
		rel, err := w.st.AddRequestRelation(r.RequestID, requestdomain.Relation{Kind: r.Relation, OtherRequestID: r.OtherID}, w.person)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("relation:%d", rel.ID), nil
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
// the issue dropped, the criterion waived, the relation removed. Only the owner
// of the namespace may (the caller checks).
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
