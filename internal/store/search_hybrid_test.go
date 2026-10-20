package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embed/embedtest"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// The fake server maps these words to the concepts q1..q4. A document that says
// "s1 s2" and a query that says "q1 q2" share no word, only meaning.
var fakeConcepts = map[string]string{"s1": "q1", "s2": "q2", "s3": "q3", "s4": "q4",
	"wagen": "auto", "kaputt": "defekt"}

func newFakeEmbedder(t *testing.T) (*embedtest.Server, *embed.Client) {
	t.Helper()
	srv := embedtest.New(t)
	srv.Dim = 256
	srv.Concepts = fakeConcepts
	c, err := embed.New(embed.Config{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return srv, c
}

func addK(t *testing.T, st *Store, title, body string, ax scope.Axes, confidence, person string) int64 {
	t.Helper()
	id, err := st.InsertKnowledge(Knowledge{Type: "note", Title: title, Body: body, Scope: ax, Confidence: confidence, Person: person})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func modelOf(c *embed.Client) EmbedModel {
	return EmbedModel{Model: c.Model(), PrefixVersion: c.PrefixVersion()}
}

// embedAll does what the background job does, in one go: embed everything that
// is due, with a look at every entry, and store the vectors.
func embedAll(t *testing.T, st *Store, c *embed.Client) {
	t.Helper()
	for range 400 {
		due, err := st.KnowledgeEmbeddingDue(modelOf(c), 16, time.Now(), true)
		if err != nil {
			t.Fatal(err)
		}
		if len(due.Items) == 0 {
			return
		}
		texts := make([]string, len(due.Items))
		for i, d := range due.Items {
			texts[i] = d.Text
		}
		vecs, err := c.Documents(context.Background(), texts)
		if err != nil {
			t.Fatal(err)
		}
		for i, d := range due.Items {
			q, scale, err := embed.Quantize(vecs[i])
			if err != nil {
				t.Fatal(err)
			}
			if err := st.UpsertKnowledgeEmbedding(KnowledgeEmbedding{KnowledgeID: d.ID, Model: c.Model(), PrefixVersion: c.PrefixVersion(),
				TextHash: d.Hash, Dim: len(q), Scale: float64(scale), Vec: q, ReadAt: due.ReadAt}); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Fatal("embedAll did not converge")
}

func searchTitles(ks []Knowledge) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = k.Title
	}
	return out
}

func matches(ks []Knowledge) map[string]string {
	out := map[string]string{}
	for _, k := range ks {
		out[k.Title] = k.Match
	}
	return out
}

// A query that shares words with the full-text hits and meaning with the others.
func TestHybridSearchFusesFullTextAndEmbeddingRankings(t *testing.T) {
	st := openTest(t)
	srv, c := newFakeEmbedder(t)
	_ = srv
	// "car" is found by words, "wagen" only by meaning, "boat" by neither.
	addK(t, st, "car", "auto defekt ersatzteil", scope.Axes{}, "", "")
	addK(t, st, "wagen", "wagen kaputt", scope.Axes{}, "", "")
	addK(t, st, "boat", "segel mast", scope.Axes{}, "", "")
	embedAll(t, st, c)

	// Before the server is configured the answer is the full-text answer.
	plain, err := st.SearchKnowledge("auto defekt", scope.Axes{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := searchTitles(plain); !slices.Equal(got, []string{"car"}) {
		t.Fatalf("full text alone: %v", got)
	}

	st.SetEmbedder(EmbedSearchConfig{Embedder: c})
	got, err := st.SearchKnowledge("auto defekt", scope.Axes{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"car", "wagen", "boat"}; !slices.Equal(searchTitles(got), want) {
		t.Fatalf("hybrid order %v, want %v", searchTitles(got), want)
	}
	m := matches(got)
	if m["car"] != "" || m["wagen"] != MatchSemantic || m["boat"] != MatchSemantic {
		t.Fatalf("only hits the full-text index missed carry the marker: %v", m)
	}
	if hybrid, fallbacks := st.EmbedSearchStats(); hybrid != 1 || fallbacks != 0 {
		t.Fatalf("stats %d %d", hybrid, fallbacks)
	}
	// The marker travels in the JSON and is absent where it is empty.
	raw, _ := json.Marshal(got)
	if strings.Count(string(raw), `"match":"semantic"`) != 2 {
		t.Fatalf("json: %s", raw)
	}
	// The limit cuts the fused list, not a single ranking.
	if one, _ := st.SearchKnowledge("auto defekt", scope.Axes{}, 1); len(one) != 1 || one[0].Title != "car" {
		t.Fatalf("limit 1: %v", searchTitles(one))
	}
}

func TestHybridSearchRanksByReciprocalRankNotByOneList(t *testing.T) {
	st := openTest(t)
	_, c := newFakeEmbedder(t)
	// Three hits in the text ranking, A best. The embedding prefers C, so C and A
	// both rank high in one list and A also in the other.
	addK(t, st, "A", "q1 q2 f1", scope.Axes{}, "", "")
	addK(t, st, "B", "q1 f1 f2 f3 f4", scope.Axes{}, "", "")
	addK(t, st, "C", "s1 s2", scope.Axes{}, "", "")
	embedAll(t, st, c)
	st.SetEmbedder(EmbedSearchConfig{Embedder: c})
	text, err := st.SearchKnowledgeWith("q1 q2", KnowledgeSearch{Mode: SearchFullTextOnly, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	vector, err := st.SearchKnowledgeWith("q1 q2", KnowledgeSearch{Mode: SearchVectorOnly, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	hybrid, err := st.SearchKnowledgeWith("q1 q2", KnowledgeSearch{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var textIDs, vecIDs, wantIDs []int64
	for _, k := range text {
		textIDs = append(textIDs, k.ID)
	}
	for _, k := range vector {
		vecIDs = append(vecIDs, k.ID)
		if k.Match != MatchSemantic {
			t.Fatalf("vector-only hits are all semantic: %+v", k)
		}
	}
	for _, f := range embed.Fuse(textIDs, vecIDs) {
		wantIDs = append(wantIDs, f.ID)
	}
	var gotIDs []int64
	for _, k := range hybrid {
		gotIDs = append(gotIDs, k.ID)
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("hybrid %v is not the reciprocal rank fusion %v of text %v and vector %v", gotIDs, wantIDs, textIDs, vecIDs)
	}
	if len(textIDs) < 2 || len(vecIDs) < 3 {
		t.Fatalf("the fixture must give both rankings something to fuse: %v %v", textIDs, vecIDs)
	}
}

// hiddenWorld has three entries every reader may see and, on top of them in the
// embedding ranking, `hidden` entries that guests may not see (staged), plus
// entries of a project nobody but the owner may read and a global entry of mia.
// With enough hidden rows above them, fusing before filtering would turn the
// order of the visible rows around.
func hiddenWorld(t *testing.T, hidden int) (*Store, *embed.Client) {
	t.Helper()
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	if _, err := st.EnsureProject("person:1", otherProject); err != nil {
		t.Fatal(err)
	}
	_, c := newFakeEmbedder(t)
	p := scope.Axes{Project: roleProject}
	// The text ranking is V1, V2; the embedding ranking V2, V3, V1.
	addK(t, st, "V1", "q1 q2 f1 f2", p, "trusted", "mia")
	addK(t, st, "V2", "q1 s2 s3", p, "trusted", "mia")
	addK(t, st, "V3", "s1 s2", p, "trusted", "mia")
	for i := range hidden {
		addK(t, st, fmt.Sprintf("H%d", i), "s1 s2 s3 s4", p, "staged", "mia")
	}
	for i := range 3 {
		addK(t, st, fmt.Sprintf("X%d", i), "s1 s2 s3 s4", scope.Axes{Project: otherProject}, "trusted", "robin")
	}
	addK(t, st, "G", "s1 s2 s3 s4 q1", scope.Axes{}, "trusted", "mia")
	embedAll(t, st, c)
	st.SetEmbedder(EmbedSearchConfig{Embedder: c})
	return st, c
}

// keepRule is what the HTTP handlers apply on top of the query.
func keepRule(pa *ProjectAccess) func(Knowledge) bool {
	return func(k Knowledge) bool { return pa.CanSeeKnowledge(k) && pa.CanDeliverKnowledge(k) }
}

func visibleTitles(t *testing.T, st *Store, who Principal, ax scope.Axes, useKeep bool) ([]Knowledge, []string) {
	t.Helper()
	pa := st.Access(who)
	// Without a project the union would mean "global entries only"; the search
	// across projects matches the axes as given.
	o := KnowledgeSearch{Axes: ax, Union: ax.Project != "", Limit: 20, Viewer: pa.Viewer()}
	if useKeep {
		o.Keep = keepRule(pa)
	}
	got, err := st.SearchKnowledgeWith("q1 q2 q3 q4", o)
	if err != nil {
		t.Fatal(err)
	}
	if !useKeep {
		// What the handler does afterwards.
		var kept []Knowledge
		for _, k := range got {
			if keepRule(pa)(k) {
				kept = append(kept, k)
			}
		}
		got = kept
	}
	return got, searchTitles(got)
}

func TestHybridSearchShowsTheGuestNothingHiddenAndDoesNotLetHiddenRowsMoveOthers(t *testing.T) {
	gus := Principal{ID: "person:5", Label: "gus"}
	withHidden, _ := hiddenWorld(t, 40)
	without, _ := hiddenWorld(t, 0)
	all := scope.Axes{}

	_, clean := visibleTitles(t, without, gus, all, true)
	if want := []string{"V2", "V1", "V3"}; !slices.Equal(clean, want) {
		t.Fatalf("the visible rows alone rank %v, want %v", clean, want)
	}
	got, gotTitles := visibleTitles(t, withHidden, gus, all, true)
	if !slices.Equal(gotTitles, clean) {
		t.Fatalf("hidden rows changed what a guest sees: %v, without them %v", gotTitles, clean)
	}
	if m := matches(got); m["V3"] != MatchSemantic || m["V1"] != "" || m["V2"] != "" {
		t.Fatalf("markers: %v", m)
	}
	for _, title := range gotTitles {
		if strings.HasPrefix(title, "H") || strings.HasPrefix(title, "X") || title == "G" {
			t.Fatalf("a guest got %q", title)
		}
	}

	// The test is only worth something if the naive way, fusing everything and
	// filtering afterwards, would have given another order.
	_, naive := visibleTitles(t, withHidden, gus, all, false)
	if slices.Equal(naive, clean) {
		t.Fatalf("the fixture does not tell filter-before-fusion from filter-after: %v", naive)
	}

	// A project filter keeps the other project's rows out in the query itself.
	pa := withHidden.Access(gus)
	scoped, err := withHidden.SearchKnowledgeWith("q1 q2 q3 q4", KnowledgeSearch{Axes: scope.Axes{Project: roleProject}, Limit: 200, Viewer: pa.Viewer()})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range scoped {
		if k.Scope.Project != roleProject {
			t.Fatalf("project filter let %q of %q through", k.Title, k.Scope.Project)
		}
	}
	// And the global entry of another author is out of the query for an enforced
	// viewer even when nobody applies a row rule.
	for _, k := range scoped {
		if k.Title == "G" {
			t.Fatal("mia's global entry reached a guest through the query")
		}
	}
	unfiltered, err := withHidden.SearchKnowledgeWith("q1 q2 q3 q4", KnowledgeSearch{Limit: 200, Viewer: pa.Viewer(), Mode: SearchVectorOnly})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range unfiltered {
		if k.Title == "G" {
			t.Fatal("the vector part compared a global entry of another author")
		}
	}
}

func TestHybridSearchNeverReturnsInactiveEntries(t *testing.T) {
	st := openTest(t)
	_, c := newFakeEmbedder(t)
	live := addK(t, st, "live", "wagen kaputt", scope.Axes{}, "trusted", "")
	quarantined := addK(t, st, "quarantined", "wagen kaputt", scope.Axes{}, "quarantined", "")
	ids := map[string]int64{"archived": 0, "superseded": 0, "stale": 0, "deprecated": 0}
	for status := range ids {
		ids[status] = addK(t, st, status, "wagen kaputt", scope.Axes{}, "trusted", "")
	}
	embedAll(t, st, c)
	for status, id := range ids {
		if _, err := st.db.Exec(`UPDATE knowledge SET status=? WHERE id=?`, status, id); err != nil {
			t.Fatal(err)
		}
	}
	// The vectors stay, which is the point: only the entry's status keeps them out.
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM knowledge_embeddings`).Scan(&n); err != nil || n != 6 {
		t.Fatalf("vectors: %d %v", n, err)
	}
	st.SetEmbedder(EmbedSearchConfig{Embedder: c})

	agent, err := st.SearchKnowledgeWith("auto defekt", KnowledgeSearch{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got := searchTitles(agent); !slices.Equal(got, []string{"live"}) {
		t.Fatalf("agent view: %v", got)
	}
	// The operator view also lists quarantined entries, but never an inactive one
	// through the vector part.
	op, err := st.SearchKnowledgeWith("auto defekt", KnowledgeSearch{Operator: true, Limit: 20, Mode: SearchVectorOnly})
	if err != nil {
		t.Fatal(err)
	}
	got := searchTitles(op)
	slices.Sort(got)
	if !slices.Equal(got, []string{"live", "quarantined"}) {
		t.Fatalf("operator view, vector part: %v", got)
	}
	_ = live
	_ = quarantined
}

func TestHybridSearchFallsBackToFullTextWithoutAnError(t *testing.T) {
	st := openTest(t)
	srv, c := newFakeEmbedder(t)
	addK(t, st, "car", "auto defekt ersatzteil", scope.Axes{}, "", "")
	addK(t, st, "wagen", "wagen kaputt", scope.Axes{}, "", "")
	embedAll(t, st, c)
	baseline, err := st.SearchKnowledge("auto defekt", scope.Axes{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(baseline)

	var logs lineCounter
	cfg := EmbedSearchConfig{Embedder: c, Timeout: 60 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(&logs, nil))}
	for _, tc := range []struct {
		name  string
		setup func()
	}{
		{"server answers 500", func() { srv.Fail.Store(true) }},
		{"server hangs", func() { srv.Fail.Store(false); srv.Hang.Store(true) }},
		{"server is gone", func() { srv.Hang.Store(false); srv.Close() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup()
			st.SetEmbedder(cfg)
			started := time.Now()
			got, err := st.SearchKnowledge("auto defekt", scope.Axes{}, 10)
			if err != nil {
				t.Fatalf("the caller must not see the failure: %v", err)
			}
			if took := time.Since(started); took > 2*time.Second {
				t.Fatalf("the search waited %v for an embedding server that does not answer", took)
			}
			if raw, _ := json.Marshal(got); !bytes.Equal(raw, want) {
				t.Fatalf("fallback answer differs from full text:\n%s\n%s", raw, want)
			}
		})
	}
	if _, fallbacks := st.EmbedSearchStats(); fallbacks != 3 {
		t.Fatalf("fallbacks counted: %d", fallbacks)
	}
	// Three failures within a minute, one line.
	if n := logs.count("embed_search_fallback"); n != 1 {
		t.Fatalf("%d log lines for three fallbacks within a minute, want 1:\n%s", n, logs.String())
	}
	if strings.Contains(logs.String(), "auto defekt") {
		t.Fatal("the log must not hold the query")
	}
	// The strict modes, which the measurement uses, do not fall back.
	if _, err := st.SearchKnowledgeWith("auto defekt", KnowledgeSearch{Mode: SearchHybridStrict}); err == nil {
		t.Fatal("strict hybrid search must report a dead server")
	}
	if _, err := st.SearchKnowledgeWith("auto defekt", KnowledgeSearch{Mode: SearchVectorOnly}); err == nil {
		t.Fatal("vector search must report a dead server")
	}
}

type lineCounter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lineCounter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}
func (l *lineCounter) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.buf.String() }
func (l *lineCounter) count(sub string) int {
	return strings.Count(l.String(), sub)
}

func TestSearchWithoutAnEmbeddingServerIsExactlyTheOldSearch(t *testing.T) {
	st := openTest(t)
	addK(t, st, "one", "auto defekt", scope.Axes{Project: roleProject}, "", "")
	addK(t, st, "two", "wagen kaputt", scope.Axes{Project: roleProject}, "", "")
	addK(t, st, "three", "auto", scope.Axes{}, "", "")
	for _, q := range []string{"auto defekt", "wagen", "auto", "nothing", "was ist noch zu tun"} {
		for _, name := range []string{"agent", "union", "operator"} {
			var got, want []Knowledge
			var err1, err2 error
			ax := scope.Axes{Project: roleProject}
			switch name {
			case "agent":
				got, err1 = st.SearchKnowledge(q, ax, 10)
				want, err2 = st.SearchKnowledgeWith(q, KnowledgeSearch{Axes: ax, Limit: 10, Mode: SearchFullTextOnly})
			case "union":
				got, err1 = st.SearchKnowledgeForContext(q, ax, 10)
				want, err2 = st.SearchKnowledgeWith(q, KnowledgeSearch{Axes: ax, Union: true, Limit: 10, Mode: SearchFullTextOnly})
			default:
				got, err1 = st.SearchAllKnowledge(q, ax, 10)
				want, err2 = st.SearchKnowledgeWith(q, KnowledgeSearch{Axes: ax, Operator: true, Limit: 10, Mode: SearchFullTextOnly})
			}
			if err1 != nil || err2 != nil {
				t.Fatal(err1, err2)
			}
			a, _ := json.Marshal(got)
			b, _ := json.Marshal(want)
			if !bytes.Equal(a, b) || bytes.Contains(a, []byte("match")) {
				t.Fatalf("%s %q: %s vs %s", name, q, a, b)
			}
		}
	}
	if hybrid, fallbacks := st.EmbedSearchStats(); hybrid != 0 || fallbacks != 0 {
		t.Fatalf("an unconfigured store must not even try: %d %d", hybrid, fallbacks)
	}
}

func TestListingQueriesAreLeftToFullText(t *testing.T) {
	st := openTest(t)
	srv, c := newFakeEmbedder(t)
	addK(t, st, "x", "wagen", scope.Axes{}, "", "")
	embedAll(t, st, c)
	st.SetEmbedder(EmbedSearchConfig{Embedder: c})
	before := srv.Calls()
	got, err := st.SearchKnowledge("was ist noch zu tun", scope.Axes{}, 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
	if srv.Calls() != before {
		t.Fatal("a question of ordinary words must not be sent to the embedding server")
	}
}

func TestEmbeddingsFollowTextModelAndFormat(t *testing.T) {
	st := openTest(t)
	_, c := newFakeEmbedder(t)
	m := modelOf(c)
	a := addK(t, st, "a", "alpha", scope.Axes{}, "", "")
	b := addK(t, st, "b", "beta", scope.Axes{}, "", "")
	archived := addK(t, st, "archived", "gamma", scope.Axes{}, "", "")
	if _, err := st.db.Exec(`UPDATE knowledge SET status='archived' WHERE id=?`, archived); err != nil {
		t.Fatal(err)
	}
	due := func(model EmbedModel, full bool) []int64 {
		d, err := st.KnowledgeEmbeddingDue(model, 50, time.Now(), full)
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, i := range d.Items {
			ids = append(ids, i.ID)
		}
		slices.Sort(ids)
		return ids
	}
	if got := due(m, false); !slices.Equal(got, []int64{a, b}) {
		t.Fatalf("new entries are due, archived ones are not: %v", got)
	}
	status, err := st.KnowledgeEmbeddingStatus(m)
	if err != nil || status.Active != 2 || status.Current != 0 || status.Due != 2 {
		t.Fatalf("status %+v %v", status, err)
	}
	embedAll(t, st, c)
	if got := due(m, true); len(got) != 0 {
		t.Fatalf("nothing is due after embedding: %v", got)
	}
	if status, _ = st.KnowledgeEmbeddingStatus(m); status.Current != 2 || status.Due != 0 {
		t.Fatalf("status %+v", status)
	}

	// An edit through the normal path: the title or the body, the entry is due
	// again, and only it, without a look at every entry.
	if err := st.UpdateKnowledge(a, map[string]string{"body": "alpha changed"}); err != nil {
		t.Fatal(err)
	}
	if got := due(m, false); !slices.Equal(got, []int64{a}) {
		t.Fatalf("changed body: %v", got)
	}
	if status, _ = st.KnowledgeEmbeddingStatus(m); status.Current != 1 || status.Due != 1 {
		t.Fatalf("status after edit %+v", status)
	}
	embedAll(t, st, c)
	if err := st.UpdateKnowledge(b, map[string]string{"title": "b renamed"}); err != nil {
		t.Fatal(err)
	}
	if got := due(m, false); !slices.Equal(got, []int64{b}) {
		t.Fatalf("changed title: %v", got)
	}
	embedAll(t, st, c)

	// A text rewritten some other way, without updated_at, is found by the look
	// at every entry and not by the ordinary one.
	if _, err := st.db.Exec(`UPDATE knowledge SET updated_at='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE knowledge SET body='rewritten' WHERE id=?`, a); err != nil {
		t.Fatal(err)
	}
	if got := due(m, false); len(got) != 0 {
		t.Fatalf("the ordinary look does not read every text: %v", got)
	}
	if got := due(m, true); !slices.Equal(got, []int64{a}) {
		t.Fatalf("the look at every entry finds it: %v", got)
	}
	embedAll(t, st, c)

	// Another model, or another version of the text formats, makes everything due.
	other := EmbedModel{Model: "other-model", PrefixVersion: m.PrefixVersion}
	if got := due(other, false); !slices.Equal(got, []int64{a, b}) {
		t.Fatalf("other model: %v", got)
	}
	if status, _ = st.KnowledgeEmbeddingStatus(other); status.Current != 0 || status.OtherModel != 2 || status.Due != 2 {
		t.Fatalf("status for another model %+v", status)
	}
	if got := due(EmbedModel{Model: m.Model, PrefixVersion: "next"}, false); len(got) != 2 {
		t.Fatalf("other prefix version: %v", got)
	}

	// An entry that was parked is not offered, until its time is up, its text
	// changes or the model does.
	d, _ := st.KnowledgeEmbeddingDue(other, 50, time.Now(), false)
	var parked EmbedItem
	for _, it := range d.Items {
		if it.ID == a {
			parked = it
		}
	}
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if err := st.RecordEmbeddingFailure(EmbeddingFailure{KnowledgeID: a, Model: other.Model, PrefixVersion: other.PrefixVersion,
		TextHash: parked.Hash, Attempts: 1, RetryAt: until, Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	if got := due(other, false); !slices.Equal(got, []int64{b}) {
		t.Fatalf("parked entry offered: %v", got)
	}
	if status, _ = st.KnowledgeEmbeddingStatus(other); status.Parked != 1 {
		t.Fatalf("status %+v", status)
	}
	later, _ := st.KnowledgeEmbeddingDue(other, 50, time.Now().Add(2*time.Hour), false)
	if len(later.Items) != 2 {
		t.Fatalf("after the time is up it is due again: %+v", later.Items)
	}
	for _, it := range later.Items {
		if it.ID == a && it.Attempts != 1 {
			t.Fatalf("the attempts travel with the item: %+v", it)
		}
	}
	if got := due(m, false); len(got) != 0 {
		t.Fatalf("a failure of another model does not park an entry of this one: %v", got)
	}

	// Newest edits first.
	if _, err := st.db.Exec(`UPDATE knowledge SET updated_at='2099-01-01T00:00:00Z' WHERE id=?`, b); err != nil {
		t.Fatal(err)
	}
	first, _ := st.KnowledgeEmbeddingDue(other, 1, time.Now(), false)
	if len(first.Items) != 1 || first.Items[0].ID != b {
		t.Fatalf("the most recently edited entry comes first: %+v", first.Items)
	}
	// The text that is sent follows the model card and is what the hash covers.
	if want := embed.DocumentText("b renamed", "beta"); first.Items[0].Text != want || first.Items[0].Hash != embed.TextHash(want) {
		t.Fatalf("item %+v", first.Items[0])
	}
}

func TestUpsertKnowledgeEmbeddingValidates(t *testing.T) {
	st := openTest(t)
	good := KnowledgeEmbedding{KnowledgeID: 1, Model: "m", PrefixVersion: "p", TextHash: "h", Dim: 3, Scale: 0.1, Vec: []byte{1, 2, 3}, ReadAt: "2026-10-20T10:00:00Z"}
	if err := st.UpsertKnowledgeEmbedding(good); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*KnowledgeEmbedding){
		"no id":          func(e *KnowledgeEmbedding) { e.KnowledgeID = 0 },
		"no model":       func(e *KnowledgeEmbedding) { e.Model = "" },
		"no prefix":      func(e *KnowledgeEmbedding) { e.PrefixVersion = "" },
		"no hash":        func(e *KnowledgeEmbedding) { e.TextHash = "" },
		"no read time":   func(e *KnowledgeEmbedding) { e.ReadAt = "" },
		"dim mismatch":   func(e *KnowledgeEmbedding) { e.Dim = 4 },
		"empty vector":   func(e *KnowledgeEmbedding) { e.Dim, e.Vec = 0, nil },
		"zero scale":     func(e *KnowledgeEmbedding) { e.Scale = 0 },
		"negative scale": func(e *KnowledgeEmbedding) { e.Scale = -1 },
	} {
		e := good
		mutate(&e)
		if err := st.UpsertKnowledgeEmbedding(e); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	good.TextHash, good.Vec = "h2", []byte{4, 5, 6}
	if err := st.UpsertKnowledgeEmbedding(good); err != nil {
		t.Fatal(err)
	}
	var hash string
	var n int
	if err := st.db.QueryRow(`SELECT text_hash, (SELECT COUNT(*) FROM knowledge_embeddings) FROM knowledge_embeddings WHERE knowledge_id=1`).Scan(&hash, &n); err != nil || hash != "h2" || n != 1 {
		t.Fatalf("upsert replaces: %q %d %v", hash, n, err)
	}
}

func TestEmbedJobStateRoundTrips(t *testing.T) {
	st := openTest(t)
	if status, err := st.KnowledgeEmbeddingStatus(EmbedModel{Model: "m", PrefixVersion: "p"}); err != nil || status.Job != (EmbedJobState{}) {
		t.Fatalf("empty state: %+v %v", status, err)
	}
	want := EmbedJobState{LastOKAt: "2026-10-20T10:00:00Z", LastError: "boom", LastErrorAt: "2026-10-20T10:01:00Z", Failures: 3}
	if err := st.SetEmbedJobState(want); err != nil {
		t.Fatal(err)
	}
	want.Failures = 4
	if err := st.SetEmbedJobState(want); err != nil {
		t.Fatal(err)
	}
	status, err := st.KnowledgeEmbeddingStatus(EmbedModel{Model: "m", PrefixVersion: "p"})
	if err != nil || status.Job != want {
		t.Fatalf("%+v %v", status, err)
	}
}

// The runtime store routes reads through a second, read-only handle and writes
// through the writer; the embedding configuration and the vectors have to make
// it across both.
func TestHybridSearchThroughTheRuntimeStore(t *testing.T) {
	st, err := OpenRuntime(filepath.Join(t.TempDir(), "rt.db"), DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	_, c := newFakeEmbedder(t)
	addK(t, st, "car", "auto defekt ersatzteil", scope.Axes{}, "", "")
	addK(t, st, "wagen", "wagen kaputt", scope.Axes{}, "", "")
	embedAll(t, st, c)
	st.SetEmbedder(EmbedSearchConfig{Embedder: c})
	got, err := st.SearchKnowledge("auto defekt", scope.Axes{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"car", "wagen"}; !slices.Equal(searchTitles(got), want) || got[1].Match != MatchSemantic {
		t.Fatalf("%+v", got)
	}
	if status, err := st.KnowledgeEmbeddingStatus(modelOf(c)); err != nil || status.Current != 2 {
		t.Fatalf("%+v %v", status, err)
	}
	if err := st.SetEmbedJobState(EmbedJobState{LastError: "x", Failures: 1}); err != nil {
		t.Fatal(err)
	}
	// The other writes of the job go through the writer as well.
	if err := st.TouchKnowledgeEmbeddings([]int64{1, 2}, "2030-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	m := modelOf(c)
	d, err := st.KnowledgeEmbeddingDue(m, 8, time.Now(), true)
	if err != nil || len(d.Items) != 0 {
		t.Fatalf("%+v %v", d, err)
	}
	if err := st.RecordEmbeddingFailure(EmbeddingFailure{KnowledgeID: 1, Model: m.Model, PrefixVersion: m.PrefixVersion, TextHash: "x",
		Attempts: 1, RetryAt: "2030-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	st.SetEmbedder(EmbedSearchConfig{})
	got, _ = st.SearchKnowledge("auto defekt", scope.Axes{}, 10)
	if len(got) != 1 || got[0].Match != "" {
		t.Fatalf("switched off: %+v", got)
	}
}
