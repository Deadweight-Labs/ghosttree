package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Agent search answers with settled knowledge. A staged entry and the gardener's
// note about an entry share its words, and without a filter they would stand
// in front of it; asked for by is:staged or author:gardener they come back.
func TestSearchLeavesOutStagedAndGardenerUnlessAsked(t *testing.T) {
	f := accessAPI(t, true)
	for _, k := range []store.Knowledge{
		{Type: "pitfall", Title: "flarnix braucht Neustart", Body: "flarnix hängt nach dem Update", Confidence: "trusted"},
		{Type: "pitfall", Title: "flarnix Vermutung", Body: "flarnix vielleicht kaputt", Confidence: "staged"},
		{Type: "note", Title: "[Gärtner] Veraltet? flarnix", Body: "Vorschlag des Gärtners zu flarnix", Confidence: "staged",
			Harness: store.GardenerHarness, SessionRef: store.GardenerRefPrefix + "check-knowledge:1", Origin: "distilled"},
		{Type: "note", Title: "[Gärtner] bestätigt flarnix", Body: "bestätigte Notiz zu flarnix", Confidence: "trusted",
			Harness: store.GardenerHarness, SessionRef: store.GardenerRefPrefix + "check-knowledge:2", Origin: "distilled"},
	} {
		k.Scope, k.Person = scope.Axes{Project: accProject}, "mia"
		if _, err := f.st.InsertKnowledge(k); err != nil {
			t.Fatal(err)
		}
	}
	titles := func(q string) map[string]bool {
		t.Helper()
		v := url.Values{"q": {q}, "kind": {"knowledge"}, "project": {accProject}}
		res := decode[searchResult](t, f.expect(t, "robin", 200, "GET", "/api/search?"+v.Encode(), nil))
		out := map[string]bool{}
		for _, k := range res.Knowledge {
			out[k.Title] = true
		}
		return out
	}
	settled := "flarnix braucht Neustart"
	staged := "flarnix Vermutung"
	proposal := "[Gärtner] Veraltet? flarnix"
	confirmedNote := "[Gärtner] bestätigt flarnix"

	for _, q := range []string{"flarnix", "flarnix type:pitfall,note"} {
		got := titles(q)
		if !got[settled] || got[staged] || got[proposal] || got[confirmedNote] {
			t.Errorf("%q: want only the settled entry, got %v", q, got)
		}
	}
	if got := titles("flarnix is:staged"); !got[staged] || !got[proposal] || got[settled] {
		t.Errorf("is:staged: %v", got)
	}
	for _, q := range []string{"flarnix author:gardener", "flarnix harness:gardener"} {
		if got := titles(q); !got[proposal] || !got[confirmedNote] || got[staged] || got[settled] {
			t.Errorf("%q: %v", q, got)
		}
	}
	// Negated, the question is not asked: the default stays.
	if got := titles("flarnix -author:gardener"); !got[settled] || got[staged] || got[proposal] {
		t.Errorf("-author:gardener: %v", got)
	}
}

// One request for every area, as ctx search sends it: a failed area becomes a
// gap and the hits of the others still come back. Asked for that area alone,
// its failure is the answer.
func TestSearchAllAnswersWhenOneAreaFails(t *testing.T) {
	f := accessAPI(t, true)
	k := store.Knowledge{Type: "pitfall", Title: "zentrifol hängt", Body: "b", Confidence: "trusted"}
	k.Scope, k.Person = scope.Axes{Project: accProject}, "mia"
	if _, err := f.st.InsertKnowledge(k); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB().Exec(`DROP TABLE chunk_fts`); err != nil {
		t.Fatal(err)
	}
	v := url.Values{"q": {"zentrifol"}, "project": {accProject}}
	// The default search does not ask the sessions: their index failing costs nothing.
	res := decode[searchResult](t, f.expect(t, "robin", 200, "GET", "/api/search?"+v.Encode(), nil))
	if len(res.Knowledge) != 1 || len(res.Unavailable) != 0 {
		t.Errorf("kind=all without sessions: %+v / %+v", res.Knowledge, res.Unavailable)
	}
	// Another area failing is a gap in it, and the hits of the others still come back.
	if _, err := f.st.DB().Exec(`DROP TABLE search_documents_fts`); err != nil {
		t.Fatal(err)
	}
	res = decode[searchResult](t, f.expect(t, "robin", 200, "GET", "/api/search?"+v.Encode(), nil))
	if len(res.Knowledge) != 1 || len(res.Unavailable) != 1 || res.Unavailable[0] != (store.SearchGap{Area: "requests", Reason: store.GapFailed}) {
		t.Errorf("kind=all: %+v / %+v", res.Knowledge, res.Unavailable)
	}
	v.Set("q", "zentrifol kind:knowledge,session")
	res = decode[searchResult](t, f.expect(t, "robin", 200, "GET", "/api/search?"+v.Encode(), nil))
	if len(res.Knowledge) != 1 || len(res.Unavailable) != 1 || res.Unavailable[0].Area != "sessions" {
		t.Errorf("qualified: %+v / %+v", res.Knowledge, res.Unavailable)
	}
	v.Set("q", "zentrifol")
	v.Set("kind", "sessions")
	f.expect(t, "robin", 500, "GET", "/api/search?"+v.Encode(), nil)
}

