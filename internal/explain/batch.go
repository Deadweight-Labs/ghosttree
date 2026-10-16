package explain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
)

// The bulk path of "Kurz erklärt": explanations of many objects are written
// through the provider's batch endpoint, which costs half and takes hours, in
// two steps that run in different processes. SubmitBatches sends everything
// that lacks a current text and records what it sent; CollectBatches reads the
// finished batches later and stores the texts. The records are what keeps a
// later run from sending the same object again while its batch is open.

// BatchItem is one object inside a batch: CustomID is what the provider
// returns, Hash the text the request was built from.
type BatchItem struct {
	CustomID string
	Kind     string
	ObjectID int64
	Hash     string
}

// OpenBatch is a batch that was sent and not collected yet. Pending is the
// number of its objects that still wait for a result.
type OpenBatch struct {
	ID         int64
	ProviderID string
	// State is "open" (at the provider) or "submitting" (reserved, the provider has not answered yet).
	State     string
	Model     string
	Language  string
	CreatedAt string
	Pending   int
}

// BatchKey names an object at a text for the blocked set.
func BatchKey(kind string, id int64, hash string) string {
	return fmt.Sprintf("%s:%d:%s", kind, id, hash)
}

func customID(kind string, id int64) string { return fmt.Sprintf("%s-%d", kind, id) }

const (
	// retryFailedAfter is how long an object whose batch entry failed is left
	// alone. A failure that costs tokens (cut off at the output limit) would
	// otherwise be paid for again at every hourly run.
	retryFailedAfter = 24 * time.Hour
	// maxBatchAge is how long an open batch is waited for. The provider ends a
	// batch within 24 hours; one that cannot be read after twice that (other
	// key, results gone) is given up so that its objects are released and stop
	// counting against the caps.
	maxBatchAge = 48 * time.Hour
	// maxSubmitAge is how long a reservation may stay in "submitting": a run that
	// was killed between reserving and the provider's answer.
	maxSubmitAge = 2 * time.Hour
	// Estimates for the caps, applied to requests not yet answered: the output
	// is far below the ceiling in practice.
	expectedOutputTokens = 1024
	expectedWaitingInput = 1500
	// NewObjectWindow is how young an object must be to be explained one by one
	// when a page is opened; everything older waits for the batch.
	NewObjectWindow = 24 * time.Hour
)

func (s *Service) failedSince() string {
	return s.cfg.Now().Add(-retryFailedAfter).UTC().Format(time.RFC3339)
}

// SubmitReport is the result of SubmitBatches.
type SubmitReport struct {
	// Submitted objects, in Batches batches at the provider.
	Submitted int
	Batches   int
	// Fresh already have a current text, Pending wait in an open batch or failed
	// recently, Skipped cannot be sent (no project, or the text looks like a
	// secret).
	Fresh   int
	Pending int
	Skipped int
	// Stopped is "limit" (a cap is used up), "none" (nothing left to send) or ""
	// (the requested number was reached).
	Stopped  string
	LimitHit string
	Errors   []string
}

