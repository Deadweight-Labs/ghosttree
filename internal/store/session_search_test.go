package store

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// sessionQuery is the query of the api, which looks for any of the words;
// sessionQueryWeb is the one of the web view, which wants all of them in one text.
const (
	sessionQuery    = "q1 q2 q3 q4"
	sessionQueryWeb = "q1"
)

// searchWorld: the sessions V1, V2 and V3 are visible to everybody who may read
// guest sessions; `hidden` sessions of mia and a few of another project are not.
// They say in other words what the query says, better than V3 does, so that
// fused with the rest they would fill the places for hits of meaning alone. For
// the query the words rank V2 and V1 (the later session first among equals, as
// for any reader who may not read it all) and the vectors V2, V3 and V1: the
// fusion is V2, V1, V3, with V3 found by meaning alone.
func searchWorld(t *testing.T, hidden int) (*Store, *embed.Client) {
	t.Helper()
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	if _, err := st.EnsureProject("person:1", otherProject); err != nil {
		t.Fatal(err)
	}
	_, c := newFakeEmbedder(t)
	add := func(ext string, account int64, project, vis, text string) {
		addSession(t, st, ext, account, project, "box", vis, userChunks(text))
	}
	add("V1", 4, roleProject, VisGuests, "q1 q2 f1 f2 f3 f4 please look at this one")
	add("V2", 4, roleProject, VisGuests, "q1 s2 s3 let us look at it please now")
	add("V3", 4, roleProject, VisGuests, "s1 s2 and a few words around them here")
	for i := range hidden {
		add(fmt.Sprintf("H%d", i), 3, roleProject, "", fmt.Sprintf("s1 s2 s3 s4 together, hidden number %d", i))
	}
	for i := range 3 {
		add(fmt.Sprintf("X%d", i), 1, otherProject, VisGuests, "s1 s2 s3 s4 together, in another project")
	}
	embedSessionTexts(t, st, c, later(2*SessionSettle))
	st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindSession}})
	return st, c
}

// handlerSearch is the call the HTTP handler makes for a reader.
func handlerSearch(t *testing.T, st *Store, pa *ProjectAccess, q string, limit int, mode SearchMode) []SessionHit {
	t.Helper()
	hits, err := st.SearchSessionsWith(q, SessionSearch{Limit: limit, Prefilter: pa.TranscriptPrefilter(), Mode: mode})
	if err != nil {
		t.Fatal(err)
	}
	var out []SessionHit
	for _, h := range hits {
		if pa.CanSeeTranscript(h.Session) {
			out = append(out, h)
		}
	}
	return out
}

func sessionHitNames(hits []SessionHit) string {
	var out []string
	for _, h := range hits {
		name := h.Session.ExternalID
		if h.Match == MatchSemantic {
			name += "*"
		}
		out = append(out, name)
	}
	return strings.Join(out, " ")
}

func pageNames(t *testing.T, st *Store, pa *ProjectAccess, q string, limit int) (string, SearchPage) {
	t.Helper()
	page, err := st.SearchTranscripts(pa, SearchQuery{Q: q, Limit: limit})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, g := range page.Groups {
		raw, err := st.SessionByPublicID(g.Session.PublicID)
		if err != nil {
			t.Fatal(err)
		}
		name := raw.ExternalID
		if g.Match == MatchSemantic {
			name += "*"
		}
		out = append(out, name)
	}
	return strings.Join(out, " "), page
}

