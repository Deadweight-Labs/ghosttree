package embedjob

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestParseWindow(t *testing.T) {
	for in, want := range map[string]Window{
		"00:00-06:00":    {Start: 0, End: 360},
		" 01:30 - 07:00": {Start: 90, End: 420},
		"22:00-04:00":    {Start: 22 * 60, End: 4 * 60},
		"6:00-23:59":     {Start: 360, End: 23*60 + 59},
		"00:00-24:00":    {Start: 0, End: 24 * 60},
		"always":         {Always: true},
		"ALWAYS":         {Always: true},
	} {
		got, err := ParseWindow(in)
		if err != nil || got != want {
			t.Errorf("%q: %+v %v, want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "6", "06:00", "06:00-", "-06:00", "24:00-02:00", "25:00-26:00", "06:60-07:00", "6:0-7:00",
		"06:00-06:00", "00:00-00:00", "night", "06:00-07:00-08:00", "ab:cd-ef:gh"} {
		if got, err := ParseWindow(bad); err == nil {
			t.Errorf("%q accepted as %+v", bad, got)
		}
	}
	// String is what ParseWindow reads.
	for _, w := range []Window{DefaultSessionWindow, {Always: true}, {Start: 22 * 60, End: 4*60 + 5}} {
		if back, err := ParseWindow(w.String()); err != nil || back != w {
			t.Errorf("%+v -> %q -> %+v %v", w, w.String(), back, err)
		}
	}
}

func TestWindowContainsAndOpens(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 10, 21, h, m, 0, 0, time.UTC) }
	night := Window{Start: 0, End: 6 * 60}
	for _, c := range []struct {
		w    Window
		h, m int
		in   bool
	}{
		{night, 0, 0, true}, {night, 3, 30, true}, {night, 5, 59, true}, {night, 6, 0, false}, {night, 12, 0, false}, {night, 23, 59, false},
		{Window{Start: 22 * 60, End: 4 * 60}, 23, 0, true}, {Window{Start: 22 * 60, End: 4 * 60}, 2, 0, true},
		{Window{Start: 22 * 60, End: 4 * 60}, 4, 0, false}, {Window{Start: 22 * 60, End: 4 * 60}, 12, 0, false}, {Window{Start: 22 * 60, End: 4 * 60}, 22, 0, true},
		{Window{Always: true}, 12, 0, true}, {Window{Start: 0, End: 24 * 60}, 23, 59, true},
	} {
		if got := c.w.Contains(at(c.h, c.m)); got != c.in {
			t.Errorf("%s at %02d:%02d: %v", c.w, c.h, c.m, got)
		}
	}
	for _, c := range []struct {
		w    Window
		h, m int
		wait time.Duration
	}{
		{night, 3, 0, 0}, {night, 12, 0, 12 * time.Hour}, {night, 23, 30, 30 * time.Minute},
		{Window{Start: 22 * 60, End: 4 * 60}, 12, 0, 10 * time.Hour}, {Window{Start: 22 * 60, End: 4 * 60}, 4, 0, 18 * time.Hour},
		{Window{Always: true}, 12, 0, 0},
	} {
		if got := c.w.Opens(at(c.h, c.m)); got != c.wait {
			t.Errorf("%s at %02d:%02d opens in %v, want %v", c.w, c.h, c.m, got, c.wait)
		}
	}
	// The seconds count: at 23:59:30 the window opens in thirty seconds.
	if got := night.Opens(time.Date(2026, 10, 21, 23, 59, 30, 0, time.UTC)); got != 30*time.Second {
		t.Errorf("%v", got)
	}
}

