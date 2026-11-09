package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// sessionsBudget is how long the session area of a search over several areas
// may take. Transcripts are by far the largest index, and a common word there
// can take longer than everything else together; past the budget the other
// areas are answered and the sessions are named as missing, instead of the
// caller giving up on all of them.
const sessionsBudget = 8 * time.Second

// searchGaps collects the areas of a search that failed. With several areas
// asked for, a failed one becomes a gap and the others are still answered; with
// one area its failure is the answer, as before.
type searchGaps struct {
	a     *api
	r     *http.Request
	multi bool
	list  []store.SearchGap
	first error
	// ok counts the areas that answered.
	ok int
}

func (a *api) newSearchGaps(r *http.Request, multi bool) *searchGaps {
	return &searchGaps{a: a, r: r, multi: multi}
}

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
// With one area there is no budget of its own: the request's time is the area's.
func (g *searchGaps) budget() (*store.Store, func(error) error) {
	if !g.multi {
		return g.a.st, func(err error) error { return err }
	}
	ctx, cancel := context.WithTimeout(g.r.Context(), sessionsBudget)
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