func TestSessionSearchFusesWordsAndMeaning(t *testing.T) {
	st, _ := searchWorld(t, 0)
	gus := viewer(st, "person:5", "gus")
	got := handlerSearch(t, st, gus, sessionQuery, 10, SearchAuto)
	if want := "V2 V1 V3*"; sessionHitNames(got) != want {
		t.Fatalf("api: %s, want %s", sessionHitNames(got), want)
	}
	for _, h := range got {
		if h.Snippet == "" || h.Seq != 0 {
			t.Errorf("a hit shows a text and where it is: %+v", h)
		}
	}
	// A hit that the words found shows what they found; one that only the meaning
	// found shows the text that came closest, from its beginning.
	if !strings.Contains(got[2].Snippet, "s1 s2 and a few words") {
		t.Errorf("the hit of meaning shows %q", got[2].Snippet)
	}
	web, page := pageNames(t, st, gus, sessionQueryWeb, 10)
	if web != "V2 V1 V3*" || page.Sessions != 3 {
		t.Fatalf("web: %s (%d sessions)", web, page.Sessions)
	}
	if hits := page.Groups[2].Hits; len(hits) != 1 || hits[0].Seq != 0 || len(hits[0].Parts) == 0 || hits[0].Kind != "user" || page.Groups[2].Total != 0 {
		t.Fatalf("the group of meaning: %+v", page.Groups[2])
	}
	if hybrid, _ := st.EmbedSearchStats(); hybrid < 2 {
		t.Fatalf("hybrid searches: %d", hybrid)
	}
}

// The embedding ranking counts twice as much as the words: the session the
// vectors put first goes first, where the words put the other one.
func TestSessionSearchWeightsTheEmbeddingRanking(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	_, c := newFakeEmbedder(t)
	// B first, A later: for the words (equal counts) the later session is first; for
	// the vectors B, which says what the query says, is.
	addSession(t, st, "B", 4, roleProject, "box", VisGuests, userChunks("q1 s2 s3 s4 together as one thing"))
	addSession(t, st, "A", 4, roleProject, "box", VisGuests, userChunks("q1 plus a lot of other words around it for dilution here"))
	embedSessionTexts(t, st, c, later(2*SessionSettle))
	gus := viewer(st, "person:5", "gus")
	for _, tc := range []struct {
		name    string
		weights map[string]float64
		want    string
	}{
		{"the default weight of sessions is 2", nil, "B A"},
		{"weight 2", map[string]float64{KindSession: 2}, "B A"},
		{"equal weights", map[string]float64{KindSession: 1}, "A B"},
		{"a weight that is not positive is the default", map[string]float64{KindSession: 0}, "B A"},
		{"a weight of another kind is no weight of sessions", map[string]float64{KindRequest: 1}, "B A"},
	} {
		st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindSession}, VectorWeights: tc.weights})
		if got := sessionHitNames(handlerSearch(t, st, gus, sessionQuery, 10, SearchAuto)); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

// At most five hits of ten are there by meaning alone, however many sessions are
// near: the rest of the ten is what the words found.
func TestSessionSearchShowsAtMostFiveHitsOfMeaningAlone(t *testing.T) {
	st := orgStore(t, "robin")
	_, c := newFakeEmbedder(t)
	for i := range 12 {
		addSession(t, st, fmt.Sprintf("n%02d", i), 1, "", "m", "", userChunks(fmt.Sprintf("s1 s2 s3 and number %d of the neighbours here", i)))
	}
	addSession(t, st, "words", 1, "", "m", "", userChunks("q1 is what this one has in common with the query"))
	embedSessionTexts(t, st, c, later(2*SessionSettle))
	st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindSession}})
	pa := viewer(st, "person:1", "robin")
	hits, err := st.SearchSessionsWith("q1 q2 q3", SessionSearch{Limit: 10, Prefilter: pa.TranscriptPrefilter()})
	if err != nil {
		t.Fatal(err)
	}
	semantic := 0
	for _, h := range hits {
		if h.Match == MatchSemantic {
			semantic++
		}
	}
	if len(hits) != 6 || semantic != 5 || hits[0].Session.ExternalID != "words" || hits[0].Match != "" {
		t.Fatalf("%d hits, %d by meaning: %s", len(hits), semantic, sessionHitNames(hits))
	}
	// A short page is half meaning at most.
	few, err := st.SearchSessionsWith("q1 q2 q3", SessionSearch{Limit: 3, Prefilter: pa.TranscriptPrefilter()})
	if err != nil || len(few) != 3 || strings.Count(sessionHitNames(few), "*") != 2 {
		t.Fatalf("%s %v", sessionHitNames(few), err)
	}
	// The ranking alone, as the measurement asks for it, is not capped.
	only, err := st.SearchSessionsWith("q1 q2 q3", SessionSearch{Limit: 10, Prefilter: pa.TranscriptPrefilter(), Mode: SearchVectorOnly})
	if err != nil || len(only) != 10 || strings.Count(sessionHitNames(only), "*") != 10 {
		t.Fatalf("%s %v", sessionHitNames(only), err)
	}
}