func (f fixture) addSession(t *testing.T, ext string, texts ...string) {
	t.Helper()
	id, err := f.st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: ext})
	if err != nil {
		t.Fatal(err)
	}
	var chunks []store.Chunk
	for i, text := range texts {
		raw, _ := json.Marshal(map[string]any{"type": "user", "timestamp": "2026-10-01T10:00:00Z", "message": map[string]any{"role": "user", "content": text}})
		chunks = append(chunks, store.Chunk{Seq: i, Role: "user", Text: text, Raw: string(raw)})
	}
	if err := f.st.AppendChunks(id, chunks); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) sessionStatus(t *testing.T) store.EmbedStatus {
	t.Helper()
	return f.kindStatus(t, store.KindSession)
}

func numbered(prefix string, n int) []string {
	var out []string
	for i := range n {
		out = append(out, fmt.Sprintf("%s, a prompt of the session with the number %d in it", prefix, i))
	}
	return out
}

// The sessions are embedded in their window and not outside it, where the other
// kinds go on all the same.
func TestJobEmbedsSessionsOnlyInTheirWindow(t *testing.T) {
	f := newFixture(t)
	f.addSession(t, "s", numbered("night work", 6)...)
	f.addRequest(t, "ein Wunsch")
	clk := &stepClock{t: time.Date(2099, 1, 1, 12, 0, 0, 0, time.UTC)}
	cfg := Config{Batch: 4, Kinds: []string{store.KindRequest, store.KindSession}, SessionWindow: Window{Start: 0, End: 6 * 60}}
	j := f.job(cfg, clk)

	// At noon: the request is embedded, the sessions are not asked for.
	res, err := j.Step(context.Background())
	if err != nil || res.Kind != store.KindRequest || res.Embedded != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if res, err = j.Step(context.Background()); err != nil || res != (Result{}) {
		t.Fatalf("at noon the sessions wait: %+v %v", res, err)
	}
	if got := f.srv.Inputs(); got != 1 {
		t.Fatalf("%d texts reached the server at noon", got)
	}
	if s := f.sessionStatus(t); s.Current != 0 || s.Due != 6 {
		t.Fatalf("%+v", s)
	}

	// At three in the morning they are, a batch at a time.
	clk.t = time.Date(2099, 1, 2, 3, 0, 0, 0, time.UTC)
	res, err = j.Step(context.Background())
	if err != nil || res.Kind != store.KindSession || res.Embedded != 4 {
		t.Fatalf("%+v %v", res, err)
	}
	if res, err = j.Step(context.Background()); err != nil || res.Kind != store.KindSession || res.Embedded != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if res, err = j.Step(context.Background()); err != nil || res != (Result{}) {
		t.Fatalf("%+v %v", res, err)
	}
	if s := f.sessionStatus(t); s.Current != 6 || s.Due != 0 {
		t.Fatalf("%+v", s)
	}
	// The window closes at six: a new text waits for the next night.
	f.addSession(t, "later", numbered("a late one", 1)...)
	clk.t = time.Date(2099, 1, 2, 6, 0, 0, 0, time.UTC)
	if res, err = j.Step(context.Background()); err != nil || res != (Result{}) {
		t.Fatalf("at six the window is closed: %+v %v", res, err)
	}
	clk.t = time.Date(2099, 1, 3, 0, 0, 0, 0, time.UTC)
	if res, err = j.Step(context.Background()); err != nil || res.Kind != store.KindSession || res.Embedded != 1 {
		t.Fatalf("%+v %v", res, err)
	}
}

// By default the window is the night, from midnight to six; "always" is no
// window.
func TestJobSessionWindowDefaultsToTheNightAndCanBeAlways(t *testing.T) {
	if got := (Config{}).withDefaults().SessionWindow; got != DefaultSessionWindow || got.String() != "00:00-06:00" {
		t.Fatalf("%+v", got)
	}
	f := newFixture(t)
	f.addSession(t, "s", numbered("any time", 2)...)
	clk := &stepClock{t: time.Date(2099, 1, 1, 12, 0, 0, 0, time.UTC)}
	j := f.job(Config{Batch: 8, Kinds: []string{store.KindSession}}, clk)
	if res, err := j.Step(context.Background()); err != nil || res != (Result{}) {
		t.Fatalf("by default noon is outside the window: %+v %v", res, err)
	}
	j = f.job(Config{Batch: 8, Kinds: []string{store.KindSession}, SessionWindow: Window{Always: true}}, clk)
	if res, err := j.Step(context.Background()); err != nil || res.Embedded != 2 {
		t.Fatalf("%+v %v", res, err)
	}
}

