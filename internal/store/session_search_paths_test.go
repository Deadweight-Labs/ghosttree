package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// SearchTranscripts goes from the chunks of the readable sessions when they are
// few and from the hits when they are many. Both ways give every viewer the same
// page: the same sessions, counters, facets, hits and snippets, and nothing of a
// session he may not read.
func TestTranscriptSearchGivesTheSamePageEitherWay(t *testing.T) {
	f := newVisFixture(t)
	addSession(t, f.st, "f-rex-noise", 4, roleProject, "box", VisProject, transcriptWith("walrus"))
	old := smallReadableSet
	t.Cleanup(func() { smallReadableSet = old })
	viewers := []struct{ id, label string }{
		{"person:1", "robin"}, {"person:2", "lena"}, {"person:3", "mia"}, {"person:4", "rex"}, {"person:5", "gus"}, {"person:6", "nora"},
	}
	for _, enforce := range []bool{true, false} {
		f.st.SetAccessMode(AccessMode{Enforce: enforce})
		for _, v := range viewers {
			for _, q := range []SearchQuery{
				{Q: "zebra", Limit: 10}, {Q: "zebra", Sort: "newest", Limit: 10}, {Q: "zebra", Kind: "all", Limit: 1},
				{Q: "walrus", Limit: 10}, {Q: "nothing-at-all", Limit: 10},
			} {
				page := func(small int) string {
					smallReadableSet = small
					p, err := f.st.SearchTranscripts(viewer(f.st, v.id, v.label), q)
					if err != nil {
						t.Fatalf("%s %+v: %v", v.label, q, err)
					}
					b, _ := json.Marshal(p)
					return string(b)
				}
				if chunks, hits := page(1<<30), page(0); chunks != hits {
					t.Errorf("enforce=%v %s %+v:\nfrom the chunks %s\nfrom the hits   %s", enforce, v.label, q, chunks, hits)
				}
			}
		}
	}
}

// A search whose context ended stops with the context's error instead of an answer.
func TestTranscriptSearchStopsWhenItsContextEnds(t *testing.T) {
	f := newVisFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, small := range []int{0, 1 << 30} {
		old := smallReadableSet
		smallReadableSet = small
		page, err := f.st.SearchTranscripts(viewer(f.st, "person:1", "robin"), SearchQuery{Q: "zebra", Limit: 10, Ctx: ctx})
		smallReadableSet = old
		if !errors.Is(err, context.Canceled) || len(page.Groups) != 0 {
			t.Errorf("small=%d: %v, %d groups", small, err, len(page.Groups))
		}
	}
}

// A database no server has migrated yet lacks chunk_index_chunk (a store opened
// read-only on it): the search from the hits joins plainly instead of failing.
func TestTranscriptSearchWithoutTheChunkMap(t *testing.T) {
	f := newVisFixture(t)
	if _, err := f.st.db.Exec(`DROP INDEX chunk_index_chunk`); err != nil {
		t.Fatal(err)
	}
	old := smallReadableSet
	t.Cleanup(func() { smallReadableSet = old })
	pa := viewer(f.st, "person:1", "robin")
	smallReadableSet = 1 << 30
	want, err := f.st.SearchTranscripts(pa, SearchQuery{Q: "zebra", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	smallReadableSet = 0
	got, err := f.st.SearchTranscripts(pa, SearchQuery{Q: "zebra", Limit: 10})
	if err != nil {
		t.Fatalf("without the index: %v", err)
	}
	a, _ := json.Marshal(want)
	b, _ := json.Marshal(got)
	if string(a) != string(b) {
		t.Errorf("without the index:\n%s\nwant\n%s", b, a)
	}
}

// The reads of a store view end with its context: every search kind of the web
// page answers with the context's error instead of running on.
func TestStoreViewStopsItsReadsWithItsContext(t *testing.T) {
	f := newVisFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v := f.st.WithContext(ctx)
	if _, err := v.SearchKnowledgeWith("zebra", KnowledgeSearch{Limit: 5}); !errors.Is(err, context.Canceled) {
		t.Errorf("knowledge: %v", err)
	}
	if _, err := v.ObjectFacts("request", []int64{1, 2}); !errors.Is(err, context.Canceled) {
		t.Errorf("facts: %v", err)
	}
	if _, err := v.SearchTranscripts(viewer(v, "person:1", "robin"), SearchQuery{Q: "zebra", Limit: 5}); !errors.Is(err, context.Canceled) {
		t.Errorf("transcripts: %v", err)
	}
	// The store itself is untouched.
	if _, err := f.st.SearchTranscripts(viewer(f.st, "person:1", "robin"), SearchQuery{Q: "zebra", Limit: 5}); err != nil {
		t.Errorf("the store after its view ended: %v", err)
	}
}