// ---------------------------------------------------------------- unchanged without the switch

// Without sessions among the search kinds nothing changes, however many vectors
// there are: the answer is that of the full-text search, to the byte.
func TestSessionSearchIsUnchangedWithoutTheSwitch(t *testing.T) {
	st, c := searchWorld(t, 3)
	gus := viewer(st, "person:5", "gus")
	robin := viewer(st, "person:1", "robin")
	off := func() {
		st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindKnowledge, KindRequest}})
	}
	st.SetEmbedder(EmbedSearchConfig{})
	for _, pa := range []*ProjectAccess{gus, robin} {
		plain, err := st.SearchSessionsVisible(sessionQuery, scope.Axes{}, "", 10, nil, pa.TranscriptPrefilter())
		if err != nil {
			t.Fatal(err)
		}
		plainPage, err := st.SearchTranscripts(pa, SearchQuery{Q: sessionQuery, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		off()
		got, err := st.SearchSessionsWith(sessionQuery, SessionSearch{Limit: 10, Prefilter: pa.TranscriptPrefilter()})
		if err != nil || !reflect.DeepEqual(got, plain) {
			t.Fatalf("the api answer changed without the switch: %v %v", err, sessionHitNames(got))
		}
		gotPage, err := st.SearchTranscripts(pa, SearchQuery{Q: sessionQuery, Limit: 10})
		if err != nil || !reflect.DeepEqual(gotPage, plainPage) {
			t.Fatalf("the web answer changed without the switch: %v", err)
		}
		if _, err := st.SearchSessionsWith(sessionQuery, SessionSearch{Limit: 10, Prefilter: pa.TranscriptPrefilter(), Mode: SearchVectorOnly}); err == nil {
			t.Fatal("the measurement of a kind that is not switched on must say so, not answer from words")
		}
		st.SetEmbedder(EmbedSearchConfig{})
	}
	if hybrid, _ := st.EmbedSearchStats(); hybrid != 0 {
		t.Fatalf("%d hybrid searches without the switch", hybrid)
	}
	// With the switch the answer is another.
	st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindSession}})
	got, _ := st.SearchSessionsWith(sessionQuery, SessionSearch{Limit: 10, Prefilter: gus.TranscriptPrefilter()})
	if !strings.Contains(sessionHitNames(got), "*") {
		t.Fatalf("with the switch: %s", sessionHitNames(got))
	}
}