// Every other kind goes first: the sessions are asked only when nothing else is
// due, so a request that arrives while sessions are being caught up on waits for
// the batch that is running and no longer.
func TestJobServesSessionsAfterEveryOtherKind(t *testing.T) {
	f := newFixture(t)
	f.addSession(t, "s", numbered("many", 12)...)
	f.addRequest(t, "ein Wunsch")
	f.addPain(t, "ein Schmerz")
	clk := &stepClock{t: time.Date(2099, 1, 1, 3, 0, 0, 0, time.UTC)}
	// The sessions are listed first, which must not matter.
	j := f.job(Config{Batch: 4, Kinds: []string{store.KindSession, store.KindRequest, store.KindPain}, SessionWindow: Window{Always: true}}, clk)
	var order []string
	step := func() {
		res, err := j.Step(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		order = append(order, fmt.Sprintf("%s:%d", res.Kind, res.Embedded))
	}
	step()
	step()
	step() // the first batch of sessions
	f.addRequest(t, "noch ein Wunsch")
	step() // the next batch is the request, not the second batch of sessions
	step()
	step()
	if got := strings.Join(order, " "); got != "request:1 pain:1 session:4 request:1 session:4 session:4" {
		t.Fatalf("order of the batches: %s", got)
	}
	if res, err := j.Step(context.Background()); err != nil || res != (Result{}) {
		t.Fatalf("%+v %v", res, err)
	}
}

// A text on which the server fails is parked, the others of its batch are stored
// and the sessions go on; the session of the parked text is looked at again when
// its time comes.
func TestJobParksATextOfASession(t *testing.T) {
	f := newFixture(t)
	f.addSession(t, "s", "a fine prompt about s1 and the cache, nothing special", "this one makes the model crash POISON for good", "another fine prompt about the cache layer")
	f.srv.FailOn("POISON")
	clk := newStepClock()
	j := f.job(Config{Batch: 8, Kinds: []string{store.KindSession}, SessionWindow: Window{Always: true}}, clk)
	ctx := context.Background()
	if _, err := j.Step(ctx); err == nil {
		t.Fatal("the first failure of a batch is an error")
	}
	res, err := j.Step(ctx)
	if err != nil || res.Kind != store.KindSession || res.Embedded != 2 || res.Skipped != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	var kind int
	if err := f.st.DB().QueryRow(`SELECT count(*) FROM session_embedding_failures`).Scan(&kind); err != nil || kind != 1 {
		t.Fatalf("%d failures %v", kind, err)
	}
	if s := f.sessionStatus(t); s.Current != 2 || s.Parked != 1 || s.Due != 1 {
		t.Fatalf("%+v", s)
	}
	// Left alone, the parked text costs no request, and its session is not marked as done.
	calls := f.srv.Calls()
	if res, err := j.Step(ctx); err != nil || res != (Result{}) || f.srv.Calls() != calls {
		t.Fatalf("%+v %v", res, err)
	}
	var marks int
	if err := f.st.DB().QueryRow(`SELECT count(*) FROM session_embedding_sync`).Scan(&marks); err != nil || marks != 0 {
		t.Fatalf("a session with a parked text was marked as done: %d", marks)
	}
	// After the time it was parked for, and with the server well, it is embedded.
	f.srv.FailOn("")
	clk.add(DefaultMaxPark + time.Hour)
	if res, err := j.Step(ctx); err != nil || res.Embedded != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if s := f.sessionStatus(t); s.Current != 3 || s.Due != 0 {
		t.Fatalf("%+v", s)
	}
}

// All the state is in the database: a job that stops and one that starts later go
// on where the first left off, and no text is embedded twice.
func TestJobResumesTheSessionsAfterAnInterruption(t *testing.T) {
	f := newFixture(t)
	f.addSession(t, "a", numbered("first", 5)...)
	f.addSession(t, "b", numbered("second", 5)...)
	cfg := Config{Batch: 4, Kinds: []string{store.KindSession}, SessionWindow: Window{Always: true}}
	first := f.job(cfg, nil)
	if res, err := first.Step(context.Background()); err != nil || res.Embedded != 4 {
		t.Fatalf("%+v %v", res, err)
	}
	second := f.job(cfg, nil)
	steps := drain(t, second)
	embedded := 0
	for _, s := range steps {
		embedded += s.Embedded
	}
	if embedded != 6 {
		t.Fatalf("the second job embedded %d texts, want the 6 that were left: %+v", embedded, steps)
	}
	var seen []string
	for _, r := range f.srv.Requests() {
		seen = append(seen, r...)
	}
	slices.Sort(seen)
	if len(slices.Compact(slices.Clone(seen))) != len(seen) || len(seen) != 10 {
		t.Fatalf("%d texts reached the server, 10 expected once each", len(seen))
	}
	if s := f.sessionStatus(t); s.Current != 10 || s.Due != 0 {
		t.Fatalf("%+v", s)
	}
}

// An embedding server that is away is the server's failure, not a text's: the job
// backs off like for every kind, nothing is parked or marked as done, and it goes
// on when the server is back.
func TestJobBacksOffWhileTheServerIsAwayAndGoesOnForSessions(t *testing.T) {
	f := newFixture(t)
	f.addSession(t, "s", numbered("away", 3)...)
	f.srv.Fail.Store(true)
	cfg := Config{Batch: 8, Pause: 2 * time.Second, Idle: 30 * time.Second, StartDelay: -1, Kinds: []string{store.KindSession}, SessionWindow: Window{Always: true}}
	j := New(f.st, f.c, cfg, quiet())
	waits := runUntil(t, j, func(s []time.Duration) bool { return len(s) >= 3 }, func(n int) {
		if n == 3 {
			f.srv.Fail.Store(false)
		}
	})
	if waits[1] != DefaultInitialBackoff || waits[2] != 2*DefaultInitialBackoff {
		t.Fatalf("back-off while the server fails: %v", waits)
	}
	var failures, marks int
	if err := f.st.DB().QueryRow(`SELECT (SELECT count(*) FROM session_embedding_failures), (SELECT count(*) FROM session_embedding_sync)`).Scan(&failures, &marks); err != nil || failures != 0 || marks != 0 {
		t.Fatalf("a server that is away parked %d texts and marked %d sessions: %v", failures, marks, err)
	}
	if s := f.sessionStatus(t); s.Current != 0 || s.Due != 3 {
		t.Fatalf("%+v", s)
	}
	steps := drain(t, j)
	embedded := 0
	for _, s := range steps {
		embedded += s.Embedded
	}
	if embedded != 3 {
		t.Fatalf("%+v", steps)
	}
}

// The newest session comes first, so that what was written last is searchable
// first while the stock is still being caught up on.
func TestJobEmbedsTheNewestSessionFirst(t *testing.T) {
	f := newFixture(t)
	f.addSession(t, "old", "an old session, with a prompt that is long enough")
	f.addSession(t, "new", "a new session, with a prompt that is long enough")
	if _, err := f.st.DB().Exec(`UPDATE sessions SET last_seen_at = '2026-01-01T00:00:00Z' WHERE external_id = 'old'`); err != nil {
		t.Fatal(err)
	}
	j := f.job(Config{Batch: 1, Kinds: []string{store.KindSession}, SessionWindow: Window{Always: true}}, nil)
	if res, err := j.Step(context.Background()); err != nil || res.Embedded != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if got := f.srv.Requests(); len(got) != 1 || !strings.Contains(got[0][0], "a new session") {
		t.Fatalf("%q", got)
	}
}
