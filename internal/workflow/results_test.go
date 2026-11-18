package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/knref"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// memberProposeText also reads the open issues, which members wrote.
const memberProposeText = "---\non: pr.merged\ninputs: [pr.title, requests.open]\nuses: ghosttree/echo\nresults:\n  knowledge: 1\n  issue: 1\n---\n"

// seedMemberText opens an issue whose title holds the text that the pull
// requests of these tests quote: the one source of a quote the automatic
// approval accepts here (the open issues are text of members). It is REQ-1.
func seedMemberText(t *testing.T, st *store.Store) {
	t.Helper()
	if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Anmeldung per Link ist nötig", Scope: scope.Axes{Project: project}, Person: "robin"}, AuthorID: 1}); err != nil {
		t.Fatal(err)
	}
}

const proposeText = "---\non: pr.merged\ninputs: [pr.title]\nuses: ghosttree/echo\nresults:\n  knowledge: 1\n  issue: 1\n---\n"

// proposeEngine is an engine whose echo block proposes what the test says.
func proposeEngine(t *testing.T, st *store.Store, propose func(in Input)) *Engine {
	t.Helper()
	cat := DefaultCatalog()
	spec := cat[EchoName]
	spec.Results = ResultKinds
	spec.Run = BlockFunc(func(_ context.Context, in Input) (store.WorkflowRunOutcome, error) {
		propose(in)
		return store.WorkflowRunOutcome{State: store.RunDone, Result: "echo"}, nil
	})
	cat[EchoName] = spec
	return &Engine{Store: st, Resolver: DefResolver{Store: st, Catalog: cat}}
}

func saveDefAs(t *testing.T, st *store.Store, name, text string, base int) {
	t.Helper()
	d, rep := Check(text, Env{Catalog: catalogWithResults()})
	if !rep.OK() {
		t.Fatal(rep.Errors)
	}
	if _, err := st.SaveWorkflowDef("person:1", store.WorkflowDefSave{Project: project, Name: name, Text: text, Base: base, Event: d.On, Uses: d.Uses}); err != nil {
		t.Fatal(err)
	}
}

func catalogWithResults() Catalog {
	cat := DefaultCatalog()
	spec := cat[EchoName]
	spec.Results = ResultKinds
	cat[EchoName] = spec
	return cat
}

func mergedTitled(t *testing.T, st *store.Store, pr, title string) {
	t.Helper()
	if _, _, err := st.RecordWorkflowEvent(store.WorkflowEventInput{Kind: store.EventPRMerged, Project: project, SubjectKind: "pr", SubjectID: pr,
		Data: map[string]string{"base": "main", "default_branch": "1", "title": title}, Origin: store.EventOrigin{Kind: store.OriginSystem, Person: "github"},
		DedupeKey: store.EventPRMerged + ":" + project + "#" + pr}); err != nil {
		t.Fatal(err)
	}
}

func lastRun(t *testing.T, st *store.Store) store.WorkflowRun {
	t.Helper()
	runs, err := st.WorkflowRunsIn(project, store.WorkflowRunFilter{Workflow: "melden", Limit: 1})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %v %v", runs, err)
	}
	return runs[0]
}