// A query of ordinary words asks for a list: it is not sent to the embedding
// server, and the sort by time is not ranked.
func TestSessionSearchLeavesListsAndTheSortByTimeAlone(t *testing.T) {
	st, _ := searchWorld(t, 0)
	robin := viewer(st, "person:1", "robin")
	before := st.embedShared().hybrid.Load()
	if _, err := st.SearchSessionsWith("was ist das", SessionSearch{Limit: 10, Prefilter: robin.TranscriptPrefilter()}); err != nil {
		t.Fatal(err)
	}
	if st.embedShared().hybrid.Load() != before {
		t.Fatal("a question of ordinary words was answered by meaning")
	}
	newest, err := st.SearchTranscripts(robin, SearchQuery{Q: sessionQuery, Sort: "newest", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range newest.Groups {
		if g.Match != "" {
			t.Fatalf("a search by time has a hit of meaning: %+v", g)
		}
	}
	// The kinds of text that are not messages are what they say.
	for _, kind := range []string{"output", "commands", "thinking"} {
		page, err := st.SearchTranscripts(robin, SearchQuery{Q: sessionQuery, Kind: kind, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range page.Groups {
			if g.Match != "" {
				t.Fatalf("kind %s has a hit of meaning", kind)
			}
		}
	}
	for _, kind := range []string{"", "messages", "all"} {
		page, err := st.SearchTranscripts(robin, SearchQuery{Q: sessionQuery, Kind: kind, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(fmt.Sprint(groupMatches(page)), MatchSemantic); n == 0 {
			t.Fatalf("kind %q has no hit of meaning: %+v", kind, groupMatches(page))
		}
	}
}

func groupMatches(p SearchPage) []string {
	var out []string
	for _, g := range p.Groups {
		out = append(out, g.Match)
	}
	return out
}

// Without a server that answers, the search answers from words like before.
func TestSessionSearchFallsBackToWordsWhenTheEmbeddingFails(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	srv, c := newFakeEmbedder(t)
	addSession(t, st, "V1", 4, roleProject, "box", VisGuests, userChunks("q1 q2 f1 f2 f3 f4 please look at this one"))
	addSession(t, st, "V3", 4, roleProject, "box", VisGuests, userChunks("s1 s2 and a few words around them here"))
	embedSessionTexts(t, st, c, later(2*SessionSettle))
	st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindSession}})
	gus := viewer(st, "person:5", "gus")
	srv.Fail.Store(true)
	got, err := st.SearchSessionsWith(sessionQuery, SessionSearch{Limit: 10, Prefilter: gus.TranscriptPrefilter()})
	if err != nil {
		t.Fatal(err)
	}
	want, err := st.SearchSessionsVisible(sessionQuery, scope.Axes{}, "", 10, nil, gus.TranscriptPrefilter())
	if err != nil || !reflect.DeepEqual(got, want) || len(got) != 1 {
		t.Fatalf("%v %s", err, sessionHitNames(got))
	}
	if _, fallbacks := st.EmbedSearchStats(); fallbacks != 1 {
		t.Fatalf("fallbacks %d", fallbacks)
	}
	// And a measurement does not take the fallback for an answer.
	if _, err := st.SearchSessionsWith(sessionQuery, SessionSearch{Limit: 10, Mode: SearchHybridStrict}); err == nil {
		t.Fatal("a strict search answered while the server failed")
	}
}

// ---------------------------------------------------------------- who sees what

// The vectors answer for the sessions the reader may read and for no others:
// every kind of reader gets what the words search gives him, and no more.
func TestSessionSearchByMeaningSeesOnlyTheReadableSet(t *testing.T) {
	f := newVisFixture(t)
	_, c := newFakeEmbedder(t)
	// newVisFixture's sessions all say "zebra"; the vectors are of what each
	// says in other words.
	for _, s := range []Session{f.a, f.b, f.c, f.d, f.e} {
		if err := f.st.AppendChunks(s.ID, []Chunk{{Seq: 50, Raw: promptLine(0, "s1 s2 s3 s4 together in "+s.ExternalID)}}); err != nil {
			t.Fatal(err)
		}
	}
	embedSessionTexts(t, f.st, c, later(2*SessionSettle))
	f.st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindSession}})
	for _, who := range []struct {
		id, label string
		want      []string
	}{
		{"person:1", "robin", []string{"a-robin-private", "c-rex-project", "d-rex-guests"}},
		{"person:2", "lena", []string{"c-rex-project", "d-rex-guests"}},
		{"person:3", "mia", []string{"b-mia-private", "c-rex-project", "d-rex-guests"}},
		{"person:4", "rex", []string{"c-rex-project", "d-rex-guests", "e-rex-private"}},
		{"person:5", "gus", []string{"d-rex-guests"}},
		{"person:6", "nora", nil},
	} {
		pa := viewer(f.st, who.id, who.label)
		api := handlerSearch(t, f.st, pa, sessionQuery, 20, SearchAuto)
		got := map[string]bool{}
		for _, h := range api {
			got[h.Session.ExternalID] = true
			if h.Match != MatchSemantic {
				t.Errorf("%s: %s is not found by words", who.label, h.Session.ExternalID)
			}
		}
		if !sameKeys(got, who.want...) {
			t.Errorf("%s: the api gives %v, want %v", who.label, got, who.want)
		}
		_, web := searchIDs(t, f.st, pa, sessionQuery)
		if !sameKeys(web, who.want...) {
			t.Errorf("%s: the web gives %v, want %v", who.label, web, who.want)
		}
		// What the vector part may give is a part of what the words part may give.
		_, allowed := searchIDs(t, f.st, pa, "zebra")
		for name := range got {
			if !allowed[name] {
				t.Errorf("%s gets %s by meaning, which the words search would not give him", who.label, name)
			}
		}
	}
}

