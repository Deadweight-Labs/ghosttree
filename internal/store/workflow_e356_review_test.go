package store

import (
	"strconv"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Findings of the review of E3, E5 and E6 (KN-4825).

// M1, side finding: knowledge the distiller drew from a private session and
// nobody confirmed is held from the members like such a request, until its
// owner shares the session.
func TestHeldFromMembersCountsKnowledgeDistilledFromAPrivateSession(t *testing.T) {
	w := newPainWorld(t)
	sid, err := w.st.UpsertSession(Session{Harness: "claude-code", ExternalID: "privat", AccountID: 3, Scope: scope.Axes{Project: roleProject}})
	if err != nil {
		t.Fatal(err)
	}
	ref := "session:" + strconv.FormatInt(sid, 10) + "#4"
	distilled, err := w.st.InsertKnowledge(Knowledge{Type: "note", Title: "aus der privaten Session", Body: "b", Scope: scope.Axes{Project: roleProject},
		Origin: "distilled", Confidence: "quarantined", SessionRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := w.st.InsertKnowledge(Knowledge{Type: "note", Title: "abgelegt", Body: "b", Scope: scope.Axes{Project: roleProject}, Person: "mia", AuthorID: 3})
	if err != nil {
		t.Fatal(err)
	}
	if k, _ := w.st.KnowledgeByID(distilled); w.pa["gus"].CanSeeKnowledge(k) {
		t.Fatal("precondition: another member reads the distilled entry")
	}
	if !w.st.HeldFromMembers(HeldKnowledge, distilled) {
		t.Fatal("knowledge distilled from a private session does not count as held")
	}
	if w.st.HeldFromMembers(HeldKnowledge, plain) {
		t.Fatal("an entry a member wrote counts as held")
	}
	if err := w.st.SetSessionVisibility(sid, w.pa["mia"], VisProject); err != nil {
		t.Fatal(err)
	}
	if w.st.HeldFromMembers(HeldKnowledge, distilled) {
		t.Fatal("still held after its session was shared")
	}
}

// m5: closing a discussion again starts workflows only when the idea changed
// since the last closing that did; reopening and closing it in a loop is
// recorded, but is one event.
func TestClosingADiscussionAgainStartsWorkflowsOnlyAfterAChange(t *testing.T) {
	w := newPainWorld(t)
	mia := w.pa["mia"]
	i := w.workshopIdea(t, "Wiederholt geschlossen")
	about := "Worum es geht."
	if _, _, err := mia.PutIdeaConcept(i.ID, PutIdeaConceptInput{Patch: idea.ConceptPatch{About: &about}, Actor: w.actor("mia")}); err != nil {
		t.Fatal(err)
	}
	if _, err := mia.StartIdeaDiscussion(i.ID); err != nil {
		t.Fatal(err)
	}
	events := func() int {
		var n int
		w.st.db.QueryRow(`SELECT COUNT(*) FROM workflow_events WHERE kind=? AND subject_kind='idea' AND subject_id=?`, EventIdeaDiscussionClosed, strconv.FormatInt(i.ID, 10)).Scan(&n)
		return n
	}
	closeAndReopen := func() {
		t.Helper()
		if _, err := mia.SetIdeaPhaseSaid(i.ID, idea.PhaseDiscussed, w.agent("mia"), 0, "ja, mach zu"); err != nil {
			t.Fatal(err)
		}
		if _, err := mia.SetIdeaPhase(i.ID, idea.PhaseDiscussion, w.actor("mia"), 0); err != nil {
			t.Fatal(err)
		}
	}
	for range 5 {
		closeAndReopen()
	}
	var closures int
	w.st.db.QueryRow(`SELECT COUNT(*) FROM idea_discussion_closures WHERE idea_id=?`, i.ID).Scan(&closures)
	if n := events(); n != 1 || closures != 5 {
		t.Fatalf("five closings without a change: %d events, %d closings", n, closures)
	}
	if _, err := mia.AddComment("idea", i.ID, "Noch ein Punkt zur Diskussion.", CommentActor{AccountID: 3, Person: "mia"}); err != nil {
		t.Fatal(err)
	}
	closeAndReopen()
	if n := events(); n != 2 {
		t.Fatalf("a closing after a comment: %d events", n)
	}
	more := "Worum es jetzt geht."
	if _, _, err := mia.PutIdeaConcept(i.ID, PutIdeaConceptInput{Patch: idea.ConceptPatch{About: &more}, Actor: w.actor("mia")}); err != nil {
		t.Fatal(err)
	}
	closeAndReopen()
	closeAndReopen()
	if n := events(); n != 3 {
		t.Fatalf("after a new version of the concept: %d events", n)
	}
}
