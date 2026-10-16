package explain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
)

// ----------------------------------------------------------- fake batch records

type fakeBatch struct {
	OpenBatch
	state string
}

type fakeBatchItem struct {
	BatchItem
	batch     int64
	state     string
	errText   string
	updatedAt time.Time
}

func (f *fakeStore) RecordExplainBatch(providerID, model, language string, items []BatchItem) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.items == nil {
		f.items = map[string]*fakeBatchItem{}
	}
	id := int64(len(f.batches) + 1)
	f.batches = append(f.batches, &fakeBatch{OpenBatch: OpenBatch{ID: id, ProviderID: providerID, Model: model, Language: language}, state: "open"})
	for _, it := range items {
		f.items[fmt.Sprintf("%d/%s", id, it.CustomID)] = &fakeBatchItem{BatchItem: it, batch: id, state: "pending", updatedAt: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	}
	return id, nil
}

func (f *fakeStore) ReserveExplainBatch(model, language string, items []BatchItem) (int64, error) {
	f.mu.Lock()
	for _, it := range items {
		if f.blocked(it.Kind, it.ObjectID, it.Hash, "9999-01-01T00:00:00Z") {
			f.mu.Unlock()
			return 0, ErrBatchTaken
		}
	}
	f.mu.Unlock()
	id, err := f.RecordExplainBatch("submitting", model, language, items)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches[id-1].state = "submitting"
	f.batches[id-1].State = "submitting"
	return id, err
}

func (f *fakeStore) ActivateExplainBatch(batchID int64, providerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.batches[batchID-1]
	b.ProviderID, b.state, b.State = providerID, "open", "open"
	return nil
}

func (f *fakeStore) OpenExplainBatches() ([]OpenBatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []OpenBatch
	for _, b := range f.batches {
		if b.state != "open" && b.state != "submitting" {
			continue
		}
		o := b.OpenBatch
		o.State = b.state
		for _, it := range f.items {
			if it.batch == b.ID && it.state == "pending" {
				o.Pending++
			}
		}
		out = append(out, o)
	}
	return out, nil
}

func (f *fakeStore) PendingExplainBatchItems(batchID int64) ([]BatchItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []BatchItem
	for _, it := range f.items {
		if it.batch == batchID && it.state == "pending" {
			out = append(out, it.BatchItem)
		}
	}
	return out, nil
}

func (f *fakeStore) FinishExplainBatchItem(batchID int64, customID, state, errText, day string, u Usage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	it := f.items[fmt.Sprintf("%d/%s", batchID, customID)]
	if it == nil || it.state != "pending" {
		return nil
	}
	it.state, it.errText, it.updatedAt = state, errText, time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	cur := f.usage[day]
	cur.Day = day
	f.usage[day] = cur.Add(u)
	return nil
}

func (f *fakeStore) CloseExplainBatch(batchID int64, state string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches[batchID-1].state = state
	return nil
}

func (f *fakeStore) blocked(kind string, id int64, hash, since string) bool {
	t, _ := time.Parse(time.RFC3339, since)
	for _, it := range f.items {
		if it.Kind != kind || it.ObjectID != id || it.Hash != hash {
			continue
		}
		if (it.state == "pending" && (f.batches[it.batch-1].state == "open" || f.batches[it.batch-1].state == "submitting")) || (it.state == "failed" && !it.updatedAt.Before(t)) {
			return true
		}
	}
	return false
}

func (f *fakeStore) ExplainBatchBlocked(since string) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]bool{}
	for _, it := range f.items {
		if f.blocked(it.Kind, it.ObjectID, it.Hash, since) {
			out[BatchKey(it.Kind, it.ObjectID, it.Hash)] = true
		}
	}
	return out, nil
}

func (f *fakeStore) ExplainBatched(kind string, id int64, hash, since string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blocked(kind, id, hash, since), nil
}

// ------------------------------------------------------------------ fake client

type fakeBatchClient struct {
	mu         sync.Mutex
	submitted  [][]llm.BatchRequest
	done       bool
	results    map[string]llm.BatchResult
	submitErr  error
	collects   int
	statusErr  error
	collectErr error
	onSubmit   func()
}

