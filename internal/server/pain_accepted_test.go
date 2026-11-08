package server

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/pain"
)

// REQ-616 über die API: die Begründung ist Pflicht, accepted zählt als
// geschlossen (Liste, Suchsprache), die Reihenfolge ist die der Zustände.
func TestAcceptedPainOverTheAPI(t *testing.T) {
	w := newQWorld(t)
	f := w.f
	path := idPath("/api/pains/%d/state", w.pain3)

	for _, body := range []map[string]any{
		{"state": "accepted"},
		{"state": "accepted", "note": "  "},
	} {
		out := f.expect(t, "robin", 400, "POST", path, body)
		if !strings.Contains(out, "accepted_reason_required") {
			t.Errorf("accepting without a reason: %s", out)
		}
	}
	d := decode[pain.Detail](t, f.expect(t, "robin", 200, "POST", path, map[string]any{"state": "accepted", "note": "kostet mehr als es bringt"}))
	if d.Pain.State != "accepted" || d.Pain.AcceptedReason != "kostet mehr als es bringt" {
		t.Fatalf("pain = %+v", d.Pain)
	}
	// Der Rückweg verlangt eine Notiz.
	if out := f.expect(t, "robin", 400, "POST", path, map[string]any{"state": "active"}); !strings.Contains(out, "note_required") {
		t.Errorf("back to active without a note: %s", out)
	}

	// Listen: die offene Liste zeigt ihn nicht, state=accepted und all schon, in der Reihenfolge der Zustände.
	list := func(state string) []pain.Pain {
		return decode[[]pain.Pain](t, f.expect(t, "mia", 200, "GET", "/api/pains?project="+accProject+"&state="+state, nil))
	}
	for _, p := range list("") {
		if p.State == "accepted" {
			t.Errorf("the open list shows an accepted pain")
		}
	}
	if got := list("accepted"); len(got) != 1 || got[0].ID != w.pain3 || got[0].AcceptedReason == "" {
		t.Errorf("state=accepted: %+v", got)
	}
	rank := map[string]int{}
	for i, s := range pain.States {
		rank[s] = i
	}
	prev := -1
	for _, p := range list("all") {
		if rank[p.State] < prev {
			t.Errorf("all is not in the order of the states: %s after rank %d", p.State, prev)
		}
		prev = rank[p.State]
	}

	// Suchsprache: is:closed schließt accepted ein, is:open nicht.
	p1, p2, p3 := "P"+i64(w.pain1), "P"+i64(w.pain2), "P"+i64(w.pain3)
	want(t, w, "mia", "is:pain is:open", p1)
	want(t, w, "mia", "is:pain is:closed", p2, p3)
	want(t, w, "mia", "is:pain is:accepted", p3)
	want(t, w, "mia", "is:pain -is:closed", p1)
}