// SubmitBatches sends up to limit objects that lack a current explanation, the
// review queue first, as one batch (several if the provider's size limits
// require). Objects that wait in an open batch are not sent again.
func (s *Service) SubmitBatches(ctx context.Context, limit int) (SubmitReport, error) {
	var r SubmitReport
	if !s.Enabled() || s.cfg.Batch == nil {
		return r, ErrDisabled
	}
	// Calls that wait for a result count against a calls cap already, so a
	// batch cannot be submitted twice over a cap of one.
	open, err := s.st.OpenExplainBatches()
	if err != nil {
		return r, err
	}
	waiting := 0
	for _, b := range open {
		waiting += b.Pending
	}
	day, err := s.Today()
	if err != nil {
		return r, err
	}
	month, err := s.Month()
	if err != nil {
		return r, err
	}
	day.Calls += waiting
	month.Calls += waiting
	// Open batches are not in the usage yet; count them at an estimate so that
	// the token and dollar caps hold before the money is spent, not after.
	wait := s.estimate(waiting*expectedWaitingInput, waiting*expectedOutputTokens)
	wait.Calls = 0
	day, month = day.Add(wait), month.Add(wait)
	if full := s.cfg.Limits.Check(day, month); full != nil {
		r.Stopped, r.LimitHit = "limit", full.Error()
		return r, nil
	}
	room := limit
	for _, c := range []struct{ cap, used int }{{s.cfg.Limits.Day.Calls, day.Calls}, {s.cfg.Limits.Month.Calls, month.Calls}} {
		if c.cap > 0 && c.cap-c.used < room {
			room = c.cap - c.used
		}
	}
	cands, err := s.st.ExplainCandidates(1 << 20)
	if err != nil {
		return r, err
	}
	blocked, err := s.st.ExplainBatchBlocked(s.failedSince())
	if err != nil {
		return r, err
	}
	var reqs []llm.BatchRequest
	var items []BatchItem
	seen := map[string]bool{}
	capped, stopped := false, false
	var queued Usage
	for _, c := range cands {
		if ctx.Err() != nil {
			return r, ctx.Err()
		}
		id := customID(c.Kind, c.ID)
		if seen[id] {
			continue
		}
		seen[id] = true
		src, err := s.st.ExplainSource(c.Kind, c.ID)
		if err != nil || src.Project == "" {
			continue
		}
		e, ok, err := s.st.Explanation(c.Kind, c.ID)
		if err != nil {
			return r, err
		}
		if ok && e.ContentHash == src.Hash && (e.Source != SourceGenerated || e.Language == s.cfg.Language) {
			r.Fresh++
			continue
		}
		if blocked[BatchKey(c.Kind, c.ID, src.Hash)] {
			r.Pending++
			continue
		}
		fields := []string{"title", src.Title, "text", src.Body}
		for _, cx := range src.Context {
			fields = append(fields, "context", cx)
		}
		if rule := humantask.CheckFields(fields...); rule != nil {
			r.Skipped++
			continue
		}
		if len(reqs) >= room {
			capped = true
			break
		}
		system, user := Prompt(s.cfg.Language, src)
		if full := s.cfg.Limits.Check(day.Add(queued), month.Add(queued)); full != nil {
			r.Stopped, r.LimitHit = "limit", full.Error()
			stopped = true
			break
		}
		one := s.estimate(estimateTokens(system)+estimateTokens(user), expectedOutputTokens)
		one.Calls = 0
		queued = queued.Add(one)
		reqs = append(reqs, llm.BatchRequest{CustomID: id, System: system, User: user, MaxTokens: maxOutputTokens})
		items = append(items, BatchItem{CustomID: id, Kind: c.Kind, ObjectID: c.ID, Hash: src.Hash})
	}
	limitHit := func() {
		if stopped {
			return
		}
		if room < limit {
			r.Stopped, r.LimitHit = "limit", fmt.Sprintf("the calls cap leaves room for %d more", max(room, 0))
		}
	}
	if len(reqs) == 0 {
		switch {
		case stopped:
		case capped:
			limitHit()
		default:
			r.Stopped = "none"
		}
		return r, nil
	}
	byID := map[string]BatchItem{}
	for _, it := range items {
		byID[it.CustomID] = it
	}
	for _, group := range llm.SplitBatch(reqs, s.cfg.Model, s.cfg.BatchMaxRequests, s.cfg.BatchMaxBytes) {
		recorded := make([]BatchItem, 0, len(group))
		for _, g := range group {
			recorded = append(recorded, byID[g.CustomID])
		}
		// Reserved before the upload: a second run, or the next hour after a kill,
		// sees these objects as taken.
		rid, err := s.st.ReserveExplainBatch(s.cfg.Model, s.cfg.Language, recorded)
		if errors.Is(err, ErrBatchTaken) {
			r.Errors = append(r.Errors, "batch skipped: an object of it is already being sent by another run")
			continue
		}
		if err != nil {
			return r, fmt.Errorf("reserve batch: %w", err)
		}
		providerID, err := s.cfg.Batch.SubmitBatch(ctx, group)
		if err != nil {
			_ = s.st.CloseExplainBatch(rid, "failed") // nothing was accepted: release the objects
			return r, fmt.Errorf("submit batch: %w", err)
		}
		var recErr error
		for try := 0; try < 3; try++ {
			if recErr = s.st.ActivateExplainBatch(rid, providerID); recErr == nil {
				break
			}
			time.Sleep(time.Duration(try) * 200 * time.Millisecond)
		}
		if recErr != nil {
			// The batch exists at the provider but is not on record; naming it
			// is the only way to find it again. The reservation stays and blocks
			// the objects until it is released.
			return r, fmt.Errorf("batch %s was submitted but could not be recorded: %w", providerID, recErr)
		}
		r.Batches++
		r.Submitted += len(group)
	}
	switch {
	case stopped:
	case capped:
		limitHit()
	default:
		r.Stopped = "none"
	}
	return r, nil
}