func (c *fakeBatchClient) SubmitBatch(ctx context.Context, reqs []llm.BatchRequest) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.submitErr != nil {
		return "", c.submitErr
	}
	if c.onSubmit != nil {
		c.mu.Unlock()
		c.onSubmit()
		c.mu.Lock()
	}
	c.submitted = append(c.submitted, reqs)
	return fmt.Sprintf("msgbatch_%d", len(c.submitted)), nil
}

func (c *fakeBatchClient) BatchStatus(ctx context.Context, id string) (llm.BatchStatusReport, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.statusErr != nil {
		return llm.BatchStatusReport{}, c.statusErr
	}
	return llm.BatchStatusReport{Done: c.done}, nil
}

func (c *fakeBatchClient) CollectBatch(ctx context.Context, id string) (map[string]llm.BatchResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.collects++
	if c.collectErr != nil {
		return nil, c.collectErr
	}
	return c.results, nil
}

func (c *fakeBatchClient) ok(kind string, id int64, in, out int) {
	if c.results == nil {
		c.results = map[string]llm.BatchResult{}
	}
	c.results[customID(kind, id)] = llm.BatchResult{Content: goodReply, PromptTokens: in, CompletionTokens: out}
}

func batchSvc(f *fakeStore, bc llm.BatchClient, lim Limits) *Service {
	return New(f, &fakeLLM{reply: goodReply}, Config{Language: "de", Model: "claude-haiku-5-5", Limits: lim, Metered: true, Batch: bc,
		Price: llm.Price{InUSD: 1, OutUSD: 5}, PriceKnown: true,
		Now: func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) }})
}

func queue(f *fakeStore, n int) {
	for i := int64(1); i <= int64(n); i++ {
		f.put(KindKnowledge, i, "p", "T", fmt.Sprintf("Text %d", i))
		f.cands = append(f.cands, Candidate{KindKnowledge, i})
	}
}

// ------------------------------------------------------------------------ tests

