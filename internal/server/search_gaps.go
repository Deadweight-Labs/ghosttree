package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// areaBudget is how long one area of a search over several areas may take, and
// searchBudget how long all of them may take together. A common word in the
// transcripts, the largest index, can take longer than everything else; past its
// budget an area is named as missing and the others are answered, instead of the
// caller giving up on all of them. The areas run one after the other, so the
// total ends the search before the client's own limit (30 s) does, whatever the
// number of areas.
const (
	areaBudget   = 8 * time.Second
	searchBudget = 24 * time.Second
)

// searchSlotWait is how long a search waits for a place among the searches of the
// server (Store.AcquireSearchSlot) before it answers search_busy with Retry-After;
// a variable only so that a test need not wait. Two seconds are a few of the usual
// searches, and well below what the client waits for an answer.
var searchSlotWait = 2 * time.Second

// areaBudgetFor is the budget of one area; a variable only so that a test can
// make one area run out of time and no other.
var areaBudgetFor = func(string) time.Duration { return areaBudget }

// searchGaps collects the areas of a search that failed. With several areas
// asked for, a failed one becomes a gap and the others are still answered; with
// one area its failure is the answer, as before.
type searchGaps struct {
	a *api
	r *http.Request
	// ctx ends the whole search at searchBudget; budget derives the one of an area.
	ctx    context.Context
	cancel context.CancelFunc
	multi  bool
	list   []store.SearchGap
	first  error
	// ok counts the areas that answered.
	ok int
}

// newSearchGaps starts the gaps of a search; the caller ends them with close.
func (a *api) newSearchGaps(r *http.Request, multi bool) *searchGaps {
	g := &searchGaps{a: a, r: r, multi: multi, ctx: r.Context(), cancel: func() {}}
	if multi {
		g.ctx, g.cancel = context.WithTimeout(r.Context(), searchBudget)
	}
	return g
}

// close ends the time limit of the search.
func (g *searchGaps) close() { g.cancel() }

// done records that an area answered.
func (g *searchGaps) done() { g.ok++ }

// add records the failure of an area. false means the search asked for one
// area only and the caller answers with the error.
func (g *searchGaps) add(area string, err error) bool {
	if !g.multi {
		return false
	}
	reason := store.GapFailed
	if errors.Is(err, context.DeadlineExceeded) {
		reason = store.GapTimeout
	}
	if g.first == nil {
		g.first = err
	}
	g.list = append(g.list, store.SearchGap{Area: area, Reason: reason})
	if g.a.logger != nil {
		g.a.logger.Warn("search_area_failed", "area", area, "reason", reason, "error", err.Error())
	}
	return true
}

// budget is the store for an area with its own time limit, and a function that
// ends it and turns an interruption caused by the limit into a deadline error.
// With one area there is no budget of its own: the request's time is the area's,
// and the store is bound to it, so that a caller who hangs up stops the SQL and
// frees the place and the connection it holds.
func (g *searchGaps) budget(area string) (*store.Store, func(error) error) {
	if !g.multi {
		return g.a.st.WithContext(g.ctx), func(err error) error { return err }
	}
	ctx, cancel := context.WithTimeout(g.ctx, areaBudgetFor(area))
	return g.a.st.WithContext(ctx), func(err error) error {
		defer cancel()
		if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errors.Join(context.DeadlineExceeded, err)
		}
		return err
	}
}

// allFailed is the first error when every area asked for failed: then there is
// nothing to answer with, and the request fails as a whole.
func (g *searchGaps) allFailed() error {
	if g.ok == 0 {
		return g.first
	}
	return nil
}
