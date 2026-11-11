package store

import (
	"errors"
	"fmt"
	"testing"
)

// derived_from of a new request ties it to an idea or a pain. That is a write
// on the idea and the pain, so it asks what LinkIdeaRequest asks: the right to
// edit the idea and an approved phase; and the right to work on the pain.
func TestDerivedFromAnIdeaAsksWhatLinkIdeaRequestAsks(t *testing.T) {
	w := newPainWorld(t)
	own := newIdea(t, w, "mia", "von mia")
	foreign := newIdea(t, w, "lena", "von lena")
	raw := newIdea(t, w, "mia", "noch im Konzept")
	if _, err := w.st.DB().Exec(`UPDATE ideas SET phase='concept' WHERE id=?`, raw.ID); err != nil {
		t.Fatal(err)
	}
	for _, i := range []int64{own.ID, foreign.ID} {
		if _, err := w.st.DB().Exec(`UPDATE ideas SET phase='approved' WHERE id=?`, i); err != nil {
			t.Fatal(err)
		}
	}
	ref := func(id int64) []string { return []string{fmt.Sprintf("IDEA-%d", id)} }

	// Own approved idea: fine, and it is promoted.
	links, err := w.pa["mia"].ResolveRequestLinks(roleProject, "", ref(own.ID))
	if err != nil || len(links.Ideas) != 1 || !links.Ideas[0].Promote {
		t.Fatalf("own approved idea: %+v %v", links.Ideas, err)
	}
	// A lead edits any idea.
	if _, err := w.pa["lena"].ResolveRequestLinks(roleProject, "", ref(own.ID)); err != nil {
		t.Errorf("a lead may link an idea of another: %v", err)
	}
	// A member who only reads somebody else's idea may not link it.
	if _, err := w.pa["rex"].ResolveRequestLinks(roleProject, "", ref(foreign.ID)); err == nil {
		t.Error("a reader without the right to edit links the idea")
	} else if _, linkErr := w.pa["rex"].LinkIdeaRequest(foreign.ID, 0, "rex"); !errors.Is(err, ErrAccessForbidden) || !errors.Is(linkErr, ErrAccessForbidden) {
		t.Errorf("derived_from answers %v, LinkIdeaRequest %v: the kind of error differs", err, linkErr)
	}
	// An idea that is not approved yet is refused like LinkIdeaRequest does.
	if _, err := w.pa["mia"].ResolveRequestLinks(roleProject, "", ref(raw.ID)); ruleCode(err) != "not_approved" {
		t.Errorf("idea in the concept phase: %v", err)
	}
	// A guest reads, but does not write.
	if _, err := w.pa["gus"].ResolveRequestLinks(roleProject, "", ref(own.ID)); err == nil {
		t.Error("a guest links an idea")
	}
}

func TestDerivedFromAPainNeedsTheRightToWorkOnIt(t *testing.T) {
	w := newPainWorld(t)
	p := w.newPain(t, "mia")
	ref := []string{fmt.Sprintf("PAIN-%d", p.Pain.ID)}
	if links, err := w.pa["rex"].ResolveRequestLinks(roleProject, "", ref); err != nil || len(links.Pains) != 1 {
		t.Fatalf("a member links a pain: %+v %v", links.Pains, err)
	}
	if _, err := w.pa["gus"].ResolveRequestLinks(roleProject, "", ref); !errors.Is(err, ErrAccessForbidden) {
		t.Errorf("a guest links a pain: %v", err)
	}
}
