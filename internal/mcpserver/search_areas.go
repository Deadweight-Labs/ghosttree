package mcpserver

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// defaultAreaBudget is how long one area of context_search may take. The areas
// run at the same time, so this is also about how long the whole search may
// take; it stays below the client's own limit so that a slow area is named as
// missing instead of the call failing as a whole.
const defaultAreaBudget = 20 * time.Second

// searchArea is one part of a search over several areas: knowledge, sessions,
// requests or files. run answers with its rendered section, empty when it found
// nothing.
type searchArea struct {
	name string
	run  func(ctx context.Context) (string, error)
}

func (s *Server) areaBudget() time.Duration {
	if s.searchBudget > 0 {
		return s.searchBudget
	}
	return defaultAreaBudget
}

// runSearchAreas runs the areas at the same time, each within budget, and joins
// their sections in the order given. An area that fails costs only its own
// section: the others are answered, and one line at the end says which area is
// missing and why. Only when a single area was asked for, or every area failed,
// is the failure the answer.
func runSearchAreas(ctx context.Context, areas []searchArea, budget time.Duration) (string, error) {
	type result struct {
		text string
		err  error
	}
	results := make([]result, len(areas))
	var wg sync.WaitGroup
	for i, a := range areas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			actx, cancel := context.WithTimeout(ctx, budget)
			defer cancel()
			text, err := a.run(actx)
			if err != nil && errors.Is(actx.Err(), context.DeadlineExceeded) {
				err = errors.Join(context.DeadlineExceeded, err)
			}
			results[i] = result{text, err}
		}()
	}
	wg.Wait()

	var sections []string
	var gaps []store.SearchGap
	var first error
	for i, r := range results {
		if r.err != nil {
			if first == nil {
				first = r.err
			}
			gaps = append(gaps, store.SearchGap{Area: areas[i].name, Reason: gapReason(r.err)})
			continue
		}
		if r.text != "" {
			sections = append(sections, r.text)
		}
	}
	if first != nil && (len(areas) == 1 || len(gaps) == len(areas)) {
		return "", first
	}
	out := strings.Join(sections, "\n")
	if note := renderGaps(gaps); note != "" {
		if out == "" {
			out = "(nothing found in the areas that answered)\n"
		}
		out += "\n" + note
	}
	return out, nil
}

// gapReason says in a few words why an area gave no answer. It never repeats
// the error itself: that may be a whole proxy error page, and the reader only
// needs to know that the area is missing and whether trying again may help.
func gapReason(err error) string {
	var se *client.StatusError
	var ae *client.APIError
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return store.GapTimeout
	case errors.As(err, &se):
		return "server answered " + strconv.Itoa(se.Status)
	case errors.As(err, &ae):
		if ae.Code != "" {
			return ae.Code
		}
		return "server answered " + strconv.Itoa(ae.Status)
	}
	return store.GapFailed
}

// renderGaps is the one line that names the areas missing from an answer.
func renderGaps(gaps []store.SearchGap) string {
	if len(gaps) == 0 {
		return ""
	}
	parts := make([]string, len(gaps))
	for i, g := range gaps {
		reason := g.Reason
		if reason == store.GapTimeout {
			reason = "timed out"
		}
		parts[i] = g.Area + ": " + reason
	}
	return "(incomplete — " + strings.Join(parts, "; ") + ". The other areas are complete; to retry one, call context_search with kind set to it.)\n"
}
