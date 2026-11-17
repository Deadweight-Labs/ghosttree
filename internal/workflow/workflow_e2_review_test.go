package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/knref"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const autoKindsText = "---\non: pr.merged\ninputs: [pr.title]\nuses: ghosttree/echo\nresults:\n  knowledge: 1\n  criterion: 1\n  relation: 1\n---\n"

// leadAnna makes a second account that holds the role lead in the project, and
// returns what a person with that role needs: the account and a workflow it
// saved (its author).
func leadAnna(t *testing.T, st *store.Store) {
	t.Helper()
	if _, err := st.AddPerson("anna"); err != nil {
		t.Fatal(err)
	}
	p, _ := st.ProjectByRemote(project)
	code, _, err := st.CreateInvitation("person:1", p.OrgID, "", store.OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectRole("person:1", project, "person:2", store.RoleLead, false, store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
}

func twoRequests(t *testing.T, st *store.Store) {
	t.Helper()
	for _, title := range []string{"Erste", "Zweite"} {
		if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: title, Scope: scope.Axes{Project: project}, Person: "robin"}, AuthorID: 1}); err != nil {
			t.Fatal(err)
		}
	}
}

func saveAs(t *testing.T, st *store.Store, actor, name, text string) {
	t.Helper()
	d, rep := Check(text, Env{Catalog: catalogWithResults()})
	if !rep.OK() {
		t.Fatal(rep.Errors)
	}
	if _, err := st.SaveWorkflowDef(actor, store.WorkflowDefSave{Project: project, Name: name, Text: text, Event: d.On, Uses: d.Uses}); err != nil {
		t.Fatal(err)
	}
}

func activityOf(t *testing.T, st *store.Store, id int64, kind string) []requestdomain.Activity {
	t.Helper()
	d, err := st.RequestByID(id)
	if err != nil {
		t.Fatal(err)
	}
	var out []requestdomain.Activity
	for _, a := range d.Activity {
		if a.Kind == kind {
			out = append(out, a)
		}
	}
	return out
}

// Review of #285, 3 and 10: what a run applies automatically carries the run
// in the history of the issue (a criterion and a relation have no session
// reference of their own), and each kind can be taken back: knowledge is
// retired, the criterion waived, the relation removed.
func TestAutomaticCriterionAndRelationNameTheRunAndEachKindCanBeUndone(t *testing.T) {
	st := engineStore(t)
	twoRequests(t, st)
	p, _ := st.ProjectByRemote(project)
	if err := st.SetWorkflowPolicy("person:1", p.OrgID, store.WorkflowPolicy{AutoKinds: []string{"knowledge", "criterion", "relation"}}); err != nil {
		t.Fatal(err)
	}
	e := proposeEngine(t, st, func(in Input) {
		in.Propose(Result{Kind: ResultKnowledge, Title: "Links laufen ab", Evidence: []string{"Anmeldung per Link"}})
		in.Propose(Result{Kind: ResultCriterion, Title: "Link gilt zehn Minuten", RequestID: 1, Evidence: []string{"Anmeldung per Link"}})
		in.Propose(Result{Kind: ResultRelation, RequestID: 1, OtherID: 2, Relation: "related", Evidence: []string{"Anmeldung per Link"}})
	})
	saveDefAs(t, st, "melden", autoKindsText, 0)
	mergedTitled(t, st, "7", "feat: Anmeldung per Link")
	if _, err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	run := lastRun(t, st)
	results, _ := st.WorkflowResultsOf(run.ID)
	if len(results) != 3 || results[0].State != store.ResultAuto || results[1].State != store.ResultAuto || results[2].State != store.ResultAuto {
		t.Fatalf("results: %+v", results)
	}
	ref := store.WorkflowRunRefPrefix + "1"
	if run.ID != 1 {
		t.Fatalf("run id %d", run.ID)
	}
	for _, kind := range []string{"criterion.added", "relation.added"} {
		acts := activityOf(t, st, 1, kind)
		if len(acts) != 1 || !strings.Contains(acts[0].Person, ref) || !strings.Contains(acts[0].Person, "robin") {
			t.Fatalf("%s names %+v, want the account and %s", kind, acts, ref)
		}
	}
	// Undo each: knowledge retired, criterion waived, relation removed.
	for _, r := range results {
		if err := UndoAuto(st, r.ID, 1, "robin"); err != nil {
			t.Fatalf("undo %s: %v", r.Kind, err)
		}
	}
	id, ok := knref.Parse(results[0].Ref)
	if k, err := st.KnowledgeByID(id); !ok || err != nil || k.Status != "deprecated" {
		t.Fatalf("knowledge after undo: %+v %v", k, err)
	}
	d, _ := st.RequestByID(1)
	if len(d.Criteria) != 1 || d.Criteria[0].State != "waived" || len(d.Relations) != 0 {
		t.Fatalf("issue after undo: criteria %+v relations %+v", d.Criteria, d.Relations)
	}
	for _, r := range results {
		if got, _ := st.WorkflowResultByID(r.ID); got.State != store.ResultUndone {
			t.Fatalf("%s after undo: %+v", r.Kind, got)
		}
	}
}