// estimate is the usage of requests that have not been answered: the tokens
// given, priced at the batch rate when the price is known.
func (s *Service) estimate(in, out int) Usage {
	u := Usage{Calls: 1, InputTokens: in, OutputTokens: out}
	if s.cfg.PriceKnown {
		u.CostMicroUSD = llmusage.MicroUSD(s.cfg.Price.Cost(llm.Usage{InputTokens: in, OutputTokens: out}) * llm.BatchPriceFactor)
	}
	return u
}

// CollectReport is the result of CollectBatches.
type CollectReport struct {
	// Open batches are still being processed at the provider.
	Open int
	// Collected batches were read and closed.
	Collected int
	Saved     int
	// Stale texts were discarded because the object changed after it was sent,
	// Superseded because a current text exists already.
	Stale      int
	Superseded int
	Failed     int
	Errors     []string
}

func (r *CollectReport) fail(format string, a ...any) {
	if len(r.Errors) < 5 {
		r.Errors = append(r.Errors, fmt.Sprintf(format, a...))
	}
}

// CollectBatches reads every open batch that has finished, stores its texts
// and counts what it used at the batch price. An entry that failed counts as a
// failed call and does not hold up the others; a batch whose results cannot be
// read stays open and is tried again at the next run.
func (s *Service) CollectBatches(ctx context.Context) (CollectReport, error) {
	var r CollectReport
	if !s.Enabled() || s.cfg.Batch == nil {
		return r, ErrDisabled
	}
	open, err := s.st.OpenExplainBatches()
	if err != nil {
		return r, err
	}
	for _, b := range open {
		if ctx.Err() != nil {
			return r, ctx.Err()
		}
		age := s.batchAge(b)
		if b.State == "submitting" {
			if age > maxSubmitAge {
				// The run that reserved it is gone and never recorded a provider id.
				if err := s.st.CloseExplainBatch(b.ID, "failed"); err != nil {
					return r, err
				}
				r.fail("reservation %d was never completed and is released", b.ID)
				r.Collected++
			} else {
				r.Open++
			}
			continue
		}
		status, err := s.cfg.Batch.BatchStatus(ctx, b.ProviderID)
		if err != nil {
			if age > maxBatchAge {
				if err := s.abandon(b, "status unreadable: "+err.Error(), &r); err != nil {
					return r, err
				}
				continue
			}
			r.fail("batch %s: %v", b.ProviderID, err)
			r.Open++
			continue
		}
		if !status.Done {
			if age > maxBatchAge {
				if err := s.abandon(b, "not finished after "+maxBatchAge.String(), &r); err != nil {
					return r, err
				}
				continue
			}
			r.Open++
			continue
		}
		results, err := s.cfg.Batch.CollectBatch(ctx, b.ProviderID)
		if err != nil {
			if status.Failed != "" {
				// Over without results (cancelled, expired as a whole): nothing
				// will ever come, and the objects may be sent again after the wait.
				if err := s.failBatch(b, status.Failed); err != nil {
					return r, err
				}
				r.Collected++
				continue
			}
			if age > maxBatchAge {
				if err := s.abandon(b, "results unreadable: "+err.Error(), &r); err != nil {
					return r, err
				}
				continue
			}
			r.fail("batch %s: %v", b.ProviderID, err)
			r.Open++
			continue
		}
		items, err := s.st.PendingExplainBatchItems(b.ID)
		if err != nil {
			return r, err
		}
		for _, it := range items {
			if err := s.finishItem(b, it, results, &r); err != nil {
				return r, err
			}
		}
		if err := s.st.CloseExplainBatch(b.ID, "collected"); err != nil {
			return r, err
		}
		r.Collected++
	}
	return r, nil
}

