package embedjob

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func (f fixture) model() store.EmbedModel {
	return store.EmbedModel{Model: f.c.Model(), PrefixVersion: f.c.PrefixVersion()}
}

func (f fixture) kindStatus(t *testing.T, kind string) store.EmbedStatus {
	t.Helper()
	s, err := f.st.EmbeddingStatus(kind, f.model())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f fixture) addRequest(t *testing.T, title string) {
	t.Helper()
	if _, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: title, Description: "wagen kaputt"}}); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) addPain(t *testing.T, title string) {
	t.Helper()
	if _, err := f.st.DB().Exec(`INSERT INTO pains(project,title,body,state,created_at,updated_at) VALUES('p',?,'text','active','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, title); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) addGhost(t *testing.T, path string) {
	t.Helper()
	if _, err := f.st.PutGhostFile(store.GhostFile{Project: "p", Path: path, Kind: "file", Description: "macht etwas mit " + path}); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) addDocument(t *testing.T, slug string) store.Document {
	t.Helper()
	d, err := f.st.CreateDocument(store.Document{Project: "p", Slug: slug, Kind: "report", Title: slug}, "ein Bericht ueber "+slug, "m")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func drain(t *testing.T, j *Job) (steps []Result) {
	t.Helper()
	for range 500 {
		res, err := j.Step(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if res == (Result{}) {
			return steps
		}
		steps = append(steps, res)
	}
	t.Fatal("the job does not come to rest")
	return nil
}

// The job embeds the kinds it is given, all of them, and nothing else: with no
// setting it is knowledge only, as before there were other kinds.
func TestJobEmbedsTheKindsItIsGivenAndOnlyThose(t *testing.T) {
	f := newFixture(t)
	f.add(t, 2)
	f.addRequest(t, "ein Wunsch")
	f.addPain(t, "ein Schmerz")
	f.addGhost(t, "a/b.go")
	f.addDocument(t, "bericht")

	drain(t, New(f.st, f.c, Config{Batch: 8}, quiet()))
	if got := f.srv.Inputs(); got != 2 {
		t.Fatalf("by default only the %d entries are embedded, not %d texts", 2, got)
	}
	for _, kind := range []string{store.KindRequest, store.KindPain, store.KindDocument, store.KindGhost} {
		if s := f.kindStatus(t, kind); s.Current != 0 || s.Due != 1 {
			t.Fatalf("%s: %+v", kind, s)
		}
	}

	steps := drain(t, New(f.st, f.c, Config{Batch: 8, Kinds: store.EmbedKinds()}, quiet()))
	seen := map[string]int{}
	for _, s := range steps {
		seen[s.Kind] += s.Embedded
	}
	for _, kind := range []string{store.KindRequest, store.KindPain, store.KindDocument, store.KindGhost} {
		if seen[kind] != 1 {
			t.Fatalf("steps %+v: %s embedded %d", steps, kind, seen[kind])
		}
		if s := f.kindStatus(t, kind); s.Active != 1 || s.Current != 1 || s.Due != 0 {
			t.Fatalf("%s: %+v", kind, s)
		}
	}
	if seen[store.KindKnowledge] != 0 {
		t.Fatalf("the entries were embedded already: %+v", steps)
	}

	// An object added later comes by itself, in the idle look.
	f.addRequest(t, "noch ein Wunsch")
	j := New(f.st, f.c, Config{Batch: 8, Kinds: store.EmbedKinds()}, quiet())
	if steps := drain(t, j); len(steps) != 1 || steps[0].Kind != store.KindRequest || steps[0].Embedded != 1 {
		t.Fatalf("%+v", steps)
	}
}

// A kind with thousands of objects to catch up on does not make a small one wait
// for the end of it: the kinds are served in turn, one batch each.
func TestJobServesTheKindsInTurnSoABigKindDoesNotStarveASmallOne(t *testing.T) {
	f := newFixture(t)
	for i := range 40 {
		f.addGhost(t, fmt.Sprintf("dir/file%02d.go", i))
	}
	f.addPain(t, "ein Schmerz")
	f.addRequest(t, "ein Wunsch")
	j := New(f.st, f.c, Config{Batch: 4, Kinds: []string{store.KindGhost, store.KindPain, store.KindRequest}}, quiet())
	var order []string
	for range 3 {
		res, err := j.Step(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		order = append(order, fmt.Sprintf("%s:%d", res.Kind, res.Embedded))
	}
	if got := strings.Join(order, " "); got != "ghost:4 pain:1 request:1" {
		t.Fatalf("the first three batches: %s", got)
	}
	if s := f.kindStatus(t, store.KindGhost); s.Current != 4 || s.Due != 36 {
		t.Fatalf("the big kind has only had its turn: %+v", s)
	}
	// A kind with nothing due is passed over in the same step, not waited for.
	res, err := j.Step(context.Background())
	if err != nil || res.Kind != store.KindGhost || res.Embedded != 4 {
		t.Fatalf("%+v %v", res, err)
	}
	if steps := drain(t, j); len(steps) != 8 {
		t.Fatalf("the other 32 ghosts take eight batches: %d", len(steps))
	}
}

// The pace is the same for every kind: a pause after each batch, one request at a
// time, and the back-off when the server fails.
func TestJobKeepsOnePaceAcrossKinds(t *testing.T) {
	f := newFixture(t)
	f.add(t, 3)
	f.addRequest(t, "ein Wunsch")
	f.addPain(t, "ein Schmerz")
	for i := range 3 {
		f.addGhost(t, fmt.Sprintf("g%d.go", i))
	}
	f.srv.SetDelay(2 * time.Millisecond)
	cfg := Config{Batch: 2, Pause: 2 * time.Second, Idle: 30 * time.Second, StartDelay: -1, Kinds: store.EmbedKinds()}
	j := New(f.st, f.c, cfg, quiet())
	waits := runUntil(t, j, idleReached(30*time.Second), nil)
	// No start delay, then a pause after each of the six batches (knowledge 2+1,
	// request 1, pain 1, ghost 2+1), then idle.
	want := []time.Duration{0, 2 * time.Second, 2 * time.Second, 2 * time.Second, 2 * time.Second, 2 * time.Second, 2 * time.Second, 30 * time.Second}
	if fmt.Sprint(waits) != fmt.Sprint(want) {
		t.Fatalf("waits %v, want %v", waits, want)
	}
	if f.srv.Peak() != 1 {
		t.Fatalf("%d requests at once", f.srv.Peak())
	}

	f.srv.Fail.Store(true)
	f.addGhost(t, "later.go")
	j = New(f.st, f.c, Config{Batch: 2, Pause: 2 * time.Second, Idle: 30 * time.Second, StartDelay: -1, Kinds: store.EmbedKinds()}, quiet())
	waits = runUntil(t, j, func(s []time.Duration) bool { return len(s) >= 4 }, nil)
	if waits[1] != DefaultInitialBackoff || waits[2] != 2*DefaultInitialBackoff {
		t.Fatalf("back-off while the server fails: %v", waits)
	}
}

// A text the server fails on is parked in its kind, and the other kinds go on.
func TestJobParksATextOfOneKindAndGoesOnWithTheOthers(t *testing.T) {
	f := newFixture(t)
	f.addGhost(t, "ok.go")
	if _, err := f.st.PutGhostFile(store.GhostFile{Project: "p", Path: "poison.go", Kind: "file", Description: "this one makes the model crash POISON"}); err != nil {
		t.Fatal(err)
	}
	f.addDocument(t, "bericht")
	f.srv.FailOn("POISON")
	clk := newStepClock()
	j := f.job(Config{Batch: 8, Kinds: []string{store.KindGhost, store.KindDocument}}, clk)
	ctx := context.Background()
	if _, err := j.Step(ctx); err == nil {
		t.Fatal("the first failure of a batch is an error")
	}
	res, err := j.Step(ctx)
	if err != nil || res.Kind != store.KindGhost || res.Embedded != 1 || res.Skipped != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if res, err = j.Step(ctx); err != nil || res.Kind != store.KindDocument || res.Embedded != 1 {
		t.Fatalf("the document comes after the ghost batch: %+v %v", res, err)
	}
	if s := f.kindStatus(t, store.KindGhost); s.Parked != 1 || s.Current != 1 {
		t.Fatalf("%+v", s)
	}
	var kind string
	if err := f.st.DB().QueryRow(`SELECT kind FROM embedding_failures`).Scan(&kind); err != nil || kind != store.KindGhost {
		t.Fatalf("%q %v", kind, err)
	}
	// Nothing more is due; the parked text is left alone.
	calls := f.srv.Calls()
	if res, err := j.Step(ctx); err != nil || res != (Result{}) || f.srv.Calls() != calls {
		t.Fatalf("%+v %v", res, err)
	}
}

// When nothing is due the job removes the vectors of what is gone or archived.
func TestJobRemovesTheVectorsOfArchivedDocumentsWhenIdle(t *testing.T) {
	f := newFixture(t)
	keep := f.addDocument(t, "bleibt")
	gone := f.addDocument(t, "geht")
	clk := newStepClock()
	j := f.job(Config{Batch: 8, Kinds: []string{store.KindDocument}}, clk)
	drain(t, j)
	count := func() int {
		var n int
		if err := f.st.DB().QueryRow(`SELECT COUNT(*) FROM embeddings WHERE kind='document'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count() != 2 {
		t.Fatalf("%d vectors", count())
	}
	if err := f.st.PatchDocument(gone.ID, map[string]string{"status": "archived"}); err != nil {
		t.Fatal(err)
	}
	// Not at once, and not before the interval is up: housekeeping, not a hot path.
	if _, err := j.Step(context.Background()); err != nil || count() != 2 {
		t.Fatalf("%d vectors right after", count())
	}
	clk.add(purgeEvery + time.Second)
	if _, err := j.Step(context.Background()); err != nil || count() != 1 {
		t.Fatalf("%d vectors after the interval", count())
	}
	if s := f.kindStatus(t, store.KindDocument); s.Active != 1 || s.Current != 1 {
		t.Fatalf("%+v", s)
	}
	_ = keep
}

func TestConfigDefaultsToKnowledgeOnly(t *testing.T) {
	if got := (Config{}).withDefaults().Kinds; len(got) != 1 || got[0] != store.KindKnowledge {
		t.Fatalf("%v", got)
	}
	if got := (Config{Kinds: []string{store.KindGhost}}).withDefaults().Kinds; len(got) != 1 || got[0] != store.KindGhost {
		t.Fatalf("%v", got)
	}
}
