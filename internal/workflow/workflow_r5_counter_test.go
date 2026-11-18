package workflow

import (
	"context"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-713 N-A: with the automatic approval on for "relation", the link of an
// idea to a feature applies at once (it used to wait for a person although the
// policy offers the kind for exactly such workflows) and one undo takes the
// link back. A comment of the same run still waits for a person.
func TestAutomaticRelationOfAnIdeaToAFeatureAppliesAndCanBeUndone(t *testing.T) {
	st := engineStore(t)
	pa := st.Access(store.Principal{ID: "person:1", Label: "robin"})
	i, err := pa.CreateIdea(store.CreateIdeaInput{Project: project, Title: "Klartext-Status", Actor: idea.Actor{AccountID: 1, Person: "robin"}})
	if err != nil {
		t.Fatal(err)
	}
	f, err := pa.CreateFeature(store.CreateFeatureInput{Project: project, Content: feature.Patch{Name: ptr("Status")}, Actor: feature.Actor{AccountID: 1, Person: "robin"}})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := st.ProjectByRemote(project)
	if err := st.SetWorkflowPolicy("person:1", p.OrgID, store.WorkflowPolicy{AutoKinds: []string{"relation"}}); err != nil {
		t.Fatal(err)
	}
	e := proposeEngine(t, st, func(in Input) {
		in.Propose(Result{Kind: ResultRelation, IdeaID: i.ID, FeatureID: f.ID, Relation: "affects", Evidence: []string{"Anmeldung per Link"}})
		in.Propose(Result{Kind: ResultComment, IdeaID: i.ID, Body: "Passt zu Status", Evidence: []string{"Anmeldung per Link"}})
	})
	saveDefAs(t, st, "melden", "---\non: pr.merged\ninputs: [pr.title]\nuses: ghosttree/echo\nresults:\n  relation: 1\n  comment: 1\n---\n", 0)
	mergedTitled(t, st, "9", "feat: Anmeldung per Link")
	if _, err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	results, _ := st.WorkflowResultsOf(lastRun(t, st).ID)
	if len(results) != 2 || results[0].State != store.ResultAuto || results[0].Ref != f.HumanID() || results[1].State != store.ResultProposed {
		t.Fatalf("results: %+v", results)
	}
	if v, err := pa.FeatureView(f.ID); err != nil || len(v.Affects) != 1 || v.Affects[0].ID != i.ID {
		t.Fatalf("link after automatic approval: %+v %v", v.Affects, err)
	}
	if err := UndoAuto(st, results[0].ID, 1, "robin"); err != nil {
		t.Fatal(err)
	}
	if v, _ := pa.FeatureView(f.ID); len(v.Affects) != 0 {
		t.Fatalf("link survived the undo: %+v", v.Affects)
	}
	if got, _ := st.WorkflowResultByID(results[0].ID); got.State != store.ResultUndone {
		t.Fatalf("record after undo: %+v", got)
	}
}

func ptr(s string) *string { return &s }
