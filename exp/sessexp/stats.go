package main

import (
	"encoding/json"
	"flag"
	"os"
	"sort"
	"unicode/utf8"
)

type lenDist struct {
	N       int     `json:"n"`
	Mean    float64 `json:"mean"`
	P10     float64 `json:"p10"`
	P50     float64 `json:"p50"`
	P90     float64 `json:"p90"`
	P99     float64 `json:"p99"`
	Max     float64 `json:"max"`
	CutShr  float64 `json:"share_cut"` // share of texts longer than the document budget
	DocMean float64 `json:"doc_mean"`  // mean runes of the document after the cut
	DocSum  int     `json:"doc_sum"`   // total runes after the cut
	Tools   int     `json:"followed_by_tool,omitempty"`
}

func dist(items []Item, kind string) lenDist {
	var full, doc []float64
	var d lenDist
	cut := 0
	for _, it := range items {
		if kind != "" && it.Kind != kind {
			continue
		}
		full = append(full, float64(it.Full))
		n := utf8.RuneCountInString(it.Doc)
		doc = append(doc, float64(n))
		d.DocSum += n
		if it.Full > 1980 {
			cut++
		}
		if it.Tools {
			d.Tools++
		}
	}
	d.N = len(full)
	if d.N == 0 {
		return d
	}
	ps := percentiles(full, 0.1, 0.5, 0.9, 0.99, 1)
	d.Mean, d.P10, d.P50, d.P90, d.P99, d.Max = mean(full), ps[0], ps[1], ps[2], ps[3], ps[4]
	d.CutShr = float64(cut) / float64(d.N)
	d.DocMean = mean(doc)
	return d
}

func cmdStats(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	itemsPath := fs.String("items", "", "items")
	sessions := fs.String("sessions", "", "sessions")
	fs.Parse(args)
	items, err := readJSONLFile[Item](*itemsPath)
	if err != nil {
		return err
	}
	metas, err := readJSONLFile[SessionMeta](*sessions)
	if err != nil {
		return err
	}
	started := map[int64]string{}
	for _, m := range metas {
		started[m.SID] = m.Started
	}
	per := map[int64]int{}
	perDay := map[string]int{}
	perDaySess := map[string]map[int64]bool{}
	for _, it := range items {
		per[it.SID]++
		day := started[it.SID]
		if len(day) >= 10 {
			day = day[:10]
			perDay[day]++
			if perDaySess[day] == nil {
				perDaySess[day] = map[int64]bool{}
			}
			perDaySess[day][it.SID] = true
		}
	}
	var counts []float64
	for _, n := range per {
		counts = append(counts, float64(n))
	}
	cp := percentiles(counts, 0.5, 0.9, 0.99, 1)
	var days []string
	for d := range perDay {
		days = append(days, d)
	}
	sort.Strings(days)
	last := map[string]int{}
	for i := len(days) - 1; i >= 0 && i >= len(days)-14; i-- {
		last[days[i]] = perDay[days[i]]
	}
	out := map[string]any{
		"all":                        dist(items, ""),
		"prompts":                    dist(items, "prompt"),
		"finals":                     dist(items, "final"),
		"sessions_with_items":        len(per),
		"items_per_session":          map[string]any{"mean": mean(counts), "p50": cp[0], "p90": cp[1], "p99": cp[2], "max": cp[3]},
		"items_per_start_day_last14": last,
		"days_with_sessions":         len(days),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", " ")
	return enc.Encode(out)
}
