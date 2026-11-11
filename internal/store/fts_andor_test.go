package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestFtsExprsJoinsWordsBothWays(t *testing.T) {
	and, or := ftsExprs("go test")
	if and != `"go" AND "test"` || or != `"go" OR "test"` {
		t.Fatalf("and = %q, or = %q", and, or)
	}
	// One word, or only ordinary words: nothing to tighten, OR is the search as before.
	for _, q := range []string{"livekit", "was ist noch zu tun", ""} {
		and, or := ftsExprs(q)
		if and != "" || or != ftsQuery(q) {
			t.Errorf("%q: and = %q, or = %q, want no AND and %q", q, and, or, ftsQuery(q))
		}
	}
}

func TestAndThenOrStopsWhenAndIsEnough(t *testing.T) {
	var asked []string
	got, err := andThenOr("alpha beta", 2, 10, idKey, func(expr string) ([]int64, error) {
		asked = append(asked, expr)
		return []int64{1, 2}, nil
	})
	if err != nil || !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if len(asked) != 1 || asked[0] != `"alpha" AND "beta"` {
		t.Errorf("asked %q, want the AND expression only", asked)
	}
}

func TestAndThenOrFillsWithOrWithoutDuplicates(t *testing.T) {
	got, err := andThenOr("alpha beta", 3, 4, idKey, func(expr string) ([]int64, error) {
		if expr == `"alpha" AND "beta"` {
			return []int64{7}, nil
		}
		return []int64{3, 7, 5, 9, 11}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The row with both words first, then the others in the order of their rank,
	// the AND row not twice, never more than the limit.
	if want := []int64{7, 3, 5, 9}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAndThenOrAsksOnceForOneWord(t *testing.T) {
	calls := 0
	_, _ = andThenOr("livekit", 5, 5, idKey, func(expr string) ([]int64, error) {
		calls++
		if expr != `"livekit"` {
			t.Errorf("expr = %q", expr)
		}
		return nil, nil
	})
	if calls != 1 {
		t.Errorf("run %d times, want once", calls)
	}
}

func TestAndThenOrFailsWithTheFallback(t *testing.T) {
	want := fmt.Errorf("boom")
	_, err := andThenOr("alpha beta", 3, 4, idKey, func(expr string) ([]int64, error) {
		if expr == `"alpha" AND "beta"` {
			return []int64{1}, nil
		}
		return nil, want
	})
	if err != want {
		t.Errorf("err = %v, want the error of the fallback, not half an answer", err)
	}
}

// sessionWithChunk stores one session with a single chunk of text.
func sessionWithChunk(t *testing.T, s *Store, ext, text string) {
	t.Helper()
	id, err := s.UpsertSession(Session{Harness: "codex", ExternalID: ext, Scope: scope.Axes{Project: "github.com/x/y"}, StartedAt: "2026-08-23T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChunks(id, []Chunk{{Seq: 0, Role: "user", Text: text, Raw: codexTextLine("user", text)}}); err != nil {
		t.Fatal(err)
	}
}

func hitExternalIDs(hits []SessionHit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Session.ExternalID
	}
	return out
}

func TestSessionSearchWithSeveralWordsPutsTheChunkWithAllOfThemFirst(t *testing.T) {
	s := openTest(t)
	// Short chunks that repeat one of the words outrank, by bm25 alone, the one long
	// chunk that has each word once; with AND first that chunk leads.
	for i := 0; i < 4; i++ {
		sessionWithChunk(t, s, "only-a"+strconv.Itoa(i), "zorblax zorblax zorblax zorblax")
		sessionWithChunk(t, s, "only-b"+strconv.Itoa(i), "quuxwidget quuxwidget quuxwidget quuxwidget")
	}
	sessionWithChunk(t, s, "both", "zorblax quuxwidget "+strings.Repeat("filler words around them ", 12))
	hits, err := s.SearchSessions("zorblax quuxwidget", scope.Axes{}, "", 3)
	if err != nil {
		t.Fatal(err)
	}
	ids := hitExternalIDs(hits)
	if len(ids) != 3 || ids[0] != "both" {
		t.Fatalf("hits = %v, want the chunk with both words first, then two others", ids)
	}
	// OR fills the page: the one chunk with both words is fewer than the three asked
	// for, and no chunk is listed twice.
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Errorf("%s twice in %v", id, ids)
		}
		seen[id] = true
	}
}

func TestSessionSearchWithSeveralWordsDoesNotRunOrWhenAndFillsThePage(t *testing.T) {
	s := openTest(t)
	for i := 0; i < 4; i++ {
		sessionWithChunk(t, s, "both"+strconv.Itoa(i), "zorblax quuxwidget")
	}
	sessionWithChunk(t, s, "one", "zorblax")
	hits, err := s.SearchSessions("zorblax quuxwidget", scope.Axes{}, "", 4)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range hitExternalIDs(hits) {
		if id == "one" {
			t.Errorf("hits = %v: the chunk with one word came in although four have both", hitExternalIDs(hits))
		}
	}
	if len(hits) != 4 {
		t.Errorf("got %d hits, want 4", len(hits))
	}
}

// A search on a view whose context has ended ends with that error: the time
// budget of a search area (WithContext) relies on it, in the readable set and in
// the vector ranking alike, so that a slow area is named as missing at its budget
// and not after the work is done.
func TestSessionSearchByMeaningEndsWithTheContextOfTheView(t *testing.T) {
	st, _ := searchWorld(t, 0)
	gus := viewer(st, "person:5", "gus")
	pre := gus.TranscriptPrefilter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, mode := range []SearchMode{SearchAuto, SearchVectorOnly} {
		_, err := st.WithContext(ctx).SearchSessionsWith(sessionQuery, SessionSearch{Limit: 10, Prefilter: pre, Mode: mode})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("mode %v: err = %v, want the search to end with the context of its view", mode, err)
		}
	}
}
