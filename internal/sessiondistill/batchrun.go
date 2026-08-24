package sessiondistill

import (
	"context"
	"fmt"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// maxChunksPerSession bounds one transcript read. The budget trims further;
// this only keeps a pathological session from being loaded whole.
const maxChunksPerSession = 5000

type SubmitOptions struct {
	Filter     scope.Axes
	IdleBefore string
	Budget     Budget
	Limit      int
	DryRun     bool
}

type SubmitReport struct {
	ProviderID      string
	Sessions        int
	PromptChars     int
	EstimatedTokens int
	TrimmedSessions int
	DroppedChunks   int
}

type CollectReport struct {
	// Failures names why each item failed. The first production run reported
	// "10 failed" and nothing else, which is a number nobody can act on.
	Failures         []string
	Batches          int
	Pending          int
	Sessions         int
	Items            int
	Failed           int
	Truncated        int
	PromptTokens     int
	CompletionTokens int
}

// maxReportedFailures bounds the list. A run where everything failed should say
// so once, not print fifty near-identical lines into the journal.
const maxReportedFailures = 10

func (r *CollectReport) note(format string, args ...any) {
	if len(r.Failures) >= maxReportedFailures {
		return
	}
	r.Failures = append(r.Failures, fmt.Sprintf(format, args...))
}

// SubmitBatch sends every eligible session in one batch and records what went
// out. The record is the whole point: the answer arrives up to 24 hours later
// in a different process, and without it the next timer tick cannot tell work
// in progress from work not yet started.
func SubmitBatch(ctx context.Context, st *store.Store, client llm.BatchClient, opts SubmitOptions) (SubmitReport, error) {
	var report SubmitReport
	sessions, err := st.SessionsPendingDistillation(opts.Filter, opts.IdleBefore, opts.Limit)
	if err != nil {
		return report, err
	}
	titlesByProject := map[string][]string{}
	reqs := []llm.BatchRequest{}
	items := []store.DistillBatchItem{}
	for _, session := range sessions {
		chunks, err := st.ReadSession(session.ID, 0, maxChunksPerSession)
		if err != nil {
			return report, err
		}
		if len(chunks) == 0 {
			continue
		}
		digest := Digest(chunks)
		exists, err := st.SessionDistillationExists(session.ID, digest)
		if err != nil {
			return report, err
		}
		if exists {
			continue
		}
		titles, ok := titlesByProject[session.Scope.Project]
		if !ok {
			existing, err := st.KnowledgeForProject(session.Scope.Project)
			if err != nil {
				return report, err
			}
			for _, k := range existing {
				titles = append(titles, k.Title)
			}
			titlesByProject[session.Scope.Project] = titles
		}
		sent, dropped := SelectWithinBudget(chunks, opts.Budget)
		user := Prompt(sent, titles)
		if dropped > 0 {
			report.TrimmedSessions++
			report.DroppedChunks += dropped
		}
		report.PromptChars += len(user) + len(system)
		customID := fmt.Sprintf("session-%d", session.ID)
		reqs = append(reqs, llm.BatchRequest{
			CustomID: customID, System: system, User: user,
			MaxTokens: MaxOutputTokens, JSONMode: true,
		})
		items = append(items, store.DistillBatchItem{CustomID: customID, SessionID: session.ID, Digest: digest})
	}
	report.Sessions = len(reqs)
	report.EstimatedTokens = EstimateTokens(report.PromptChars)
	if len(reqs) == 0 || opts.DryRun {
		return report, nil
	}
	providerID, err := client.SubmitBatch(ctx, reqs)
	if err != nil {
		return report, err
	}
	if _, err := st.RecordDistillBatch(providerID, items); err != nil {
		// The batch is already running and will be billed. Losing the record
		// means it can never be collected, so this is a hard failure worth
		// surfacing with the id needed to recover by hand.
		return report, fmt.Errorf("batch %s submitted but not recorded: %w", providerID, err)
	}
	report.ProviderID = providerID
	return report, nil
}

// CollectBatches ingests every finished batch and leaves running ones alone.
func CollectBatches(ctx context.Context, st *store.Store, client llm.BatchClient) (CollectReport, error) {
	var report CollectReport
	batches, err := st.OpenDistillBatches()
	if err != nil {
		return report, err
	}
	for _, batch := range batches {
		status, err := client.BatchStatus(ctx, batch.ProviderID)
		if err != nil {
			return report, err
		}
		if !status.Done {
			report.Pending++
			continue
		}
		report.Batches++
		results, err := client.CollectBatch(ctx, batch.ProviderID)
		if err != nil {
			// A terminal batch with no readable output has nothing more to
			// give; holding it open would withhold its sessions forever.
			report.Failed += batch.Items
			if closeErr := st.CloseDistillBatch(batch.ID, "failed"); closeErr != nil {
				return report, closeErr
			}
			continue
		}
		if err := ingestBatch(st, batch, results, &report); err != nil {
			return report, err
		}
		if err := st.CloseDistillBatch(batch.ID, "collected"); err != nil {
			return report, err
		}
	}
	return report, nil
}

func ingestBatch(st *store.Store, batch store.DistillBatch, results map[string]llm.BatchResult, report *CollectReport) error {
	items, err := st.DistillBatchItems(batch.ID)
	if err != nil {
		return err
	}
	for _, item := range items {
		result, ok := results[item.CustomID]
		if !ok || result.Error != "" {
			// Provider-side failures are transient. The session keeps no
			// distillation row and returns to the backlog for the next run.
			report.Failed++
			reason := "no result in batch output"
			if ok {
				reason = result.Error
			}
			report.note("session %d: %s", item.SessionID, reason)
			continue
		}
		if err := st.RecordDistillBatchUsage(batch.ID, item.CustomID, result.PromptTokens, result.CompletionTokens); err != nil {
			return err
		}
		report.PromptTokens += result.PromptTokens
		report.CompletionTokens += result.CompletionTokens

		session, err := st.SessionByID(item.SessionID)
		if err != nil {
			return err
		}
		chunks, err := st.ReadSession(item.SessionID, 0, maxChunksPerSession)
		if err != nil {
			return err
		}
		if result.Truncated {
			// The cap, not the transcript, is what failed here. Recording it as
			// a distillation of zero items would retire the session on the
			// strength of a configuration limit, and permanently: the digest
			// keys on transcript content, so raising the cap later would not
			// bring it back.
			report.Failed++
			report.Truncated++
			report.note("session %d: reply truncated at the output cap, left in the backlog", item.SessionID)
			continue
		}
		parsed, err := Parse(result.Content, chunks)
		if err != nil {
			// An unusable answer is recorded as a distillation of zero items.
			// Retrying would send the same transcript through the same prompt
			// for the same result, so the alternative is paying for it hourly,
			// forever.
			report.Failed++
			report.note("session %d: %v", item.SessionID, err)
			if _, err := st.ApplySessionDistillation(item.SessionID, item.Digest, session.Scope, nil); err != nil {
				return err
			}
			continue
		}
		n, err := st.ApplySessionDistillation(item.SessionID, item.Digest, session.Scope, parsed)
		if err != nil {
			return err
		}
		report.Sessions++
		report.Items += n
	}
	return nil
}
