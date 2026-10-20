package main

import (
	"fmt"
	"io"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/garden"
	"github.com/Deadweight-Labs/ghosttree/internal/ghost"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// What jobs 4 (the ledger) and 1 (the ghosts) read from the server. Both only read:
// the client of the gardener has no call here that changes a request or a ghost.
type gardenReader interface {
	SearchRequests(filter requestdomain.SearchFilter) (requestdomain.SearchPage, error)
	GetRequest(id int64) (requestdomain.Detail, error)
	GhostTree(project, prefix string) ([]store.GhostFile, error)
	GhostReviews(project, prefix string) ([]store.GhostReview, error)
}

const (
	// maxExtraJobs bounds --requests and --ghosts: each is a few sessions a night.
	maxExtraJobs = 40
	// requestsScanned bounds how many open requests are read in one run.
	requestsScanned = 300
	// requestsPage is the most the server returns in one page.
	requestsPage = 25
)

// gardenJobs is what a run takes up besides the knowledge entries.
type gardenJobs struct {
	Ledger         []garden.LedgerRequest
	LedgerRotation garden.Rotation
	Idle           []garden.IdleRequest
	Ghosts         []garden.GhostPath
	GhostRotation  garden.GhostRotation
	Samples        []garden.GhostSample
}

// gardenRequests reads the open requests of the project with their criteria and
// activity. A request that cannot be read is left out and said so; it does not
// stop the run.
func gardenRequests(st gardenReader, project string, stdout io.Writer) ([]garden.LedgerRequest, []garden.RequestFacts, error) {
	var reqs []garden.LedgerRequest
	var facts []garden.RequestFacts
	cursor, scanned := "", 0
	for scanned < requestsScanned {
		page, err := st.SearchRequests(requestdomain.SearchFilter{Scope: scope.Axes{Project: project}, State: "open", Limit: requestsPage, Cursor: cursor})
		if err != nil {
			return nil, nil, err
		}
		for _, hit := range page.Results {
			if scanned++; scanned > requestsScanned {
				break
			}
			d, err := st.GetRequest(hit.Request.ID)
			if err != nil {
				fmt.Fprintf(stdout, "request #%d not read: %v\n", hit.Request.ID, err)
				continue
			}
			if d.Request.Scope.Project != project {
				continue
			}
			if lr, f, ok := garden.LedgerFromDetail(d); ok {
				reqs = append(reqs, lr)
				facts = append(facts, f)
			}
		}
		if page.NextCursor == "" || len(page.Results) == 0 {
			break
		}
		cursor = page.NextCursor
	}
	return reqs, facts, nil
}

// gardenIdle is the list of idle requests, from data alone.
func gardenIdle(st gardenReader, project string, opts gardenOpts, now time.Time, stdout io.Writer) []garden.IdleRequest {
	if opts.IdleDays <= 0 {
		return nil
	}
	_, facts, err := gardenRequests(st, project, stdout)
	if err != nil {
		fmt.Fprintf(stdout, "cannot read requests: %v (no list of idle requests)\n", err)
		return nil
	}
	return garden.IdleRequests(facts, now, opts.IdleDays)
}

// planGardenJobs chooses what jobs 4 and 1 take up in this run: the open requests
// that are due, the files whose description is out of date or missing, and the
// list of idle requests. A job that cannot read what it needs says so and is
// skipped; it never stops the knowledge job.
func planGardenJobs(st gardenStore, repo *garden.Repo, project string, opts gardenOpts, now time.Time, head string, stdout io.Writer) gardenJobs {
	var j gardenJobs
	if opts.Requests > 0 || opts.IdleDays > 0 {
		reqs, facts, err := gardenRequests(st, project, stdout)
		if err != nil {
			fmt.Fprintf(stdout, "cannot read requests: %v (the ledger job and the list of idle requests are skipped)\n", err)
		} else {
			j.Idle = garden.IdleRequests(facts, now, opts.IdleDays)
			if opts.Requests > 0 {
				seen := gardenSeen(opts.State, garden.LedgerState(project), stdout)
				rotated, rot := garden.RotateLedger(reqs, seen, head, opts.MinAge, now)
				if len(rotated) > opts.Requests {
					rotated = rotated[:opts.Requests]
				}
				j.Ledger, j.LedgerRotation = rotated, rot
			}
		}
	}
	if opts.Ghosts > 0 {
		stored, err := st.GhostTree(project, "")
		if err != nil {
			fmt.Fprintf(stdout, "cannot read descriptions: %v (the description job is skipped)\n", err)
			return j
		}
		described := make(map[string]store.GhostFile, len(stored))
		for _, g := range stored {
			described[g.Path] = g
		}
		var empty map[string]bool
		if reviews, err := st.GhostReviews(project, ""); err == nil {
			empty = ghost.ReviewedEmpty(repo.Root, reviews)
		}
		existing, err := st.ProjectKnowledge(project, true)
		if err != nil {
			fmt.Fprintf(stdout, "cannot read existing proposals: %v (the description job is skipped)\n", err)
			return j
		}
		seen := gardenSeen(opts.State, garden.GhostState(project), stdout)
		j.Ghosts, j.GhostRotation = garden.SelectGhosts(repo, described, empty, seen, existing, opts.Ghosts)
		j.Samples = garden.PickSamples(described, j.Ghosts)
	}
	return j
}

// gardenSeen reads the check memory under one name; without a memory nothing was seen.
func gardenSeen(state gardenState, name string, stdout io.Writer) map[int64]garden.Seen {
	seen := map[int64]garden.Seen{}
	if state == nil {
		return seen
	}
	checks, err := state.GardenChecks(name)
	if err != nil {
		fmt.Fprintf(stdout, "cannot read earlier checks (%s): %v\n", name, err)
		return seen
	}
	for id, c := range checks {
		seen[id] = garden.Seen{CheckedAt: c.CheckedAt, Commit: c.Commit, EntryHash: c.EntryHash, Verdict: c.Verdict}
	}
	return seen
}