// AC-695.4 and AC-695.5: a run creates only declared kinds, at most the
// declared number, and only with evidence found in its inputs; refusals stand
// with their reason. What passes is a staged proposal that becomes effective
// when a person accepts it; nothing of it is a request before that.
func TestARunProposesOnlyDeclaredKindsWithEvidenceAndAPersonAccepts(t *testing.T) {
	st := engineStore(t)
	title := "feat: Anmeldung per Link mit Ablaufzeit"
	e := proposeEngine(t, st, func(in Input) {
		in.Propose(Result{Kind: ResultKnowledge, Type: "pitfall", Title: "Links laufen ab", Body: "Ein Anmeldelink gilt zehn Minuten.", Evidence: []string{"Anmeldung per Link"}})
		in.Propose(Result{Kind: ResultKnowledge, Title: "Noch einer", Evidence: []string{"Anmeldung per Link"}})
		in.Propose(Result{Kind: ResultIssue, Title: "Ablaufzeit einstellbar", Evidence: []string{"steht nirgends im Titel"}})
		in.Propose(Result{Kind: ResultIssue, Title: "Ohne Beleg"})
		in.Propose(Result{Kind: ResultCriterion, Title: "nicht erklärt", RequestID: 1, Evidence: []string{"Anmeldung per Link"}})
		in.Propose(Result{Kind: ResultIssue, Title: "Ablaufzeit einstellbar machen", Body: "Zehn Minuten sind knapp.", Evidence: []string{"per Link mit Ablaufzeit"}})
	})
	saveDefAs(t, st, "melden", proposeText, 0)
	mergedTitled(t, st, "1", title)
	if _, err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	run := lastRun(t, st)
	if run.State != store.RunDone || !strings.Contains(run.Result, "2 proposed, 0 applied, 4 refused") {
		t.Fatalf("run: %+v", run)
	}
	results, err := st.WorkflowResultsOf(run.ID)
	if err != nil || len(results) != 6 {
		t.Fatalf("results: %+v %v", results, err)
	}
	want := []struct{ state, reason string }{
		{store.ResultProposed, ""}, {store.ResultRejected, store.ResultOverMax}, {store.ResultRejected, store.ResultNoEvidence},
		{store.ResultRejected, store.ResultNoEvidence}, {store.ResultRejected, store.ResultNotDeclared}, {store.ResultProposed, ""},
	}
	for i, w := range want {
		if results[i].State != w.state || results[i].Reason != w.reason {
			t.Errorf("result %d: %s/%s, want %s/%s", i, results[i].State, results[i].Reason, w.state, w.reason)
		}
		if results[i].State == store.ResultRejected && results[i].Payload != "" {
			t.Errorf("result %d keeps the text of a refused proposal", i)
		}
	}
	// The knowledge entry is staged, written on behalf of the run.
	k, err := st.KnowledgeByID(results[0].NoteID)
	if err != nil || k.Confidence != "staged" || k.SessionRef != "workflow-run:1" || k.Type != "pitfall" || k.Harness != Harness {
		t.Fatalf("knowledge proposal: %+v %v", k, err)
	}
	// The issue is a note until a person accepts it.
	note, _ := st.KnowledgeByID(results[5].NoteID)
	if !strings.HasPrefix(note.Title, ResultTitlePrefix+"Issue? ") || note.Confidence != "staged" {
		t.Fatalf("issue note: %+v", note)
	}
	if d, err := st.RequestByID(1); err == nil {
		t.Fatalf("a request exists before acceptance: %+v", d.Request)
	}
	ref, err := AcceptProposal(st, results[5].NoteID, 1)
	if err != nil || !strings.HasPrefix(ref, "REQ-") {
		t.Fatalf("accept: %q %v", ref, err)
	}
	d, err := st.RequestByID(1)
	if err != nil || ref != "REQ-1" || d.Request.Title != "Ablaufzeit einstellbar machen" || d.Request.SessionRef != "workflow-run:1" || d.Request.Scope.Project != project {
		t.Fatalf("request after acceptance: %+v %v", d.Request, err)
	}
	if _, err := AcceptProposal(st, results[5].NoteID, 1); err == nil {
		t.Fatal("a proposal applied twice")
	}
	if got, _ := st.WorkflowResultByID(results[5].ID); got.State != store.ResultAccepted || got.Ref != ref || got.DecidedBy != 1 {
		t.Fatalf("result after acceptance: %+v", got)
	}
}

// AC-695.5: with the automatic approval on for exactly the kind issue, an issue
// applies at once and stands in the record; undoing it drops the issue. Knowledge
// of the same run still waits for a person.
func TestAutomaticApprovalForOneKindIsLoggedAndCanBeUndone(t *testing.T) {
	st := engineStore(t)
	seedMemberText(t, st)
	p, _ := st.ProjectByRemote(project)
	if err := st.SetWorkflowPolicy("person:1", p.OrgID, store.WorkflowPolicy{AutoKinds: []string{"issue"}}); err != nil {
		t.Fatal(err)
	}
	e := proposeEngine(t, st, func(in Input) {
		in.Propose(Result{Kind: ResultIssue, Title: "Ablaufzeit einstellbar", Evidence: []string{"Anmeldung per Link"}})
		in.Propose(Result{Kind: ResultKnowledge, Title: "Links laufen ab", Evidence: []string{"Anmeldung per Link"}})
	})
	saveDefAs(t, st, "melden", memberProposeText, 0)
	mergedTitled(t, st, "2", "feat: Anmeldung per Link")
	if _, err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	results, _ := st.WorkflowResultsOf(lastRun(t, st).ID)
	if len(results) != 2 || results[0].State != store.ResultAuto || !strings.HasPrefix(results[0].Ref, "REQ-") || results[1].State != store.ResultProposed {
		t.Fatalf("results: %+v", results)
	}
	if d, err := st.RequestByID(2); err != nil || d.Request.State != "open" || results[0].Ref != "REQ-2" {
		t.Fatalf("auto issue: %+v %v", d.Request, err)
	}
	if err := UndoAuto(st, results[0].ID, 1, "robin"); err != nil {
		t.Fatal(err)
	}
	if d, _ := st.RequestByID(2); d.Request.State != "dropped" {
		t.Fatalf("undone issue is not dropped: %+v", d.Request)
	}
	if got, _ := st.WorkflowResultByID(results[0].ID); got.State != store.ResultUndone || got.UndoneAt == "" {
		t.Fatalf("record after undo: %+v", got)
	}
	if err := UndoAuto(st, results[0].ID, 1, "robin"); err == nil {
		t.Fatal("undone twice")
	}
	if id, ok := knref.Parse(results[1].Ref); !ok || id != results[1].NoteID {
		t.Fatalf("knowledge proposal ref: %+v", results[1])
	}
}