func (s *Service) batchAge(b OpenBatch) time.Duration {
	t, err := time.Parse(time.RFC3339, b.CreatedAt)
	if err != nil {
		return 0
	}
	return s.cfg.Now().Sub(t)
}

// abandon gives up a batch that cannot be read any more.
func (s *Service) abandon(b OpenBatch, why string, r *CollectReport) error {
	if err := s.failBatch(b, why); err != nil {
		return err
	}
	r.fail("batch %s given up: %s", b.ProviderID, why)
	r.Collected++
	return nil
}

func (s *Service) failBatch(b OpenBatch, why string) error {
	items, err := s.st.PendingExplainBatchItems(b.ID)
	if err != nil {
		return err
	}
	for _, it := range items {
		if err := s.st.FinishExplainBatchItem(b.ID, it.CustomID, "failed", "batch "+why, s.day(), Usage{Failures: 1}); err != nil {
			return err
		}
	}
	return s.st.CloseExplainBatch(b.ID, "failed")
}

// finishItem judges one result, stores its text if it is still wanted and
// records usage and outcome together.
func (s *Service) finishItem(b OpenBatch, it BatchItem, results map[string]llm.BatchResult, r *CollectReport) error {
	res, ok := results[it.CustomID]
	usage := Usage{Calls: 1}
	state, reason := "failed", ""
	var text string
	switch {
	case !ok:
		reason = "no result in the batch"
	default:
		usage.InputTokens, usage.OutputTokens = res.PromptTokens, res.CompletionTokens
		usage.CacheReadTokens, usage.CacheWriteTokens = res.CacheReadTokens, res.CacheWriteTokens
		switch {
		case res.Error != "":
			reason = res.Error
		case res.Truncated:
			reason = fmt.Sprintf("cut off at the %d token output limit", maxOutputTokens)
		default:
			var err error
			if text, err = Clean(res.Content); err != nil {
				reason = err.Error()
			} else {
				state = "saved"
			}
		}
	}
	if s.cfg.PriceKnown {
		usage.CostMicroUSD = llmusage.MicroUSD(s.cfg.Price.Cost(llm.Usage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
			CacheReadTokens: usage.CacheReadTokens, CacheWriteTokens: usage.CacheWriteTokens}) * llm.BatchPriceFactor)
	}
	if state == "saved" {
		src, err := s.st.ExplainSource(it.Kind, it.ObjectID)
		switch {
		case errors.Is(err, ErrNotExplainable) || (err == nil && (src.Project == "" || src.Hash != it.Hash)):
			// Gone, or edited while the model worked: the text would describe the old one.
			state = "stale"
		case err != nil:
			return err
		default:
			cur, have, err := s.st.Explanation(it.Kind, it.ObjectID)
			if err != nil {
				return err
			}
			if have && cur.ContentHash == src.Hash && (cur.Source != SourceGenerated || cur.Language == b.Language) {
				state = "superseded"
			} else if err := s.st.SaveExplanation(Explanation{
				Kind: it.Kind, ObjectID: it.ObjectID, Project: src.Project, Text: text, Language: b.Language,
				Source: SourceGenerated, Model: b.Model, GeneratedAt: s.cfg.Now().UTC().Format(time.RFC3339), ContentHash: src.Hash,
			}); err != nil {
				return err
			}
		}
	}
	if state == "failed" {
		usage.Failures = 1
	}
	if err := s.st.FinishExplainBatchItem(b.ID, it.CustomID, state, reason, s.day(), usage); err != nil {
		return err
	}
	switch state {
	case "saved":
		r.Saved++
	case "stale":
		r.Stale++
	case "superseded":
		r.Superseded++
	default:
		r.Failed++
		r.fail("%s %d: %s", it.Kind, it.ObjectID, strings.TrimSpace(reason))
	}
	return nil
}