func TestSubmitSendsOneBatchInQueueOrderAndRecordsIt(t *testing.T) {
	f := newFakeStore()
	queue(f, 4)
	// 2 is current already, 3 has no project
	f.exp[key(KindKnowledge, 2)] = Explanation{Kind: KindKnowledge, ObjectID: 2, Text: "Schon da und aktuell erklärt.", Language: "de", Source: SourceGenerated, ContentHash: f.src[key(KindKnowledge, 2)].Hash}
	f.put(KindKnowledge, 3, "", "T", "ohne Projekt")
	bc := &fakeBatchClient{}
	s := batchSvc(f, bc, Limits{})
	rep, err := s.SubmitBatches(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Submitted != 2 || rep.Batches != 1 || rep.Fresh != 1 || rep.Stopped != "none" {
		t.Errorf("report %+v", rep)
	}
	if len(bc.submitted) != 1 || len(bc.submitted[0]) != 2 || bc.submitted[0][0].CustomID != "knowledge-1" || bc.submitted[0][1].CustomID != "knowledge-4" {
		t.Fatalf("submitted %+v", bc.submitted)
	}
	r := bc.submitted[0][0]
	if r.MaxTokens != 4096 || !strings.Contains(r.User, "<<<DATEN") || !strings.Contains(r.System, "Deutsch") {
		t.Errorf("request %+v", r)
	}
	open, _ := f.OpenExplainBatches()
	if len(open) != 1 || open[0].ProviderID != "msgbatch_1" || open[0].Pending != 2 || open[0].Model != "claude-haiku-5-5" || open[0].Language != "de" {
		t.Errorf("recorded %+v", open)
	}
}

func TestNoObjectIsSubmittedTwiceWhileItsBatchIsOpen(t *testing.T) {
	f := newFakeStore()
	queue(f, 3)
	bc := &fakeBatchClient{}
	s := batchSvc(f, bc, Limits{})
	if _, err := s.SubmitBatches(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	rep, err := s.SubmitBatches(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Submitted != 1 || rep.Pending != 2 || len(bc.submitted) != 2 || bc.submitted[1][0].CustomID != "knowledge-3" {
		t.Fatalf("second run %+v, batches %d", rep, len(bc.submitted))
	}
	rep, _ = s.SubmitBatches(context.Background(), 100)
	if rep.Submitted != 0 || rep.Pending != 3 || rep.Stopped != "none" || len(bc.submitted) != 2 {
		t.Errorf("third run %+v", rep)
	}
	// An object edited meanwhile is a different text: it is sent again, and the
	// result for the old one is discarded when it arrives.
	f.put(KindKnowledge, 1, "p", "T", "Text 1, geändert")
	rep, _ = s.SubmitBatches(context.Background(), 100)
	if rep.Submitted != 1 {
		t.Errorf("edited object: %+v", rep)
	}
}

func TestTheServerDoesNotGenerateAnObjectThatIsInAnOpenBatch(t *testing.T) {
	f := newFakeStore()
	queue(f, 1)
	bc := &fakeBatchClient{}
	c := &fakeLLM{reply: goodReply}
	s := New(f, c, Config{Language: "de", Batch: bc, Now: func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) }})
	if _, err := s.SubmitBatches(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if v := s.View(KindKnowledge, 1); !v.Empty() {
		t.Errorf("%+v", v)
	}
	s.Wait()
	if c.calls != 0 {
		t.Errorf("%d single calls for an object the batch carries", c.calls)
	}
}

func TestLargeBacklogIsSplitIntoSeveralBatches(t *testing.T) {
	f := newFakeStore()
	queue(f, 7)
	bc := &fakeBatchClient{}
	s := batchSvc(f, bc, Limits{})
	s.cfg.BatchMaxRequests = 3
	rep, err := s.SubmitBatches(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Batches != 3 || rep.Submitted != 7 || len(bc.submitted[2]) != 1 {
		t.Errorf("%+v", rep)
	}
	if open, _ := f.OpenExplainBatches(); len(open) != 3 {
		t.Errorf("%d batches recorded", len(open))
	}
}

func TestCollectStoresTextsAndCountsTheBatchPrice(t *testing.T) {
	f := newFakeStore()
	queue(f, 2)
	bc := &fakeBatchClient{}
	s := batchSvc(f, bc, Limits{})
	if _, err := s.SubmitBatches(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	// Not finished: nothing is read, nothing counted.
	rep, err := s.CollectBatches(context.Background())
	if err != nil || rep.Open != 1 || rep.Saved != 0 || bc.collects != 0 {
		t.Fatalf("unfinished: %+v %v", rep, err)
	}
	bc.done = true
	bc.ok(KindKnowledge, 1, 1000, 200)
	bc.results[customID(KindKnowledge, 2)] = llm.BatchResult{Content: goodReply, PromptTokens: 500, CompletionTokens: 100, CacheReadTokens: 10000, CacheWriteTokens: 2000}
	rep, err = s.CollectBatches(context.Background())
	if err != nil || rep.Saved != 2 || rep.Collected != 1 || rep.Failed != 0 {
		t.Fatalf("collect: %+v %v", rep, err)
	}
	e, ok, _ := f.Explanation(KindKnowledge, 1)
	if !ok || e.Text != goodReply || e.Source != SourceGenerated || e.Model != "claude-haiku-5-5" || e.Language != "de" || e.Project != "p" || e.ContentHash != f.src[key(KindKnowledge, 1)].Hash {
		t.Errorf("stored %+v", e)
	}
	u, _ := s.Today()
	if u.Calls != 2 || u.Failures != 0 || u.InputTokens != 1500 || u.OutputTokens != 300 || u.CacheReadTokens != 10000 || u.CacheWriteTokens != 2000 {
		t.Fatalf("usage %+v", u)
	}
	// Synchronous price: (1500*1 + 300*5 + 10000*0.1 + 2000*1.25)/1e6 = 0.0065; a batch costs half.
	if got := u.CostUSD(); got < 0.003249 || got > 0.003251 {
		t.Errorf("cost %.6f, want 0.00325 (50%% of list price)", got)
	}
	// The batch is closed: a second run reads nothing and counts nothing.
	rep, _ = s.CollectBatches(context.Background())
	if rep.Collected != 0 || bc.collects != 1 {
		t.Errorf("second collect: %+v, %d reads", rep, bc.collects)
	}
	if again, _ := s.Today(); again.Calls != 2 {
		t.Errorf("counted twice: %+v", again)
	}
}

func TestOneFailedEntryDoesNotCostTheOthersTheirTexts(t *testing.T) {
	f := newFakeStore()
	queue(f, 6)
	bc := &fakeBatchClient{done: true}
	s := batchSvc(f, bc, Limits{})
	if _, err := s.SubmitBatches(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	bc.ok(KindKnowledge, 1, 100, 10)
	bc.results[customID(KindKnowledge, 2)] = llm.BatchResult{Error: "overloaded"}
	// cut off at the output limit: billed, no usable text
	bc.results[customID(KindKnowledge, 3)] = llm.BatchResult{Content: "Halber Sa", Truncated: true, PromptTokens: 100, CompletionTokens: 4096}
	// a secret in the model output is refused like in the single path
	bc.results[customID(KindKnowledge, 4)] = llm.BatchResult{Content: "Der Zugang ist sk-ant-abcdefghijklmnop1234 und das ist wichtig genug.", PromptTokens: 100, CompletionTokens: 30}
	bc.ok(KindKnowledge, 5, 100, 10)
	// 6 has no result at all
	rep, err := s.CollectBatches(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Saved != 2 || rep.Failed != 4 || rep.Collected != 1 {
		t.Fatalf("%+v", rep)
	}
	for _, id := range []int64{2, 3, 4, 6} {
		if _, ok, _ := f.Explanation(KindKnowledge, id); ok {
			t.Errorf("object %d got an explanation", id)
		}
	}
	for _, id := range []int64{1, 5} {
		if _, ok, _ := f.Explanation(KindKnowledge, id); !ok {
			t.Errorf("object %d lost its text with its siblings", id)
		}
	}
	u, _ := s.Today()
	if u.Calls != 6 || u.Failures != 4 || u.OutputTokens != 4096+30+10+10 {
		t.Errorf("usage %+v: failed entries count, billed ones with their tokens", u)
	}
	if len(rep.Errors) == 0 || !strings.Contains(strings.Join(rep.Errors, "\n"), "overloaded") {
		t.Errorf("errors not reported: %v", rep.Errors)
	}
	// The failed objects are not sent again at the next run for a day.
	next, _ := s.SubmitBatches(context.Background(), 10)
	if next.Submitted != 0 || next.Pending != 4 {
		t.Errorf("failed objects resubmitted at once: %+v", next)
	}
	later := batchSvc(f, bc, Limits{})
	later.cfg.Now = func() time.Time { return time.Date(2026, 10, 17, 13, 0, 0, 0, time.UTC) }
	again, _ := later.SubmitBatches(context.Background(), 10)
	if again.Submitted != 4 {
		t.Errorf("not retried after a day: %+v", again)
	}
}

func TestResultForAnObjectEditedMeanwhileOrAnsweredByAnAgentIsNotStored(t *testing.T) {
	f := newFakeStore()
	queue(f, 2)
	bc := &fakeBatchClient{done: true}
	s := batchSvc(f, bc, Limits{})
	if _, err := s.SubmitBatches(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	bc.ok(KindKnowledge, 1, 100, 10)
	bc.ok(KindKnowledge, 2, 100, 10)
	f.put(KindKnowledge, 1, "p", "T", "inzwischen geändert")
	f.exp[key(KindKnowledge, 2)] = Explanation{Kind: KindKnowledge, ObjectID: 2, Text: "Der Agent hat das selbst erklärt, aktuell.", Source: SourceAgent, ContentHash: f.src[key(KindKnowledge, 2)].Hash}
	rep, _ := s.CollectBatches(context.Background())
	if rep.Stale != 1 || rep.Superseded != 1 || rep.Saved != 0 {
		t.Fatalf("%+v", rep)
	}
	if _, ok, _ := f.Explanation(KindKnowledge, 1); ok {
		t.Error("text of the old wording stored")
	}
	if e, _, _ := f.Explanation(KindKnowledge, 2); e.Source != SourceAgent {
		t.Error("agent text overwritten")
	}
	if u, _ := s.Today(); u.Calls != 2 || u.OutputTokens != 20 {
		t.Errorf("both were billed: %+v", u)
	}
}

func TestSecretInTheObjectIsNeverPutIntoABatch(t *testing.T) {
	f := newFakeStore()
	f.put(KindKnowledge, 1, "p", "Key", "Der Key lautet sk-ant-abcdefghijklmnop1234 im Klartext.")
	f.put(KindKnowledge, 2, "p", "Ok", "harmloser Text")
	f.cands = []Candidate{{KindKnowledge, 1}, {KindKnowledge, 2}}
	bc := &fakeBatchClient{}
	rep, err := batchSvc(f, bc, Limits{}).SubmitBatches(context.Background(), 10)
	if err != nil || rep.Skipped != 1 || rep.Submitted != 1 || strings.Contains(bc.submitted[0][0].User, "sk-ant") {
		t.Errorf("%+v %v", rep, err)
	}
}

func TestSubmitStopsAtACapAndOpenBatchesCountAsCalls(t *testing.T) {
	f := newFakeStore()
	queue(f, 5)
	bc := &fakeBatchClient{}
	s := batchSvc(f, bc, Limits{Day: Limit{Calls: 3}})
	rep, _ := s.SubmitBatches(context.Background(), 100)
	if rep.Submitted != 3 || rep.Stopped != "limit" {
		t.Fatalf("cap of 3 calls: %+v", rep)
	}
	// The three wait in an open batch: they use the cap up although nothing is billed yet.
	rep, _ = s.SubmitBatches(context.Background(), 100)
	if rep.Submitted != 0 || rep.Stopped != "limit" || !strings.Contains(rep.LimitHit, "daily calls") || len(bc.submitted) != 1 {
		t.Errorf("second run: %+v", rep)
	}
	// Collecting books them as calls; the cap still holds.
	bc.done = true
	for i := int64(1); i <= 3; i++ {
		bc.ok(KindKnowledge, i, 10, 10)
	}
	if _, err := s.CollectBatches(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rep, _ = s.SubmitBatches(context.Background(), 100); rep.Submitted != 0 || rep.Stopped != "limit" {
		t.Errorf("after collecting: %+v", rep)
	}
	// A dollar cap that is used up stops it too.
	g := newFakeStore()
	queue(g, 1)
	g.usage["2026-10-16"] = Usage{CostMicroUSD: 5_000_000}
	if rep, _ := batchSvc(g, bc, Limits{Day: Limit{USD: 5}}).SubmitBatches(context.Background(), 10); rep.Stopped != "limit" || rep.Submitted != 0 {
		t.Errorf("usd cap: %+v", rep)
	}
}

func TestSubmitFailureLeavesNothingRecordedAndWithoutAClientNothingHappens(t *testing.T) {
	f := newFakeStore()
	queue(f, 2)
	bc := &fakeBatchClient{submitErr: errors.New("HTTP 401")}
	if _, err := batchSvc(f, bc, Limits{}).SubmitBatches(context.Background(), 10); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("err %v", err)
	}
	if open, _ := f.OpenExplainBatches(); len(open) != 0 {
		t.Errorf("a batch that was never accepted is on record: %+v", open)
	}
	if _, err := batchSvc(f, nil, Limits{}).SubmitBatches(context.Background(), 10); !errors.Is(err, ErrDisabled) {
		t.Errorf("without a client: %v", err)
	}
	if _, err := svcWith(f, nil, Limits{}).CollectBatches(context.Background()); !errors.Is(err, ErrDisabled) {
		t.Errorf("collect without a client: %v", err)
	}
}

func TestAnUnreadableBatchIsGivenUpAfterItsMaxAgeAndReleasesItsObjects(t *testing.T) {
	f := newFakeStore()
	queue(f, 3)
	bc := &fakeBatchClient{}
	s := batchSvc(f, bc, Limits{Day: Limit{Calls: 3}})
	if rep, _ := s.SubmitBatches(context.Background(), 100); rep.Submitted != 3 {
		t.Fatalf("%+v", rep)
	}
	bc.statusErr = errors.New("HTTP 404")
	// Young: it stays open, still counts against the cap, and is tried again.
	rep, _ := s.CollectBatches(context.Background())
	if rep.Open != 1 || len(rep.Errors) != 1 {
		t.Fatalf("young: %+v", rep)
	}
	if rep, _ := s.SubmitBatches(context.Background(), 100); rep.Stopped != "limit" || rep.Submitted != 0 {
		t.Fatalf("cap while open: %+v", rep)
	}
	// Old: given up, objects released after the failure delay, no usage booked.
	f.batches[0].CreatedAt = s.cfg.Now().Add(-49 * time.Hour).Format(time.RFC3339)
	rep, err := s.CollectBatches(context.Background())
	if err != nil || rep.Open != 0 || rep.Collected != 1 || len(rep.Errors) != 1 || !strings.Contains(rep.Errors[0], "given up") {
		t.Fatalf("old: %+v %v", rep, err)
	}
	if u, _ := s.Today(); u.Calls != 0 {
		t.Errorf("giving up booked calls: %+v", u)
	}
	if open, _ := f.OpenExplainBatches(); len(open) != 0 {
		t.Errorf("still open: %+v", open)
	}
	// Collect errors and never-finishing batches age out the same way.
	for name, set := range map[string]func(){
		"results":  func() { bc.statusErr, bc.done, bc.collectErr = nil, true, errors.New("decode") },
		"finished": func() { bc.statusErr, bc.done, bc.collectErr = nil, false, nil },
	} {
		g := newFakeStore()
		queue(g, 1)
		s2 := batchSvc(g, bc, Limits{})
		if _, err := s2.SubmitBatches(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
		set()
		g.batches[0].CreatedAt = s2.cfg.Now().Add(-49 * time.Hour).Format(time.RFC3339)
		if rep, _ := s2.CollectBatches(context.Background()); rep.Open != 0 || rep.Collected != 1 {
			t.Errorf("%s: %+v", name, rep)
		}
	}
}

func TestObjectsAreReservedBeforeTheUploadAndAKilledRunIsReleased(t *testing.T) {
	f := newFakeStore()
	queue(f, 2)
	bc := &fakeBatchClient{}
	s := batchSvc(f, bc, Limits{})
	var during SubmitReport
	bc.onSubmit = func() {
		bc.onSubmit = nil
		during, _ = s.SubmitBatches(context.Background(), 100) // a second run, while the upload is going on
	}
	if _, err := s.SubmitBatches(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if during.Submitted != 0 || during.Pending != 2 || len(bc.submitted) != 1 {
		t.Fatalf("concurrent run sent %+v, uploads %d", during, len(bc.submitted))
	}
	// A reservation another run made between reading and reserving is refused.
	g := newFakeStore()
	queue(g, 1)
	s2 := batchSvc(g, bc, Limits{})
	g.cands = nil
	if _, err := g.ReserveExplainBatch("m", "de", []BatchItem{{CustomID: "knowledge-1", Kind: KindKnowledge, ObjectID: 1, Hash: g.src[key(KindKnowledge, 1)].Hash}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ReserveExplainBatch("m", "de", []BatchItem{{CustomID: "knowledge-1", Kind: KindKnowledge, ObjectID: 1, Hash: g.src[key(KindKnowledge, 1)].Hash}}); !errors.Is(err, ErrBatchTaken) {
		t.Errorf("second reservation: %v", err)
	}
	// The run died after reserving: still blocked while young, released after two hours.
	if rep, _ := s2.CollectBatches(context.Background()); rep.Open != 1 || rep.Collected != 0 {
		t.Errorf("young reservation: %+v", rep)
	}
	g.batches[0].CreatedAt = s2.cfg.Now().Add(-3 * time.Hour).Format(time.RFC3339)
	if rep, _ := s2.CollectBatches(context.Background()); rep.Open != 0 || rep.Collected != 1 {
		t.Errorf("old reservation: %+v", rep)
	}
	if blocked, _ := g.ExplainBatchBlocked("2000-01-01T00:00:00Z"); len(blocked) != 0 {
		t.Errorf("still blocked: %v", blocked)
	}
}

func TestTokenAndDollarCapsHoldBeforeTheBatchIsCollected(t *testing.T) {
	f := newFakeStore()
	queue(f, 50)
	bc := &fakeBatchClient{}
	// Each request is estimated at its input plus 1024 output tokens, about 1040.
	s := batchSvc(f, bc, Limits{Day: Limit{Tokens: 5000}})
	rep, err := s.SubmitBatches(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Submitted < 1 || rep.Submitted > 6 || rep.Stopped != "limit" || !strings.Contains(rep.LimitHit, "tokens") {
		t.Fatalf("token cap: %+v", rep)
	}
	// The open batch counts against the cap on the next run.
	if rep, _ = s.SubmitBatches(context.Background(), 1000); rep.Submitted != 0 || rep.Stopped != "limit" {
		t.Errorf("second run: %+v", rep)
	}
	// Dollars: price 1/5 per million, batch half: ~1040 tokens is about $0.0027 per request.
	g := newFakeStore()
	queue(g, 50)
	rep, _ = batchSvc(g, bc, Limits{Day: Limit{USD: 0.01}}).SubmitBatches(context.Background(), 1000)
	if rep.Submitted < 1 || rep.Submitted > 6 || rep.Stopped != "limit" || !strings.Contains(rep.LimitHit, "limit") {
		t.Errorf("usd cap: %+v", rep)
	}
}