// What a reader may not see can neither appear nor move a session he may see: the
// ranking is made from the readable set. The test is only worth something if
// fusing everything and filtering afterwards would give another order.
func TestSessionSearchHiddenSessionsMoveNoOtherSession(t *testing.T) {
	gus := Principal{ID: "person:5", Label: "gus"}
	with, _ := searchWorld(t, 40)
	without, _ := searchWorld(t, 0)
	order := func(st *Store) (api, web string) {
		pa := st.Access(gus)
		api = sessionHitNames(handlerSearch(t, st, pa, sessionQuery, 10, SearchAuto))
		web, _ = pageNames(t, st, pa, sessionQueryWeb, 10)
		return
	}
	api0, web0 := order(without)
	if api0 != "V2 V1 V3*" || web0 != "V2 V1 V3*" {
		t.Fatalf("the readable sessions alone rank %q (api) and %q (web)", api0, web0)
	}
	api1, web1 := order(with)
	if api1 != api0 || web1 != web0 {
		t.Fatalf("40 hidden sessions changed what a guest sees: api %q -> %q, web %q -> %q", api0, api1, web0, web1)
	}
	for _, name := range strings.Fields(api1 + " " + web1) {
		if strings.HasPrefix(name, "H") || strings.HasPrefix(name, "X") {
			t.Fatalf("a guest got %s", name)
		}
	}
	// The naive way: rank the whole stock, then keep what the guest may read.
	pa := with.Access(gus)
	all, err := with.SearchSessionsWith(sessionQuery, SessionSearch{Limit: 200, Prefilter: SessionPrefilter{Unrestricted: true}})
	if err != nil {
		t.Fatal(err)
	}
	var naive []SessionHit
	for _, h := range all {
		if pa.CanSeeTranscript(h.Session) {
			naive = append(naive, h)
		}
	}
	if sessionHitNames(naive) == api0 {
		t.Fatalf("the fixture does not tell filtering before the fusion from filtering after it: %s", sessionHitNames(naive))
	}
}

// The part of a hit that names its session is the one the reader may see: the
// handler applies MetaView to it, and a guest does not get who and where.
func TestSessionSearchHitsGoThroughMetaViewForGuests(t *testing.T) {
	st, _ := searchWorld(t, 0)
	gus := viewer(st, "person:5", "gus")
	for _, h := range handlerSearch(t, st, gus, sessionQuery, 10, SearchAuto) {
		v := gus.MetaView(h.Session)
		if v.ID != 0 || v.Scope.Machine != "" || v.Scope.Branch != "" || v.CWD != "" || v.ExternalID != "" || v.Owner != "" || v.PublicID == "" || v.Title == "" {
			t.Errorf("a guest sees %+v of %s", v, h.Session.ExternalID)
		}
	}
}

