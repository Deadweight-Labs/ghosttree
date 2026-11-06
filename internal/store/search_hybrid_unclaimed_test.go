package store

import (
	"slices"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Entries of a project nobody has claimed belong to their author and to the
// instance admin, and the prefilter has to let exactly those through.
func TestPrefilterKeepsTheOwnEntriesOfAnUnclaimedProject(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	_, c := newFakeEmbedder(t)
	free := scope.Axes{Project: "github.com/free/repo"}
	addK(t, st, "U-nora", "s1 s2 s3 s4", free, "trusted", "nora")
	addK(t, st, "U-mia", "s1 s2 s3 s4", free, "trusted", "mia")
	addK(t, st, "M-anywhere", "s1 s2 s3 s4", scope.Axes{Project: roleProject}, "trusted", "mia")
	embedAll(t, st, c)
	st.SetEmbedder(EmbedSearchConfig{Embedder: c})

	for who, want := range map[Principal][]string{
		{ID: "person:6", Label: "nora"}:  {"U-nora"},
		{ID: "person:3", Label: "mia"}:   {"M-anywhere", "U-mia"},
		{ID: "person:5", Label: "gus"}:   {"M-anywhere"},
		{ID: "person:1", Label: "robin"}: {"M-anywhere", "U-mia", "U-nora"},
	} {
		base := visibleSet(t, st, who, true, false)
		slices.Sort(base)
		if !slices.Equal(base, want) {
			t.Errorf("%s: %v, want %v", who.Label, base, want)
		}
		for _, combo := range [][2]bool{{true, true}, {false, true}} {
			got := visibleSet(t, st, who, combo[0], combo[1])
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Errorf("%s with keep=%v prefilter=%v: %v, want %v", who.Label, combo[0], combo[1], got, want)
			}
		}
	}
}
