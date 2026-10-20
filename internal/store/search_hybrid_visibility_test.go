package store

import (
	"fmt"
	"slices"
	"testing"
)

// visibleSet is every entry the embedding ranking offers one reader, in the
// ranking's own order: no fusion, no cap, so that what a reader may see can be
// compared whole.
func visibleSet(t *testing.T, st *Store, who Principal, useKeep, usePrefilter bool) []string {
	t.Helper()
	pa := st.Access(who)
	o := KnowledgeSearch{Limit: 500, Viewer: pa.Viewer(), Mode: SearchVectorOnly}
	if useKeep {
		o.Keep = keepRule(pa)
	}
	if usePrefilter {
		o.Prefilter = pa.KnowledgePrefilter()
	}
	got, err := st.SearchKnowledgeWith("q1 q2 q3 q4", o)
	if err != nil {
		t.Fatal(err)
	}
	var kept []Knowledge
	for _, k := range got { // what the handler does afterwards
		if keepRule(pa)(k) {
			kept = append(kept, k)
		}
	}
	return searchTitles(kept)
}

func TestHybridSearchForStrangersGuestsMembersAndOwner(t *testing.T) {
	st, _ := hiddenWorld(t, 5)
	for _, tc := range []struct {
		name string
		who  Principal
		want func(titles []string) error
	}{
		{"stranger", Principal{ID: "person:6", Label: "nora"}, func(ts []string) error {
			if len(ts) != 0 {
				return fmt.Errorf("a stranger got %v", ts)
			}
			return nil
		}},
		{"guest sees trusted entries only", Principal{ID: "person:5", Label: "gus"}, func(ts []string) error {
			if slices.Contains(ts, "H0") || slices.Contains(ts, "X0") || slices.Contains(ts, "G") || !slices.Contains(ts, "V1") {
				return fmt.Errorf("guest got %v", ts)
			}
			return nil
		}},
		{"member sees staged entries too", Principal{ID: "person:3", Label: "mia"}, func(ts []string) error {
			if !slices.Contains(ts, "H0") || !slices.Contains(ts, "G") || slices.Contains(ts, "X0") {
				return fmt.Errorf("member got %v", ts)
			}
			return nil
		}},
		{"owner of the other project", Principal{ID: "person:1", Label: "robin"}, func(ts []string) error {
			if !slices.Contains(ts, "X0") || slices.Contains(ts, "G") {
				return fmt.Errorf("owner got %v (G is mia's global entry)", ts)
			}
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The row rule alone, the prefilter and the row rule, and the prefilter
			// with the handler's row rule applied afterwards give one answer: the
			// prefilter is never narrower than the rule.
			base := visibleSet(t, st, tc.who, true, false)
			if err := tc.want(base); err != nil {
				t.Fatal(err)
			}
			for _, combo := range [][2]bool{{true, true}, {false, true}, {false, false}} {
				got := visibleSet(t, st, tc.who, combo[0], combo[1])
				if !slices.Equal(got, base) {
					t.Fatalf("keep=%v prefilter=%v: %v, want %v", combo[0], combo[1], got, base)
				}
			}
		})
	}
}