// The asking session is left out of both rankings, like it is for words.
func TestSessionSearchLeavesOutTheAskingSession(t *testing.T) {
	st, _ := searchWorld(t, 0)
	rex := viewer(st, "person:4", "rex")
	hits, err := st.SearchSessionsWith(sessionQuery, SessionSearch{Limit: 10, ExcludeSession: "V3", Prefilter: rex.TranscriptPrefilter()})
	if err != nil || strings.Contains(sessionHitNames(hits), "V3") || !strings.Contains(sessionHitNames(hits), "V2") {
		t.Fatalf("%s %v", sessionHitNames(hits), err)
	}
}

// The filters of a search (project, machine) are part of both rankings.
func TestSessionSearchFiltersApplyToTheVectorsToo(t *testing.T) {
	st, _ := searchWorld(t, 0)
	robin := viewer(st, "person:1", "robin")
	hits, err := st.SearchSessionsWith(sessionQuery, SessionSearch{Limit: 20, Filter: scope.Axes{Project: otherProject}, Prefilter: robin.TranscriptPrefilter()})
	if err != nil || len(hits) != 3 {
		t.Fatalf("%s %v", sessionHitNames(hits), err)
	}
	for _, h := range hits {
		if h.Session.Scope.Project != otherProject || !strings.HasPrefix(h.Session.ExternalID, "X") {
			t.Fatalf("%+v", h.Session)
		}
	}
	rows, err := st.SearchTranscripts(robin, SearchQuery{Q: sessionQuery, Filter: SessionFilter{Project: otherProject}, Limit: 10})
	if err != nil || rows.Sessions != 3 {
		t.Fatalf("%+v %v", rows, err)
	}
}

// The sessions found by words and the few found by meaning are shown once each
// across the pages of a web search, and the budget for meaning alone is that of
// the whole list, not of a page.
func TestSessionSearchPagesHoldTogether(t *testing.T) {
	st := orgStore(t, "robin")
	_, c := newFakeEmbedder(t)
	for i := range 9 {
		text := fmt.Sprintf("s1 s2 s3 and number %d of the neighbours here", i)
		if i%3 == 0 {
			text = fmt.Sprintf("q1 is in this one, number %d, and little else", i)
		}
		addSession(t, st, fmt.Sprintf("n%02d", i), 1, "", "m", "", userChunks(text))
	}
	embedSessionTexts(t, st, c, later(2*SessionSettle))
	st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindSession}})
	pa := viewer(st, "person:1", "robin")
	var seen, kinds []string
	cursor := ""
	for range 10 {
		page, err := st.SearchTranscripts(pa, SearchQuery{Q: "q1", Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range page.Groups {
			seen = append(seen, g.Session.PublicID)
			kinds = append(kinds, g.Match)
		}
		if page.Sessions != 4 {
			t.Fatalf("the list holds %d sessions on one page and %d on another", page.Sessions, 4)
		}
		// The facets count what the list holds: the three sessions that have the
		// word and the one neighbour the budget lets in, not the six neighbours.
		total := 0
		for _, o := range page.Facets.Harnesses {
			total += o.Count
		}
		if total != 4 {
			t.Fatalf("the facets count %d sessions, the list holds 4: %+v", total, page.Facets)
		}
		if cursor = page.Next; cursor == "" {
			break
		}
	}
	// Three sessions have the word, and of the six that only come near one (a page
	// of two, a budget of (2+1)/2) is let in.
	slices.Sort(seen)
	if len(slices.Compact(slices.Clone(seen))) != len(seen) || len(seen) != 4 || strings.Count(strings.Join(kinds, ","), MatchSemantic) != 1 {
		t.Fatalf("pages gave %v with %v", seen, kinds)
	}
}