// Review of #285, 2: a run acts for the account that saved the version, with
// the rights that account holds now. After a demotion the workflow does not run
// (skipped with the reason), and the gate writes nothing in its name.
func TestRunOfADemotedLeadIsSkippedAndTheGateWritesNothing(t *testing.T) {
	st := engineStore(t)
	leadAnna(t, st)
	twoRequests(t, st)
	p, _ := st.ProjectByRemote(project)
	if err := st.SetWorkflowPolicy("person:1", p.OrgID, store.WorkflowPolicy{AutoKinds: []string{"knowledge", "criterion", "relation"}}); err != nil {
		t.Fatal(err)
	}
	ran := 0
	e := proposeEngine(t, st, func(in Input) {
		ran++
		in.Propose(Result{Kind: ResultCriterion, Title: "Link gilt zehn Minuten", RequestID: 1, Evidence: []string{"Anmeldung per Link"}})
	})
	saveAs(t, st, "person:2", "melden", autoKindsText)

	mergedTitled(t, st, "1", "feat: Anmeldung per Link")
	if _, err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ran != 1 {
		t.Fatalf("the lead's workflow ran %d times", ran)
	}
	if d, _ := st.RequestByID(1); len(d.Criteria) != 1 {
		t.Fatalf("a lead's automatic criterion: %+v", d.Criteria)
	}
	run := lastRun(t, st)
	if got := st.WorkflowRunActor(run); got != 2 {
		t.Fatalf("actor of the run: %d", got)
	}

	// The lead is demoted to member: the next run is skipped with the reason.
	if err := st.SetProjectRole("person:1", project, "person:2", store.RoleMember, false, store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	mergedTitled(t, st, "2", "feat: Anmeldung per Link")
	if _, err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ran != 1 {
		t.Fatal("the workflow of a demoted lead ran")
	}
	runs, _ := st.WorkflowRunsIn(project, store.WorkflowRunFilter{Workflow: "melden"})
	if len(runs) != 2 || runs[0].State != store.RunSkipped || runs[0].Reason != SkipAuthorRights {
		t.Fatalf("runs: %+v", runs)
	}
	if st.WorkflowRunActor(runs[0]) != 0 {
		t.Fatal("a demoted lead still acts for a run")
	}
	// A run that began before the demotion ends at the gate: nothing is written.
	g := Gate{Store: st, Run: run, Def: &Def{Results: []ResultLimit{{Kind: ResultCriterion, Max: 1}}}, Policy: store.WorkflowPolicy{AutoKinds: []string{"criterion"}}, Inputs: "Anmeldung per Link"}
	rep, err := g.Apply([]Result{{Kind: ResultCriterion, Title: "Noch eins", RequestID: 1, Evidence: []string{"Anmeldung per Link"}}})
	if err != nil || rep.Auto != 0 || rep.Proposed != 0 || rep.Rejected != 1 {
		t.Fatalf("gate for a demoted author: %+v %v", rep, err)
	}
	if d, _ := st.RequestByID(1); len(d.Criteria) != 1 {
		t.Fatalf("criteria after the demotion: %+v", d.Criteria)
	}

	// Removed from the project altogether, and disabled: no more than before.
	if err := st.RemoveProjectRole("person:1", project, "person:2", store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	if st.WorkflowRunActor(run) != 0 {
		t.Fatal("an account without a role acts for a run")
	}
}