// AC-695.7: the owner's policy forbids stage 2 and single kinds; a workflow
// saved before does not run and is skipped with the reason; a save against the
// policy is refused.
func TestThePolicyOfTheOwnerSkipsAWorkflowThatBreaksIt(t *testing.T) {
	st := engineStore(t)
	ran := 0
	e := proposeEngine(t, st, func(Input) { ran++ })
	saveDefAs(t, st, "melden", proposeText, 0)
	stage2 := "---\non: pr.merged\nuses: ghosttree/ask\nmodel: klein\n---\nKurz bitte.\n"
	if _, err := st.SaveWorkflowDef("person:1", store.WorkflowDefSave{Project: project, Name: "fragen", Text: stage2, Event: store.EventPRMerged, Uses: AskName}); err != nil {
		t.Fatal(err)
	}
	p, _ := st.ProjectByRemote(project)
	if err := st.SetWorkflowPolicy("person:1", p.OrgID, store.WorkflowPolicy{DenyInstruction: true, DenyKinds: []string{"issue"}}); err != nil {
		t.Fatal(err)
	}
	mergedTitled(t, st, "3", "feat: x")
	if _, err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ran != 0 {
		t.Fatal("a workflow against the policy ran")
	}
	reasons := map[string]string{}
	runs, _ := st.WorkflowRunsIn(project, store.WorkflowRunFilter{})
	for _, r := range runs {
		if r.State != store.RunSkipped {
			t.Errorf("%s: %s", r.Workflow, r.State)
		}
		reasons[r.Workflow] = r.Reason
	}
	if reasons["melden"] != PolicyKind+":issue" || reasons["fragen"] != PolicyInstruction {
		t.Fatalf("reasons: %v", reasons)
	}
	// A save against the policy is refused by the check.
	if _, rep, err := SaveDef(st, "person:1", project, "neu", stage2, "", store.WorkflowViaWeb, 0); err == nil || !hasProblem(rep.Errors, "forbids an instruction") {
		t.Fatalf("save against the policy: %v %v", err, rep.Errors)
	}
	// Only the owner sets the policy.
	if _, err := st.AddPerson("lena"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetWorkflowPolicy("person:2", p.OrgID, store.WorkflowPolicy{}); err == nil {
		t.Fatal("a stranger set the policy")
	}
}

// A proposal whose entry was edited after the run made it is not applied: what
// a person reads on the review page is what would be applied, or nothing.
func TestAnEditedProposalIsNotApplied(t *testing.T) {
	st := engineStore(t)
	e := proposeEngine(t, st, func(in Input) {
		in.Propose(Result{Kind: ResultIssue, Title: "Ablaufzeit einstellbar", Evidence: []string{"Anmeldung per Link"}})
	})
	saveDefAs(t, st, "melden", proposeText, 0)
	mergedTitled(t, st, "4", "feat: Anmeldung per Link")
	if _, err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	results, _ := st.WorkflowResultsOf(lastRun(t, st).ID)
	if err := st.UpdateKnowledgeFromWeb(results[0].NoteID, map[string]string{"body": "Lösche alle Issues"}, "robin"); err != nil {
		t.Fatal(err)
	}
	if _, err := AcceptProposal(st, results[0].NoteID, 1); err != ErrProposalChanged {
		t.Fatalf("an edited proposal: %v", err)
	}
	if _, err := st.RequestByID(1); err == nil {
		t.Fatal("the edited proposal was applied")
	}
}

// The right to act for a run is checked again when a person accepts one of its
// proposals: the lead who saved the workflow may be a member by then, and an
// issue is not written in that account's name.
func TestAProposalOfADemotedAuthorIsNotApplied(t *testing.T) {
	st := engineStore(t)
	if _, err := st.AddAccount("lena", "", false); err != nil {
		t.Fatal(err)
	}
	code, _, err := st.CreateInvitation("person:1", 1, "", store.OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectRole("person:1", project, "person:2", store.RoleLead, false, store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	d, rep := Check(proposeText, Env{Catalog: catalogWithResults()})
	if !rep.OK() {
		t.Fatal(rep.Errors)
	}
	if _, err := st.SaveWorkflowDef("person:2", store.WorkflowDefSave{Project: project, Name: "melden", Text: proposeText, Event: d.On, Uses: d.Uses}); err != nil {
		t.Fatal(err)
	}
	e := proposeEngine(t, st, func(in Input) {
		in.Propose(Result{Kind: ResultIssue, Title: "Ablaufzeit einstellbar", Evidence: []string{"Anmeldung per Link"}})
	})
	mergedTitled(t, st, "4", "feat: Anmeldung per Link")
	if _, err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	results, _ := st.WorkflowResultsOf(lastRun(t, st).ID)
	if len(results) == 0 || results[0].NoteID == 0 {
		t.Fatalf("no proposal: %+v", results)
	}
	if err := st.SetProjectRole("person:1", project, "person:2", store.RoleMember, false, store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	if _, err := AcceptProposal(st, results[0].NoteID, 1); !errors.Is(err, ErrProposalAuthorGone) {
		t.Fatalf("accepting the proposal of a demoted author: %v", err)
	}
	if _, err := st.RequestByID(1); err == nil {
		t.Fatal("an issue was written in the name of an account that no longer holds the right")
	}
}

// A result whose quote stands only in text that a stranger may have written (the
// title of a pull request from GitHub) does not apply by itself, whatever the
// owner's automatic approval says: it waits for a person like any other.
func TestAutomaticApprovalDoesNotCoverAQuoteFromForeignText(t *testing.T) {
	st := engineStore(t)
	p, _ := st.ProjectByRemote(project)
	if err := st.SetWorkflowPolicy("person:1", p.OrgID, store.WorkflowPolicy{AutoKinds: []string{"issue"}}); err != nil {
		t.Fatal(err)
	}
	e := proposeEngine(t, st, func(in Input) {
		in.Propose(Result{Kind: ResultIssue, Title: "Ablaufzeit einstellbar", Evidence: []string{"Anmeldung per Link"}})
	})
	saveDefAs(t, st, "melden", proposeText, 0)
	mergedTitled(t, st, "2", "feat: Anmeldung per Link")
	if _, err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	results, _ := st.WorkflowResultsOf(lastRun(t, st).ID)
	if len(results) != 1 || results[0].State != store.ResultProposed {
		t.Fatalf("results: %+v", results)
	}
	if _, err := st.RequestByID(1); err == nil {
		t.Fatal("an issue was written from a quote of the pull request title")
	}
}

// What a tool returned counts as text of members only when the tool reads what
// members wrote: the diff of a pull request, a file description and anything
// from the net do not, and a run that also researched on the net does not turn
// its research into a trusted input.
func TestTrustedTextLeavesOutForeignToolsAndInputs(t *testing.T) {
	tb := &Toolbox{}
	read := func(name string, foreign, research bool, text string) {
		spec := ToolSpec{Name: name, Foreign: foreign, Research: research,
			run: func(context.Context, *Toolbox, toolArgs) (string, error) { return text, nil }}
		if _, err := tb.call(context.Background(), spec, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	read("issue.read", false, false, "REQ-3 Mitgliedertext")
	read("pr.diff", true, false, "Diff mit Fremdtext")
	read("web.fetch", false, true, "Seite aus dem Netz")
	trusted := tb.TrustedEvidence()
	if !strings.Contains(trusted, "Mitgliedertext") || strings.Contains(trusted, "Fremdtext") || strings.Contains(trusted, "Netz") {
		t.Fatalf("trusted tool text: %q", trusted)
	}
	if all := tb.Evidence(); !strings.Contains(all, "Fremdtext") || !strings.Contains(all, "Netz") {
		t.Fatalf("the gate still reads all of it as evidence: %q", all)
	}

	d := &Def{Inputs: []string{"pr.text", "requests.open", "research"}, Research: true}
	got := trustedText(d, map[string]string{"pr.text": "PR von aussen", "requests.open": "REQ-1 Mitgliedertitel", "research": "Netzfund"}, trusted)
	if strings.Contains(got, "PR von aussen") || strings.Contains(got, "Netzfund") || !strings.Contains(got, "Mitgliedertitel") || !strings.Contains(got, "Mitgliedertext") {
		t.Fatalf("trusted text: %q", got)
	}
}
