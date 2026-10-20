package embedjob

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embed/embedtest"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fixture struct {
	st  *store.Store
	srv *embedtest.Server
	c   *embed.Client
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := embedtest.New(t)
	c, err := embed.New(embed.Config{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{st, srv, c}
}

func (f fixture) add(t *testing.T, n int) []int64 {
	t.Helper()
	var ids []int64
	for i := range n {
		id, err := f.st.InsertKnowledge(store.Knowledge{Type: "note", Title: fmt.Sprintf("entry %d", i), Body: fmt.Sprintf("body of entry %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func (f fixture) addBody(t *testing.T, title, body string) int64 {
	t.Helper()
	id, err := f.st.InsertKnowledge(store.Knowledge{Type: "note", Title: title, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f fixture) status(t *testing.T) store.EmbedStatus {
	t.Helper()
	s, err := f.st.KnowledgeEmbeddingStatus(store.EmbedModel{Model: f.c.Model(), PrefixVersion: f.c.PrefixVersion()})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// clock replaces the sleeping of a job: it records the waits and ends the run
// when stop says so.
type clock struct {
	mu     sync.Mutex
	slept  []time.Duration
	stop   func(slept []time.Duration) bool
	hook   func(n int)
	cancel context.CancelFunc
}

func (c *clock) sleep(ctx context.Context, d time.Duration) bool {
	c.mu.Lock()
	c.slept = append(c.slept, d)
	n := len(c.slept)
	stop := c.stop != nil && c.stop(append([]time.Duration(nil), c.slept...))
	c.mu.Unlock()
	if c.hook != nil {
		c.hook(n)
	}
	if stop {
		c.cancel()
		return false
	}
	return ctx.Err() == nil
}

func (c *clock) waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.slept...)
}

// now is a clock that only moves when the job waits.
func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for _, d := range c.slept {
		t = t.Add(d)
	}
	return t
}

func runUntil(t *testing.T, j *Job, stop func(slept []time.Duration) bool, hook func(n int)) []time.Duration {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	clk := &clock{stop: stop, hook: hook, cancel: cancel}
	j.sleep, j.now = clk.sleep, clk.now
	done := make(chan struct{})
	go func() { defer close(done); j.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(25 * time.Second):
		t.Fatal("the job did not stop")
	}
	return clk.waits()
}

func idleReached(idle time.Duration) func([]time.Duration) bool {
	return func(slept []time.Duration) bool { return slept[len(slept)-1] == idle }
}

// stepClock makes the time of a job a variable of the test.
type stepClock struct{ t time.Time }

func (c *stepClock) now() time.Time      { return c.t }
func (c *stepClock) add(d time.Duration) { c.t = c.t.Add(d) }
func newStepClock() *stepClock           { return &stepClock{time.Now().UTC().Truncate(time.Second)} }
func (f fixture) job(cfg Config, c *stepClock) *Job {
	j := New(f.st, f.c, cfg, quiet())
	if c != nil {
		j.now = c.now
	}
	return j
}

func TestJobEmbedsInSmallBatchesOneRequestAtATimeWithPauses(t *testing.T) {
	f := newFixture(t)
	f.add(t, 20)
	f.srv.SetDelay(5 * time.Millisecond)
	cfg := Config{Batch: 8, Pause: 2 * time.Second, Idle: 30 * time.Second, StartDelay: 7 * time.Second}
	j := New(f.st, f.c, cfg, quiet())
	waits := runUntil(t, j, idleReached(30*time.Second), nil)

	want := []time.Duration{7 * time.Second, 2 * time.Second, 2 * time.Second, 2 * time.Second, 30 * time.Second}
	if fmt.Sprint(waits) != fmt.Sprint(want) {
		t.Fatalf("waits %v, want start delay, a pause after each of three batches, then idle: %v", waits, want)
	}
	var sizes []int
	for _, r := range f.srv.Requests() {
		sizes = append(sizes, len(r))
	}
	if fmt.Sprint(sizes) != "[8 8 4]" {
		t.Fatalf("batch sizes %v, want [8 8 4]", sizes)
	}
	if f.srv.Peak() != 1 {
		t.Fatalf("%d requests at once, want 1", f.srv.Peak())
	}
	if s := f.status(t); s.Active != 20 || s.Current != 20 || s.Due != 0 {
		t.Fatalf("status %+v", s)
	}
}

func TestJobResumesWhereTheLastRunStopped(t *testing.T) {
	f := newFixture(t)
	f.add(t, 12)
	first := New(f.st, f.c, Config{Batch: 5}, quiet())
	if res, err := first.Step(context.Background()); err != nil || res.Embedded != 5 {
		t.Fatalf("first step: %+v %v", res, err)
	}
	if s := f.status(t); s.Current != 5 || s.Due != 7 {
		t.Fatalf("status %+v", s)
	}
	// A new process knows nothing but the table.
	second := New(f.st, f.c, Config{Batch: 5}, quiet())
	runUntil(t, second, idleReached(DefaultIdle), nil)
	if got := f.srv.Inputs(); got != 12 {
		t.Fatalf("the server embedded %d texts for 12 entries; nothing may be done twice", got)
	}
	if s := f.status(t); s.Current != 12 || s.Due != 0 {
		t.Fatalf("status %+v", s)
	}
}

type otherFormat struct{ *embed.Client }

func (otherFormat) PrefixVersion() string { return "next" }

func TestJobEmbedsAgainWhenTheTextTheModelOrTheFormatChanges(t *testing.T) {
	f := newFixture(t)
	ids := f.add(t, 6)
	runUntil(t, New(f.st, f.c, Config{}, quiet()), idleReached(DefaultIdle), nil)
	if f.srv.Inputs() != 6 {
		t.Fatalf("inputs %d", f.srv.Inputs())
	}

	if err := f.st.UpdateKnowledge(ids[2], map[string]string{"body": "a different body"}); err != nil {
		t.Fatal(err)
	}
	runUntil(t, New(f.st, f.c, Config{}, quiet()), idleReached(DefaultIdle), nil)
	if f.srv.Inputs() != 7 {
		t.Fatalf("an edited entry is embedded again, and only that one: %d", f.srv.Inputs())
	}
	last := f.srv.Requests()[len(f.srv.Requests())-1]
	if len(last) != 1 || !strings.Contains(last[0], "a different body") {
		t.Fatalf("last request %q", last)
	}

	other, err := embed.New(embed.Config{URL: f.srv.URL, Model: "another-model"})
	if err != nil {
		t.Fatal(err)
	}
	runUntil(t, New(f.st, other, Config{}, quiet()), idleReached(DefaultIdle), nil)
	if f.srv.Inputs() != 13 {
		t.Fatalf("a new model embeds everything again: %d", f.srv.Inputs())
	}
	if s, _ := f.st.KnowledgeEmbeddingStatus(store.EmbedModel{Model: "another-model", PrefixVersion: embed.PrefixVersion}); s.Current != 6 {
		t.Fatalf("status for the new model %+v", s)
	}

	runUntil(t, New(f.st, otherFormat{other}, Config{}, quiet()), idleReached(DefaultIdle), nil)
	if f.srv.Inputs() != 19 {
		t.Fatalf("a new text format embeds everything again: %d", f.srv.Inputs())
	}
	if s, _ := f.st.KnowledgeEmbeddingStatus(store.EmbedModel{Model: "another-model", PrefixVersion: "next"}); s.Current != 6 {
		t.Fatalf("status for the new format %+v", s)
	}
}

func TestJobLeavesInactiveEntriesAlone(t *testing.T) {
	f := newFixture(t)
	ids := f.add(t, 5)
	for status, id := range map[string]int64{"archived": ids[1], "stale": ids[2], "superseded": ids[3], "deprecated": ids[4]} {
		if _, err := f.st.DB().Exec(`UPDATE knowledge SET status=? WHERE id=?`, status, id); err != nil {
			t.Fatal(err)
		}
	}
	runUntil(t, New(f.st, f.c, Config{}, quiet()), idleReached(DefaultIdle), nil)
	if f.srv.Inputs() != 1 {
		t.Fatalf("only the active entry is embedded, got %d", f.srv.Inputs())
	}
	if s := f.status(t); s.Active != 1 || s.Current != 1 {
		t.Fatalf("status %+v", s)
	}
}

func TestBackoffDoublesUpToTheCap(t *testing.T) {
	c := Config{InitialBackoff: 5 * time.Second, MaxBackoff: 5 * time.Minute}
	var got []time.Duration
	for n := 1; n <= 9; n++ {
		got = append(got, c.Backoff(n))
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second,
		160 * time.Second, 300 * time.Second, 300 * time.Second, 300 * time.Second}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%v", got)
	}
	if d := (Config{}).Backoff(1); d != DefaultInitialBackoff {
		t.Fatalf("default %v", d)
	}
	if d := (Config{}).Backoff(50); d != DefaultMaxBackoff {
		t.Fatalf("default cap %v", d)
	}
	if got := (Config{}).ParkFor(1); got != DefaultPark {
		t.Fatalf("first park %v", got)
	}
	if got := (Config{}).ParkFor(2); got != 2*DefaultPark {
		t.Fatalf("second park %v", got)
	}
	if got := (Config{}).ParkFor(40); got != DefaultMaxPark {
		t.Fatalf("park cap %v", got)
	}
}

func TestJobBacksOffWhileTheServerFailsAndRecovers(t *testing.T) {
	f := newFixture(t)
	f.add(t, 3)
	f.srv.Fail.Store(true)
	cfg := Config{Batch: 8, Pause: 2 * time.Second, Idle: 30 * time.Second, StartDelay: -1}
	j := New(f.st, f.c, cfg, quiet())
	failing := func(n int) {
		if n == 7 { // after six waits the server comes back
			s := f.status(t)
			if s.Job.Failures != 6 || !strings.Contains(s.Job.LastError, "model crashed") || s.Job.LastErrorAt == "" {
				t.Errorf("the state of a failing job must say so: %+v", s.Job)
			}
			f.srv.Fail.Store(false)
		}
	}
	waits := runUntil(t, j, idleReached(30*time.Second), failing)
	// The first wait is the start delay (none); then 5s, 10s, ... doubling.
	want := []time.Duration{0, 5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 160 * time.Second,
		2 * time.Second, 30 * time.Second}
	if fmt.Sprint(waits) != fmt.Sprint(want) {
		t.Fatalf("waits %v, want %v", waits, want)
	}
	s := f.status(t)
	if s.Current != 3 || s.Job.Failures != 0 || s.Job.LastOKAt == "" {
		t.Fatalf("recovered: %+v", s)
	}
	if s.Job.LastError != "" {
		t.Fatalf("a recovered job drops its error: %+v", s.Job)
	}
	if s.Parked != 0 {
		t.Fatalf("a server that was down is not a reason to park entries: %+v", s)
	}
}

func TestJobStateKeepsTheLastErrorUntilSomethingWorks(t *testing.T) {
	f := newFixture(t)
	f.add(t, 1)
	f.srv.Fail.Store(true)
	j := New(f.st, f.c, Config{StartDelay: -1}, quiet())
	runUntil(t, j, func(s []time.Duration) bool { return len(s) >= 3 }, nil)
	s := f.status(t)
	if s.Job.Failures < 1 || s.Job.LastError == "" || s.Due != 1 {
		t.Fatalf("%+v", s)
	}
	if strings.Contains(s.Job.LastError, "entry 0") {
		t.Fatalf("the error must not hold entry text: %q", s.Job.LastError)
	}
}

func TestJobSkipsATextTheServerRefusesForGoodAndKeepsTheRest(t *testing.T) {
	f := newFixture(t)
	f.add(t, 3)
	long := f.addBody(t, "long", strings.Repeat("word ", 100))
	f.srv.Reject.Store(150)
	j := New(f.st, f.c, Config{Batch: 8}, quiet())
	res, err := j.Step(context.Background())
	if err != nil || res.Embedded != 3 || res.Skipped != 1 {
		t.Fatalf("step: %+v %v", res, err)
	}
	if s := f.status(t); s.Parked != 1 || s.Current != 3 {
		t.Fatalf("status %+v", s)
	}
	calls := f.srv.Calls()
	if res, err = j.Step(context.Background()); err != nil || res != (Result{}) {
		t.Fatalf("second step: %+v %v", res, err)
	}
	// A new process knows from the table.
	if res, err = New(f.st, f.c, Config{Batch: 8}, quiet()).Step(context.Background()); err != nil || res != (Result{}) {
		t.Fatalf("after a restart: %+v %v", res, err)
	}
	if f.srv.Calls() != calls {
		t.Fatal("a text the server refuses for good must not be sent again")
	}
	// It is tried again once it has changed.
	if err := f.st.UpdateKnowledge(long, map[string]string{"body": "short now"}); err != nil {
		t.Fatal(err)
	}
	if res, err = j.Step(context.Background()); err != nil || res.Embedded != 1 {
		t.Fatalf("after the edit: %+v %v", res, err)
	}
	if s := f.status(t); s.Parked != 0 || s.Current != 4 {
		t.Fatalf("status %+v", s)
	}

	// A single refused text is put aside without any isolating.
	other := newFixture(t)
	other.addBody(t, "alone", strings.Repeat("word ", 100))
	other.srv.Reject.Store(150)
	if res, err := New(other.st, other.c, Config{}, quiet()).Step(context.Background()); err != nil || res.Skipped != 1 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestParkedForGoodIsTriedAgainWithAnotherModel(t *testing.T) {
	f := newFixture(t)
	f.addBody(t, "long", strings.Repeat("word ", 100))
	f.srv.Reject.Store(150)
	if res, err := New(f.st, f.c, Config{}, quiet()).Step(context.Background()); err != nil || res.Skipped != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	f.srv.Reject.Store(0) // the server was started with a larger context
	other, err := embed.New(embed.Config{URL: f.srv.URL, Model: "another-model"})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := New(f.st, other, Config{}, quiet()).Step(context.Background()); err != nil || res.Embedded != 1 {
		t.Fatalf("a new model tries the text again: %+v %v", res, err)
	}
}

// One text on which the server fails (500) must not hold the whole backlog in
// back-off: after the second failure of the same batch the texts are sent one by
// one, the others are stored and the culprit is parked, for longer each time.
func TestJobParksTheTextTheServerFailsOnAndGoesOnWithTheRest(t *testing.T) {
	f := newFixture(t)
	f.add(t, 4)
	f.addBody(t, "poison entry", "this one makes the model crash POISON")
	f.srv.FailOn("POISON")
	clk := newStepClock()
	cfg := Config{Batch: 8, Park: 5 * time.Minute}
	j := f.job(cfg, clk)
	ctx := context.Background()

	if res, err := j.Step(ctx); err == nil || res != (Result{}) {
		t.Fatalf("the first failure of a batch is an error: %+v %v", res, err)
	}
	res, err := j.Step(ctx)
	if err != nil || res.Embedded != 4 || res.Skipped != 1 {
		t.Fatalf("the second attempt sends the texts one by one: %+v %v", res, err)
	}
	if s := f.status(t); s.Parked != 1 || s.Current != 4 || s.Due != 1 {
		t.Fatalf("status %+v", s)
	}
	attempts := func() int {
		var n int
		if err := f.st.DB().QueryRow(`SELECT attempts FROM knowledge_embedding_failures`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if attempts() != 1 {
		t.Fatalf("attempts %d", attempts())
	}
	calls := f.srv.Calls()
	if res, err := j.Step(ctx); err != nil || res != (Result{}) || f.srv.Calls() != calls {
		t.Fatalf("a parked entry is left alone: %+v %v", res, err)
	}

	// A restart knows it from the table, too.
	if res, err := f.job(cfg, clk).Step(ctx); err != nil || res != (Result{}) || f.srv.Calls() != calls {
		t.Fatalf("after a restart: %+v %v", res, err)
	}

	// Once the time is up it is tried again, with a new entry in the batch; it
	// fails again, and the wait doubles.
	clk.add(6 * time.Minute)
	f.addBody(t, "new entry", "a perfectly fine text")
	if _, err := j.Step(ctx); err == nil {
		t.Fatal("the batch with the poison text fails again")
	}
	if res, err := j.Step(ctx); err != nil || res.Embedded != 1 || res.Skipped != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if attempts() != 2 {
		t.Fatalf("attempts %d, want 2", attempts())
	}
	clk.add(6 * time.Minute) // less than the doubled wait
	calls = f.srv.Calls()
	if res, err := j.Step(ctx); err != nil || res != (Result{}) || f.srv.Calls() != calls {
		t.Fatalf("still parked for ten minutes: %+v %v", res, err)
	}

	// The server is fixed; the entry is embedded and its failure forgotten.
	f.srv.FailOn("")
	clk.add(10 * time.Minute)
	if res, err := j.Step(ctx); err != nil || res.Embedded != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	var left int
	if err := f.st.DB().QueryRow(`SELECT COUNT(*) FROM knowledge_embedding_failures`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("failure rows left: %d %v", left, err)
	}
	if s := f.status(t); s.Parked != 0 || s.Due != 0 {
		t.Fatalf("status %+v", s)
	}
}

func TestJobParksAnUnusableVectorToo(t *testing.T) {
	f := newFixture(t)
	f.add(t, 3)
	f.addBody(t, "poison entry", "the vector of this text is all zeros POISON")
	f.srv.ZeroOn("POISON")
	j := New(f.st, f.c, Config{Batch: 8}, quiet())
	ctx := context.Background()
	if _, err := j.Step(ctx); err == nil {
		t.Fatal("a zero vector is an error")
	}
	if res, err := j.Step(ctx); err != nil || res.Embedded != 3 || res.Skipped != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if s := f.status(t); s.Parked != 1 || s.Current != 3 {
		t.Fatalf("status %+v", s)
	}
}

// When the server answers none of the texts it is the server, and nobody is
// parked for it.
func TestJobDoesNotParkAnythingWhileTheServerIsDown(t *testing.T) {
	f := newFixture(t)
	f.add(t, 4)
	f.srv.Fail.Store(true)
	j := New(f.st, f.c, Config{Batch: 8}, quiet())
	for range 4 {
		if res, err := j.Step(context.Background()); err == nil || res != (Result{}) {
			t.Fatalf("%+v %v", res, err)
		}
	}
	if s := f.status(t); s.Parked != 0 || s.Due != 4 {
		t.Fatalf("status %+v", s)
	}
	f.srv.Fail.Store(false)
	if res, err := j.Step(context.Background()); err != nil || res.Embedded != 4 {
		t.Fatalf("%+v %v", res, err)
	}
}

// A URL that answers 404 is a configuration mistake. It must back the job off
// and leave the entries due, not write all of them off one by one.
func TestJobTreatsAWrongURLAsAFailureAndNotAsRefusedTexts(t *testing.T) {
	f := newFixture(t)
	f.add(t, 4)
	wrong := httptest.NewServer(http.NotFoundHandler())
	defer wrong.Close()
	c, err := embed.New(embed.Config{URL: wrong.URL})
	if err != nil {
		t.Fatal(err)
	}
	j := New(f.st, c, Config{}, quiet())
	for range 3 {
		if res, err := j.Step(context.Background()); err == nil || res != (Result{}) {
			t.Fatalf("a 404 must be an error of the step: %+v %v", res, err)
		}
	}
	if s := f.status(t); s.Parked != 0 || s.Due != 4 {
		t.Fatalf("status %+v", s)
	}
}

// An edit that does not touch the text (a change of confidence) leaves the
// vector right; the job notes that it looked, without asking the server, and
// does not look at the entry again.
func TestJobDoesNotAskTheServerAboutAnEditThatLeftTheTextAlone(t *testing.T) {
	f := newFixture(t)
	ids := f.add(t, 3)
	if _, err := f.st.DB().Exec(`UPDATE knowledge SET updated_at='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	j := New(f.st, f.c, Config{}, quiet())
	for {
		res, err := j.Step(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if res == (Result{}) {
			break
		}
	}
	// The entry was edited after its vector was made.
	db := f.st.DB()
	if _, err := db.Exec(`UPDATE knowledge_embeddings SET read_at='2019-01-01T00:00:00Z' WHERE knowledge_id=?`, ids[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE knowledge SET confidence='verified', updated_at='2020-01-01T00:00:00Z' WHERE id=?`, ids[1]); err != nil {
		t.Fatal(err)
	}
	m := store.EmbedModel{Model: f.c.Model(), PrefixVersion: f.c.PrefixVersion()}
	due, err := f.st.KnowledgeEmbeddingDue(m, 8, time.Now(), false)
	if err != nil || len(due.Items) != 0 || len(due.Unchanged) != 1 || due.Unchanged[0] != ids[1] {
		t.Fatalf("the edited entry is looked at: %+v %v", due, err)
	}
	calls := f.srv.Calls()
	if res, err := j.Step(context.Background()); err != nil || res != (Result{}) {
		t.Fatalf("%+v %v", res, err)
	}
	if f.srv.Calls() != calls {
		t.Fatal("the text did not change, the server is not asked")
	}
	if due, err = f.st.KnowledgeEmbeddingDue(m, 8, time.Now(), false); err != nil || len(due.Unchanged) != 0 || len(due.Items) != 0 {
		t.Fatalf("the entry is looked at again: %+v %v", due, err)
	}
}

// A text that changed in a way updated_at does not show is found by the first
// look after a start, which goes through every entry, and not by the later ones.
func TestJobFindsASilentRewriteInTheFirstLookOnly(t *testing.T) {
	f := newFixture(t)
	ids := f.add(t, 3)
	if _, err := f.st.DB().Exec(`UPDATE knowledge SET updated_at='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	j := New(f.st, f.c, Config{}, quiet())
	if res, err := j.Step(context.Background()); err != nil || res.Embedded != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	if res, err := j.Step(context.Background()); err != nil || res != (Result{}) {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := f.st.DB().Exec(`UPDATE knowledge SET body='rewritten behind the back' WHERE id=?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if res, err := j.Step(context.Background()); err != nil || res != (Result{}) {
		t.Fatalf("a running job does not read every text again: %+v %v", res, err)
	}
	if res, err := New(f.st, f.c, Config{}, quiet()).Step(context.Background()); err != nil || res.Embedded != 1 {
		t.Fatalf("a new start does: %+v %v", res, err)
	}
}

// An entry edited while its vector is being computed is not lost: the vector
// carries the time the text was read, and the edit is at or after it.
func TestAnEditDuringTheComputationIsNotLost(t *testing.T) {
	f := newFixture(t)
	ids := f.add(t, 1)
	m := store.EmbedModel{Model: f.c.Model(), PrefixVersion: f.c.PrefixVersion()}
	due, err := f.st.KnowledgeEmbeddingDue(m, 8, time.Now(), true)
	if err != nil || len(due.Items) != 1 {
		t.Fatalf("%+v %v", due, err)
	}
	// The text changes after it was read and before the vector is stored.
	if err := f.st.UpdateKnowledge(ids[0], map[string]string{"body": "edited in between"}); err != nil {
		t.Fatal(err)
	}
	vecs, err := f.c.Documents(context.Background(), []string{due.Items[0].Text})
	if err != nil {
		t.Fatal(err)
	}
	q, scale, err := embed.Quantize(vecs[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.UpsertKnowledgeEmbedding(store.KnowledgeEmbedding{KnowledgeID: ids[0], Model: m.Model, PrefixVersion: m.PrefixVersion,
		TextHash: due.Items[0].Hash, Dim: len(q), Scale: float64(scale), Vec: q, ReadAt: due.ReadAt}); err != nil {
		t.Fatal(err)
	}
	again, err := f.st.KnowledgeEmbeddingDue(m, 8, time.Now(), false)
	if err != nil || len(again.Items) != 1 || !strings.Contains(again.Items[0].Text, "edited in between") {
		t.Fatalf("the edit was lost: %+v %v", again, err)
	}
}

func TestJobStopsPromptlyAndDoesNotCountItAsAFailure(t *testing.T) {
	f := newFixture(t)
	f.add(t, 2)
	// Waiting out the start delay ends with the context.
	j := New(f.st, f.c, Config{StartDelay: time.Hour}, quiet())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); j.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return from its start delay")
	}
	if f.srv.Calls() != 0 {
		t.Fatal("the job asked the server during its start delay")
	}

	// A request in flight ends with the context, too.
	f.srv.Hang.Store(true)
	j = New(f.st, f.c, Config{StartDelay: -1, Timeout: time.Hour}, quiet())
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() { defer close(done); j.Run(ctx) }()
	if !embedtest.Wait(ctx, 2*time.Second, func() bool { return f.srv.Calls() > 0 }) {
		t.Fatal("the job never asked")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return from a hanging request")
	}
	if s := f.status(t); s.Job.Failures != 0 || s.Job.LastError != "" {
		t.Fatalf("shutting down is not a failure: %+v", s.Job)
	}
}

func TestConfigDefaults(t *testing.T) {
	c := Config{}.withDefaults()
	if c.Batch != 8 || c.Pause != 2*time.Second || c.Idle != 30*time.Second || c.StartDelay != 10*time.Second ||
		c.InitialBackoff != 5*time.Second || c.MaxBackoff != 5*time.Minute || c.Timeout != embed.DefaultBatchTimeout ||
		c.Park != 5*time.Minute || c.MaxPark != 6*time.Hour {
		t.Fatalf("%+v", c)
	}
	if c := (Config{StartDelay: -1}).withDefaults(); c.StartDelay != 0 {
		t.Fatalf("a negative start delay means none: %v", c.StartDelay)
	}
}