// An area that runs out of its time budget is named as missing and the others
// are answered, whichever it is: the budget is not the sessions' alone. The areas
// run one after the other, so the search as a whole ends at its own limit too.
func TestSearchAreaOutOfTimeIsAGapAndTheOthersAnswer(t *testing.T) {
	f := accessAPI(t, true)
	k := store.Knowledge{Type: "pitfall", Title: "zentrifol hängt", Body: "b", Confidence: "trusted"}
	k.Scope, k.Person = scope.Axes{Project: accProject}, "mia"
	if _, err := f.st.InsertKnowledge(k); err != nil {
		t.Fatal(err)
	}
	old := areaBudgetFor
	t.Cleanup(func() { areaBudgetFor = old })

	for _, slow := range []string{"sessions", "requests"} {
		areaBudgetFor = func(area string) time.Duration {
			if area == slow {
				return -time.Second
			}
			return areaBudget
		}
		// Several kinds named in the line: the qualified path.
		v := url.Values{"q": {"zentrifol kind:knowledge,requests,session"}, "project": {accProject}}
		res := decode[searchResult](t, f.expect(t, "robin", 200, "GET", "/api/search?"+v.Encode(), nil))
		if len(res.Knowledge) != 1 || len(res.Unavailable) != 1 || res.Unavailable[0] != (store.SearchGap{Area: slow, Reason: store.GapTimeout}) {
			t.Errorf("qualified, %s out of time: %+v / %+v", slow, res.Knowledge, res.Unavailable)
		}
	}

	// The default search of all kinds: knowledge answers when the requests run out of time.
	areaBudgetFor = func(area string) time.Duration {
		if area == "requests" {
			return -time.Second
		}
		return areaBudget
	}
	v := url.Values{"q": {"zentrifol"}, "project": {accProject}}
	res := decode[searchResult](t, f.expect(t, "robin", 200, "GET", "/api/search?"+v.Encode(), nil))
	if len(res.Knowledge) != 1 || len(res.Unavailable) != 1 || res.Unavailable[0] != (store.SearchGap{Area: "requests", Reason: store.GapTimeout}) {
		t.Errorf("all, requests out of time: %+v / %+v", res.Knowledge, res.Unavailable)
	}
	// Knowledge out of time: the requests are still answered.
	areaBudgetFor = func(area string) time.Duration {
		if area == "knowledge" {
			return -time.Second
		}
		return areaBudget
	}
	res = decode[searchResult](t, f.expect(t, "robin", 200, "GET", "/api/search?"+v.Encode(), nil))
	if len(res.Knowledge) != 0 || len(res.Unavailable) != 1 || res.Unavailable[0] != (store.SearchGap{Area: "knowledge", Reason: store.GapTimeout}) {
		t.Errorf("all, knowledge out of time: %+v / %+v", res.Knowledge, res.Unavailable)
	}
}

// With every place among the searches taken, a search waits only for searchSlotWait
// and then answers search_busy with Retry-After, so that a client with a timeout
// sees the answer and not its own timeout. A place freed in time is used.
func TestSearchAnswersBusyWithRetryAfterWhenNoPlaceIsFree(t *testing.T) {
	f := accessAPI(t, true)
	old := searchSlotWait
	searchSlotWait = 150 * time.Millisecond
	t.Cleanup(func() { searchSlotWait = old })

	var held []func()
	for {
		release, ok := f.st.AcquireSearchSlot(context.Background(), time.Millisecond)
		if !ok {
			break
		}
		held = append(held, release)
	}
	t.Cleanup(func() {
		for _, r := range held {
			r()
		}
	})
	if len(held) == 0 {
		t.Fatal("the store has no places at all")
	}

	get := func() *http.Response {
		req, _ := http.NewRequest("GET", f.srv.URL+"/api/search?q=zentrifol&project="+accProject, nil)
		req.Header.Set("Authorization", "Bearer "+f.tok["robin"])
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	began := time.Now()
	resp := get()
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), `"search_busy"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Errorf("no Retry-After: %v", resp.Header)
	}
	if took := time.Since(began); took < 140*time.Millisecond || took > 3*time.Second {
		t.Errorf("the search waited %v, not about the limit", took)
	}

	// A place freed within the wait: the search runs.
	go func() { time.Sleep(40 * time.Millisecond); held[0](); held = held[1:] }()
	resp2 := get()
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("a place was freed in time, the answer is %d", resp2.StatusCode)
	}
}

// A search of one area is bound to the request: when the caller hangs up, the SQL
// stops. Before, the store of the single area had no context, and the query ran on
// holding its place and its connection.
func TestSingleAreaSearchEndsWithTheRequest(t *testing.T) {
	f := accessAPI(t, true)
	a := &api{st: f.st}
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("GET", "/api/search?q=secretterm&kind=sessions", nil).WithContext(ctx)
	gaps := a.newSearchGaps(r, false)
	defer gaps.close()
	st, done := gaps.budget("sessions")

	if _, err := st.SearchSessions("secretterm", scope.Axes{}, "", 10); err != nil {
		t.Fatalf("the search of a live request: %v", err)
	}
	cancel()
	_, err := st.SearchSessions("secretterm", scope.Axes{}, "", 10)
	if err := done(err); !errors.Is(err, context.Canceled) {
		t.Fatalf("after the caller hung up the search went on: %v", err)
	}
}
