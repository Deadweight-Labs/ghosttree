package store

import (
	"errors"
	"testing"
)

const defText1 = "---\non: idea.created\nuses: ghosttree/echo\n---\n"
const defText2 = "---\non: pr.merged\nbranches: [release/*]\nuses: ghosttree/echo\n---\n"

func defSave(name, text string, base int) WorkflowDefSave {
	return WorkflowDefSave{Project: roleProject, Name: name, Text: text, Base: base, Event: EventIdeaCreated, Uses: "ghosttree/echo", Via: WorkflowViaWeb}
}

// AC-695.1 and AC-695.2: a project that never had a workflow file gets one on
// the server; every saved change is a version with its author and time; only a
// lead or owner saves, a member reads, a guest does not even read.
func TestWorkflowsLiveOnTheServerWithVersionsAndOnlyLeadsChangeThem(t *testing.T) {
	w := newPainWorld(t)
	st := w.st
	if defs, err := w.pa["mia"].WorkflowDefs(roleProject); err != nil || len(defs) != 0 {
		t.Fatalf("fresh project: %v %v", defs, err)
	}
	if _, err := st.SaveWorkflowDef(principalOfID(3), defSave("wissen", defText1, 0)); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("member creates a workflow: %v", err)
	}
	first, err := st.SaveWorkflowDef(principalOfID(2), defSave("wissen", defText1, 0))
	if err != nil || !first.Created || first.Def.Version != 1 || first.Def.Author != 2 || first.Def.UpdatedAt == "" {
		t.Fatalf("lead creates: %+v %v", first, err)
	}
	if _, err := st.SaveWorkflowDef(principalOfID(2), defSave("wissen", defText1, 0)); !errors.Is(err, ErrWorkflowDefExists) {
		t.Fatalf("second create of the same name: %v", err)
	}
	same, err := st.SaveWorkflowDef(principalOfID(1), defSave("wissen", defText1, 1))
	if err != nil || same.Changed || same.Def.Version != 1 {
		t.Fatalf("unchanged text makes a version: %+v %v", same, err)
	}
	in := defSave("wissen", defText2, 1)
	in.Event, in.Branches = EventPRMerged, []string{"release/*"}
	second, err := st.SaveWorkflowDef(principalOfID(1), in)
	if err != nil || !second.Changed || second.Def.Version != 2 || second.Def.Author != 1 || second.Def.Event != EventPRMerged {
		t.Fatalf("owner changes: %+v %v", second, err)
	}
	if _, err := st.SaveWorkflowDef(principalOfID(2), defSave("wissen", defText1, 1)); !errors.Is(err, ErrWorkflowDefConflict) {
		t.Fatalf("save on an old version: %v", err)
	}
	versions, err := w.pa["mia"].WorkflowDefVersions(roleProject, "wissen")
	if err != nil || len(versions) != 2 || versions[0].Version != 2 || versions[1].Author != 2 || versions[0].Text != defText2 {
		t.Fatalf("member reads the versions: %+v %v", versions, err)
	}
	if !w.pa["lena"].CanEditWorkflows(roleProject) || w.pa["mia"].CanEditWorkflows(roleProject) {
		t.Fatal("edit right: lead yes, member no")
	}
	if _, err := w.pa["gus"].WorkflowDefs(roleProject); err == nil {
		t.Fatal("a guest reads the workflows")
	}
	if err := st.SetWorkflowDefEnabled(principalOfID(3), roleProject, "wissen", false); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("member switches off: %v", err)
	}
	if _, err := st.SaveWorkflowDef(principalOfID(2), defSave("Ghosttree/echo", defText1, 0)); !errors.Is(err, ErrWorkflowDefName) {
		t.Fatalf("name with a slash: %v", err)
	}
}

// The trigger follows the newest version: a change of event switches the old
// trigger off, branch patterns narrow merges, and switching the workflow off
// stops its trigger.
func TestTheTriggerFollowsTheNewestVersion(t *testing.T) {
	w := newPainWorld(t)
	st := w.st
	if _, err := st.SaveWorkflowDef(principalOfID(2), defSave("wissen", defText1, 0)); err != nil {
		t.Fatal(err)
	}
	in := defSave("wissen", defText2, 1)
	in.Event, in.Branches = EventPRMerged, []string{"release/*"}
	if _, err := st.SaveWorkflowDef(principalOfID(2), in); err != nil {
		t.Fatal(err)
	}
	triggers, err := st.WorkflowTriggersOf(roleProject)
	if err != nil || len(triggers) != 2 {
		t.Fatalf("triggers: %+v %v", triggers, err)
	}
	on := map[string]WorkflowTrigger{}
	for _, tr := range triggers {
		on[tr.EventKind] = tr
	}
	if on[EventIdeaCreated].Enabled || !on[EventPRMerged].Enabled || on[EventPRMerged].BranchGlobs != "release/*" {
		t.Fatalf("triggers after the change: %+v", on)
	}
	ev := WorkflowEvent{Kind: EventPRMerged, Project: roleProject, Data: map[string]string{"base": "release/2.0"}}
	if !triggerMatches(on[EventPRMerged], ev) {
		t.Fatal("release/2.0 does not match release/*")
	}
	ev.Data = map[string]string{"base": "main", "default_branch": "1"}
	if triggerMatches(on[EventPRMerged], ev) {
		t.Fatal("main matches release/*")
	}
	if err := st.SetWorkflowDefEnabled(principalOfID(2), roleProject, "wissen", false); err != nil {
		t.Fatal(err)
	}
	triggers, _ = st.WorkflowTriggersOf(roleProject)
	for _, tr := range triggers {
		if tr.Enabled {
			t.Fatalf("trigger still on after switching off: %+v", tr)
		}
	}
	d, err := st.WorkflowDefByName(roleProject, "wissen")
	if err != nil || d.Enabled {
		t.Fatalf("workflow still on: %+v %v", d, err)
	}
}
