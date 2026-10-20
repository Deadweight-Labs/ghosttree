package main

import (
	"flag"
	"os"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// FTSResult is the answer of the production session search for one query,
// reduced to session ids.
type FTSResult struct {
	ID string `json:"id"`
	// Deep is the distinct sessions in the order of the first hit, from a search
	// with a limit of Deep rows (hit rows: one session can fill many of them).
	Deep []int64 `json:"deep"`
	// Prod20 is the same for the limit of 20 that /api/search uses by default.
	Prod20 []int64 `json:"prod20"`
	Rows   int     `json:"rows"`
	Ms     int64   `json:"ms"`
}

func distinct(hits []store.SessionHit) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, h := range hits {
		if !seen[h.Session.ID] {
			seen[h.Session.ID] = true
			out = append(out, h.Session.ID)
		}
	}
	return out
}

// cmdFTS runs the real store function of the production, read-only, from the
// view of the instance owner: no row rule, no prefilter, the whole index. The
// queries come on standard input.
func cmdFTS(args []string) error {
	fs := flag.NewFlagSet("fts", flag.ExitOnError)
	dbPath := fs.String("db", "", "database")
	deep := fs.Int("deep", 500, "row limit of the deep search")
	verify := fs.Int("verify", 25, "queries for which the limit-20 search is run for real")
	fs.Parse(args)
	qs, err := readJSONL[Query](os.Stdin)
	if err != nil {
		return err
	}
	st, err := store.OpenReadOnly(*dbPath, 2)
	if err != nil {
		return err
	}
	defer st.Close()
	var out []FTSResult
	mismatch := 0
	for i, q := range qs {
		t0 := time.Now()
		hits, err := st.SearchSessionsVisible(q.Text, scope.Axes{}, "", *deep, nil, store.SessionPrefilter{})
		if err != nil {
			return err
		}
		r := FTSResult{ID: q.ID, Deep: distinct(hits), Rows: len(hits), Ms: time.Since(t0).Milliseconds()}
		first := hits
		if len(first) > 20 {
			first = first[:20]
		}
		r.Prod20 = distinct(first)
		if i < *verify {
			real20, err := st.SearchSessionsVisible(q.Text, scope.Axes{}, "", 20, nil, store.SessionPrefilter{})
			if err != nil {
				return err
			}
			a, b := distinct(real20), r.Prod20
			same := len(a) == len(b)
			for k := 0; same && k < len(a); k++ {
				same = a[k] == b[k]
			}
			if !same {
				mismatch++
			}
		}
		out = append(out, r)
		if i%50 == 49 {
			logf("fts %d/%d", i+1, len(qs))
		}
	}
	logf("limit-20 search differs from the prefix of the deep search in %d of %d verified queries", mismatch, min(*verify, len(qs)))
	return writeJSONL(os.Stdout, out)
}
