package sessiondistill

import (
	"context"
	"strings"
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
	// Model is recorded with the batch so a later price change stays
	// attributable and cannot retroactively restate an earlier bill.
	Model  string
	Budget Budget
	Limit  int
	DryRun bool
	// Requests reads the person's messages for wishes instead of the whole
	// transcript for knowledge. The two modes share everything except what they
	// read, what they ask and where the answer lands.
	Requests bool
}

// The custom id carries the mode. Collecting happens in a different process
// from submitting, possibly days later, and the batch row would otherwise have
// to say which apply path its results belong to — a schema change on a table
// that exists in production, to encode something the id can carry for free.
const (
	knowledgePrefix = "session-"
	requestPrefix   = "wish-"
)

func version(requests bool) string {
	if requests {
		return RequestPromptVersion
	}
	return PromptVersion
}

type SubmitReport struct {
	ProviderID      string
	Sessions        int
	PromptChars     int
	EstimatedTokens int
	TrimmedSessions int
	DroppedChunks   int
	// SkippedWithoutProject counts sessions held back because they ran outside
	// a repository. Reported rather than silent: it is a quarter of the archive.
	SkippedWithoutProject int
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
	sessions, err := st.SessionsPendingDistillation(opts.Filter, opts.IdleBefore, version(opts.Requests), opts.Limit)
	if err != nil {
		return report, err
	}
	// Sessions without a project are excluded by the selection itself, so the
	// limit is filled with work that can be done. Counting them separately is
	// what keeps a quarter of the archive from silently disappearing.
	skipped, err := st.CountPendingWithoutProject(opts.IdleBefore)
	if err != nil {
		return report, err
	}
	report.SkippedWithoutProject = skipped

	// The whole submission is known before any prompt is built, and it has to
	// be: the titles a session is told not to repeat must exclude every item
	// this same run will archive, which is every item evidenced by any session
	// in the submission.
	submitted := make([]int64, 0, len(sessions))
	for _, session := range sessions {
		submitted = append(submitted, session.ID)
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
		exists, err := st.SessionDistillationExists(session.ID, digest, version(opts.Requests))
		if err != nil {
			return report, err
		}
		if exists {
			continue
		}
		titles, ok := titlesByProject[session.Scope.Project]
		if !ok {
			if opts.Requests {
				titles, err = st.RequestTitlesForPrompt(session.Scope.Project)
			} else {
				titles, err = st.KnowledgeTitlesForPrompt(session.Scope.Project, submitted)
			}
			if err != nil {
				return report, err
			}
			titlesByProject[session.Scope.Project] = titles
		}
		sysPrompt, user, customID := system, "", fmt.Sprintf("%s%d", knowledgePrefix, session.ID)
		if opts.Requests {
			said := UserChunks(chunks)
			if len(said) == 0 {
				continue
			}
			sent, _ := SelectWithinBudget(said, opts.Budget)
			sysPrompt = requestSystem
			user = RequestPrompt(sent, titles)
			customID = fmt.Sprintf("%s%d", requestPrefix, session.ID)
		} else {
			sent, dropped := SelectWithinBudget(chunks, opts.Budget)
			user = Prompt(sent, titles)
			if dropped > 0 {
				report.TrimmedSessions++
				report.DroppedChunks += dropped
			}
		}
		report.PromptChars += len(user) + len(sysPrompt)
		reqs = append(reqs, llm.BatchRequest{
			CustomID: customID, System: sysPrompt, User: user,
			MaxTokens: MaxOutputTokens, JSONMode: true,
		})
		items = append(items, store.DistillBatchItem{
			CustomID: customID, SessionID: session.ID, Digest: digest, PromptVersion: version(opts.Requests)})
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
	if _, err := st.RecordDistillBatch(providerID, opts.Model, items); err != nil {
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
		if strings.HasPrefix(item.CustomID, requestPrefix) {
			wishes, err := ParseRequests(result.Content, chunks)
			if err != nil {
				report.Failed++
				report.note("session %d: %v", item.SessionID, err)
				if _, err := st.ApplyRequestDistillation(item.SessionID, item.Digest, RequestPromptVersion, session.Scope, nil); err != nil {
					return err
				}
				continue
			}
			n, err := st.ApplyRequestDistillation(item.SessionID, item.Digest, RequestPromptVersion, session.Scope, wishes)
			if err != nil {
				// A wish that cannot be traced back to the person is not a
				// partial result to salvage. The session returns to the backlog.
				report.Failed++
				report.note("session %d: %v", item.SessionID, err)
				continue
			}
			report.Sessions++
			report.Items += n
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
			if _, err := st.ApplySessionDistillation(item.SessionID, item.Digest, PromptVersion, session.Scope, nil); err != nil {
				return err
			}
			continue
		}
		n, err := st.ApplySessionDistillation(item.SessionID, item.Digest, PromptVersion, session.Scope, parsed)
		if err != nil {
			return err
		}
		report.Sessions++
		report.Items += n
	}
	return nil
}
