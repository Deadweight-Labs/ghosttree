package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
)

type sysList func(q int) []int64

type metrics struct {
	N        int
	R1, R3   float64
	R10, MRR float64
	rr       []float64 // reciprocal rank within 10 per query
	hit10    []float64
}

func rankOf(list []int64, targets map[int64]bool) int {
	for i, id := range list {
		if targets[id] {
			return i + 1
		}
	}
	return 0
}

func computeMetrics(ranks []int) metrics {
	m := metrics{N: len(ranks)}
	for _, r := range ranks {
		rr, h := 0.0, 0.0
		if r >= 1 && r <= 10 {
			rr, h = 1/float64(r), 1
		}
		if r == 1 {
			m.R1++
		}
		if r >= 1 && r <= 3 {
			m.R3++
		}
		m.R10 += h
		m.MRR += rr
		m.rr = append(m.rr, rr)
		m.hit10 = append(m.hit10, h)
	}
	if m.N > 0 {
		n := float64(m.N)
		m.R1, m.R3, m.R10, m.MRR = m.R1/n*100, m.R3/n*100, m.R10/n*100, m.MRR/n*100
	}
	return m
}

// bootstrap returns the 95 % percentile interval of the mean of x (in percent)
// and, with base, of the mean of x-base.
func bootstrap(x, base []float64, draws int, seed uint64) (lo, hi float64) {
	n := len(x)
	if n == 0 {
		return 0, 0
	}
	r := newRand(seed)
	means := make([]float64, draws)
	for d := 0; d < draws; d++ {
		var s float64
		for i := 0; i < n; i++ {
			k := r.IntN(n)
			if base != nil {
				s += x[k] - base[k]
			} else {
				s += x[k]
			}
		}
		means[d] = s / float64(n) * 100
	}
	sort.Float64s(means)
	return means[int(0.025*float64(draws))], means[int(0.975*float64(draws))-1]
}

type vecIndex struct {
	items []int    // item idx, only embedded ones
	data  [][]byte // int8 as bytes
	scale []float32
	sid   []int64
	kind  []string
}

// weightedFuse is reciprocal rank fusion with a weight per list.
func weightedFuse(wText, wVec float64, text, vec []int64) []embed.Fused {
	type acc struct {
		score float64
		ranks [2]int
	}
	byID := map[int64]*acc{}
	var order []int64
	add := func(list []int64, li int, w float64) {
		for i, id := range list {
			a := byID[id]
			if a == nil {
				a = &acc{}
				byID[id] = a
				order = append(order, id)
			}
			if a.ranks[li] != 0 {
				continue
			}
			a.ranks[li] = i + 1
			a.score += w / float64(embed.RRFK+i+1)
		}
	}
	add(text, 0, wText)
	add(vec, 1, wVec)
	out := make([]embed.Fused, 0, len(order))
	for _, id := range order {
		a := byID[id]
		out = append(out, embed.Fused{ID: id, Score: a.score, Ranks: []int{a.ranks[0], a.ranks[1]}})
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Score != out[b].Score {
			return out[a].Score > out[b].Score
		}
		ra, rb := out[a].Ranks[0], out[b].Ranks[0]
		if ra != rb {
			if ra == 0 {
				return false
			}
			if rb == 0 {
				return true
			}
			return ra < rb
		}
		return out[a].ID < out[b].ID
	})
	return out
}

// capSemantic applies the production rule: at most five hits that only the
// embedding found among ten.
func capSemantic(f []embed.Fused, limit int) []int64 {
	room := min(5, (limit+1)/2)
	var out []int64
	for _, x := range f {
		if len(out) >= limit {
			break
		}
		if x.Ranks[0] == 0 {
			if room == 0 {
				continue
			}
			room--
		}
		out = append(out, x.ID)
	}
	return out
}

func cmdScore(args []string) error {
	fs := flag.NewFlagSet("score", flag.ExitOnError)
	itemsPath := fs.String("items", "", "items")
	vecsPath := fs.String("vecs", "", "vectors")
	qvecsPath := fs.String("qvecs", "", "query vectors")
	queriesArg := fs.String("queries", "", "title query file,own query file")
	ftsPath := fs.String("fts", "", "fts results")
	planPath := fs.String("plan", "", "plan")
	outDir := fs.String("out", ".", "output directory")
	draws := fs.Int("boot", 1000, "bootstrap draws")
	floor := fs.Float64("floor", 0.55, "minimum cosine of the production")
	nsArg := fs.String("ns", "0,100,200,400,-1", "distractor counts of the scaling table (-1: all complete)")
	fs.Parse(args)

	items, err := readJSONLFile[Item](*itemsPath)
	if err != nil {
		return err
	}
	vecs, err := readVecs(*vecsPath)
	if err != nil {
		return err
	}
	var plan Plan
	b, err := os.ReadFile(*planPath)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &plan); err != nil {
		return err
	}
	var queries []Query
	for _, p := range strings.Split(*queriesArg, ",") {
		qs, err := readJSONLFile[Query](p)
		if err != nil {
			return err
		}
		queries = append(queries, qs...)
	}
	qv, err := readJSONLFile[queryVec](*qvecsPath)
	if err != nil {
		return err
	}
	qvec := map[string][]float32{}
	for _, v := range qv {
		qvec[v.ID] = v.V
	}
	ftsRes, err := readJSONLFile[FTSResult](*ftsPath)
	if err != nil {
		return err
	}
	ftsBy := map[string]FTSResult{}
	for _, r := range ftsRes {
		ftsBy[r.ID] = r
	}

	// Which sessions are complete.
	bySess := map[int64][]int{}
	for _, it := range items {
		bySess[it.SID] = append(bySess[it.SID], it.Idx)
	}
	complete := func(sid int64) bool {
		for _, idx := range bySess[sid] {
			if _, ok := vecs[idx]; !ok {
				return false
			}
		}
		return len(bySess[sid]) > 0
	}
	var targets []int64
	for _, sid := range plan.Targets {
		if complete(sid) {
			targets = append(targets, sid)
		}
	}
	var dist []int64
	for _, sid := range plan.Distractors {
		if !complete(sid) {
			break
		}
		dist = append(dist, sid)
	}
	logf("complete: %d of %d target sessions, %d distractor sessions (in plan order)", len(targets), len(plan.Targets), len(dist))

	// All embedded items of complete sessions.
	inScope := map[int64]bool{}
	for _, s := range targets {
		inScope[s] = true
	}
	for _, s := range dist {
		inScope[s] = true
	}
	var ix vecIndex
	for _, it := range items {
		v, ok := vecs[it.Idx]
		if !ok || !inScope[it.SID] {
			continue
		}
		raw := make([]byte, dim)
		for i, x := range v.Q {
			raw[i] = byte(x)
		}
		ix.items = append(ix.items, it.Idx)
		ix.data = append(ix.data, raw)
		ix.scale = append(ix.scale, v.Scale)
		ix.sid = append(ix.sid, it.SID)
		ix.kind = append(ix.kind, it.Kind)
	}
	logf("%d embedded items in scope", len(ix.items))

	// Cosine of every query with every item, once.
	sims := make([][]float32, len(queries))
	for qi, q := range queries {
		v, ok := qvec[q.ID]
		if !ok {
			return fmt.Errorf("no vector for query %s", q.ID)
		}
		s := make([]float32, len(ix.items))
		for i := range ix.items {
			s[i] = embed.Cosine(v, ix.data[i], ix.scale[i])
		}
		sims[qi] = s
	}

	type corpus struct {
		name    string
		n       int
		inC     map[int64]bool
		nitems  int
		results map[string][]int // system -> rank per query
		lists   map[string][][]int64
	}
	build := func(nd int) *corpus {
		c := &corpus{inC: map[int64]bool{}, results: map[string][]int{}, lists: map[string][][]int64{}}
		for _, s := range targets {
			c.inC[s] = true
		}
		if nd < 0 || nd > len(dist) {
			nd = len(dist)
		}
		for _, s := range dist[:nd] {
			c.inC[s] = true
		}
		c.n = len(c.inC)
		c.name = fmt.Sprintf("%d Sessions (%d Ziele + %d Ablenker)", c.n, len(targets), nd)
		for _, sid := range ix.sid {
			if c.inC[sid] {
				c.nitems++
			}
		}
		vecList := func(qi int, kinds string, fl float64) []int64 {
			best := map[int64]float32{}
			for i, sid := range ix.sid {
				if !c.inC[sid] {
					continue
				}
				if kinds != "" && ix.kind[i] != kinds {
					continue
				}
				if s := sims[qi][i]; float64(s) >= fl {
					if cur, ok := best[sid]; !ok || s > cur {
						best[sid] = s
					}
				}
			}
			type ss struct {
				sid int64
				s   float32
			}
			all := make([]ss, 0, len(best))
			for sid, s := range best {
				all = append(all, ss{sid, s})
			}
			sort.Slice(all, func(a, b int) bool {
				if all[a].s != all[b].s {
					return all[a].s > all[b].s
				}
				return all[a].sid < all[b].sid
			})
			out := make([]int64, len(all))
			for i, x := range all {
				out[i] = x.sid
			}
			return out
		}
		for qi, q := range queries {
			f := ftsBy[q.ID]
			var fc []int64
			for _, sid := range f.Deep {
				if c.inC[sid] {
					fc = append(fc, sid)
				}
			}
			vall, vp, vf, vfl := vecList(qi, "", 0), vecList(qi, "prompt", 0), vecList(qi, "final", 0), vecList(qi, "", *floor)
			pool := func(l []int64) []int64 { return l[:min(100, len(l))] }
			put := func(name string, l []int64) { c.lists[name] = append(c.lists[name], l) }
			put("fts_full", f.Deep)
			put("fts_prod20", f.Prod20)
			put("fts_corpus", fc)
			put("vec", vall)
			put("vec_prompt", vp)
			put("vec_final", vf)
			put("vec_floor", vfl)
			put("hyb_cap", capSemantic(embed.Fuse(pool(fc), pool(vall)), 10))
			put("hyb_nocap", fusedIDs(embed.Fuse(pool(fc), pool(vall))))
			put("hyb_floor_cap", capSemantic(embed.Fuse(pool(fc), pool(vfl)), 10))
			for _, w := range []float64{0.5, 2, 3} {
				put("hyb_w"+strconv.FormatFloat(w, 'f', -1, 64), fusedIDs(weightedFuse(1, w, pool(fc), pool(vall))))
			}
			put("hyb_prompt_cap", capSemantic(embed.Fuse(pool(fc), pool(vp)), 10))
		}
		for name, ls := range c.lists {
			ranks := make([]int, len(queries))
			for qi, q := range queries {
				t := map[int64]bool{}
				for _, x := range q.Targets {
					t[x] = true
				}
				ranks[qi] = rankOf(ls[qi], t)
			}
			c.results[name] = ranks
		}
		return c
	}

	sets := []string{"title", "own"}
	idxOf := func(set string, pred func(Query) bool) []int {
		var out []int
		for i, q := range queries {
			if q.Set == set && (pred == nil || pred(q)) {
				out = append(out, i)
			}
		}
		return out
	}
	pick := func(ranks []int, idx []int) []int {
		out := make([]int, len(idx))
		for i, k := range idx {
			out[i] = ranks[k]
		}
		return out
	}

	label := map[string]string{
		"fts_full":       "FTS heute, ganzer Index (Produktion)",
		"fts_prod20":     "FTS heute, nur die 20 Zeilen des Standards",
		"fts_corpus":     "FTS, auf den Korpus beschränkt",
		"vec":            "Vektor (max. Ähnlichkeit je Session)",
		"vec_prompt":     "Vektor, nur Prompts",
		"vec_final":      "Vektor, nur finale Antworten",
		"vec_floor":      fmt.Sprintf("Vektor, Mindestähnlichkeit %.2f", *floor),
		"hyb_cap":        "Hybrid RRF, Produktionsregel (höchstens 5 reine Vektortreffer)",
		"hyb_nocap":      "Hybrid RRF, ohne Obergrenze",
		"hyb_floor_cap":  fmt.Sprintf("Hybrid RRF, Produktionsregel, Mindestähnlichkeit %.2f", *floor),
		"hyb_w0.5":       "Hybrid RRF, Vektor Gewicht 0,5",
		"hyb_w2":         "Hybrid RRF, Vektor Gewicht 2",
		"hyb_w3":         "Hybrid RRF, Vektor Gewicht 3",
		"hyb_prompt_cap": "Hybrid RRF, Produktionsregel, nur Prompts",
	}
	order := []string{"fts_full", "fts_prod20", "fts_corpus", "vec", "vec_prompt", "vec_final", "vec_floor",
		"hyb_cap", "hyb_nocap", "hyb_floor_cap", "hyb_w0.5", "hyb_w2", "hyb_w3", "hyb_prompt_cap"}

	// Main corpus: everything that is complete.
	main := build(-1)
	var md strings.Builder
	res := map[string]any{"corpus": main.name, "sessions": main.n, "items": main.nitems,
		"targets_complete": len(targets), "distractors": len(dist)}
	fmt.Fprintf(&md, "Korpus: %s, %d Texte.\n\n", main.name, main.nitems)

	table := func(c *corpus, idx []int, title string, systems []string, base string) {
		fmt.Fprintf(&md, "%s (n=%d)\n\n| System | R@1 | R@3 | R@10 | MRR@10 [95 %%-KI] | ΔMRR vs %s [95 %%-KI] | ΔMRR vs FTS heute, ganzer Index [95 %%-KI] |\n|---|---|---|---|---|---|---|\n", title, len(idx), label[base])
		baseM := computeMetrics(pick(c.results[base], idx))
		prodM := computeMetrics(pick(c.results["fts_full"], idx))
		for _, name := range systems {
			m := computeMetrics(pick(c.results[name], idx))
			lo, hi := bootstrap(m.rr, nil, *draws, 11)
			delta, delta2 := "–", "–"
			if name != base {
				dlo, dhi := bootstrap(m.rr, baseM.rr, *draws, 12)
				delta = fmt.Sprintf("%+.1f [%+.1f, %+.1f]", m.MRR-baseM.MRR, dlo, dhi)
			}
			if name != "fts_full" {
				dlo, dhi := bootstrap(m.rr, prodM.rr, *draws, 13)
				delta2 = fmt.Sprintf("%+.1f [%+.1f, %+.1f]", m.MRR-prodM.MRR, dlo, dhi)
			}
			fmt.Fprintf(&md, "| %s | %.1f | %.1f | %.1f | %.1f [%.1f, %.1f] | %s | %s |\n", label[name], m.R1, m.R3, m.R10, m.MRR, lo, hi, delta, delta2)
			res[title+"/"+name] = map[string]float64{"n": float64(len(idx)), "R1": m.R1, "R3": m.R3, "R10": m.R10, "MRR": m.MRR, "MRR_lo": lo, "MRR_hi": hi}
		}
		md.WriteString("\n")
	}
	for _, set := range sets {
		idx := idxOf(set, nil)
		table(main, idx, "Anfragen "+set, order, "fts_corpus")
	}
	// Subsets of the title queries.
	single := idxOf("title", func(q Query) bool { return len(q.Targets) == 1 })
	multi := idxOf("title", func(q Query) bool { return len(q.Targets) > 1 })
	core := []string{"fts_corpus", "vec", "hyb_cap", "hyb_nocap"}
	table(main, single, "Titel-Anfragen mit genau einer Ziel-Session", core, "fts_corpus")
	table(main, multi, "Titel-Anfragen mit mehreren Ziel-Sessions", core, "fts_corpus")

	// Complementarity.
	for _, set := range sets {
		idx := idxOf(set, nil)
		for _, sys := range []string{"vec", "hyb_cap"} {
			var both, onlyF, onlyS, none int
			for _, k := range idx {
				f := main.results["fts_corpus"][k]
				s := main.results[sys][k]
				fh, sh := f >= 1 && f <= 10, s >= 1 && s <= 10
				switch {
				case fh && sh:
					both++
				case fh:
					onlyF++
				case sh:
					onlyS++
				default:
					none++
				}
			}
			n := float64(len(idx))
			fmt.Fprintf(&md, "Komplementarität %s, %s gegen FTS (Ziel in den Top 10; Anteil der Anfragen in %%): beide %.1f, nur FTS %.1f, nur %s %.1f, keiner %.1f\n\n",
				set, label[sys], float64(both)/n*100, float64(onlyF)/n*100, label[sys], float64(onlyS)/n*100, float64(none)/n*100)
		}
	}

	// Scaling: the same queries over a growing number of distractors.
	var ns []int
	for _, s := range strings.Split(*nsArg, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return err
		}
		ns = append(ns, n)
	}
	fmt.Fprintf(&md, "Skalierung (gleiche Anfragen, wachsender Korpus; MRR@10 / R@10 in %%):\n\n| Korpus | Texte | Anfragen | FTS (Korpus) | Vektor | Hybrid (Produktionsregel) |\n|---|---|---|---|---|---|\n")
	for _, n := range ns {
		c := build(n)
		for _, set := range sets {
			idx := idxOf(set, nil)
			f := computeMetrics(pick(c.results["fts_corpus"], idx))
			v := computeMetrics(pick(c.results["vec"], idx))
			h := computeMetrics(pick(c.results["hyb_cap"], idx))
			fmt.Fprintf(&md, "| %d Sessions | %d | %s | %.1f / %.1f | %.1f / %.1f | %.1f / %.1f |\n", c.n, c.nitems, set, f.MRR, f.R10, v.MRR, v.R10, h.MRR, h.R10)
			res[fmt.Sprintf("scale/%d/%s", c.n, set)] = map[string]float64{"fts_mrr": f.MRR, "fts_r10": f.R10, "vec_mrr": v.MRR, "vec_r10": v.R10, "hyb_mrr": h.MRR, "hyb_r10": h.R10}
		}
	}
	md.WriteString("\n")

	// Production FTS speed.
	var ms []float64
	for _, r := range ftsRes {
		ms = append(ms, float64(r.Ms))
	}
	p := percentiles(ms, 0.5, 0.95, 1)
	fmt.Fprintf(&md, "FTS-Suche der Produktion (ganzer Index, Limit 500 Zeilen): Median %.0f ms, p95 %.0f ms, max %.0f ms über %d Anfragen.\n\n", p[0], p[1], p[2], len(ms))

	// Hit position of the production list: how many sessions the 20 rows hold.
	var n20 []float64
	for _, r := range ftsRes {
		n20 = append(n20, float64(len(r.Prod20)))
	}
	q20 := percentiles(n20, 0.1, 0.5, 0.9)
	fmt.Fprintf(&md, "Die 20 Zeilen des Standards enthalten im Median %.0f verschiedene Sessions (p10 %.0f, p90 %.0f).\n\n", q20[1], q20[0], q20[2])

	// Per-query ranks (ids and ranks only).
	var csv strings.Builder
	csv.WriteString("id,set,ntargets")
	for _, n := range order {
		csv.WriteString("," + n)
	}
	csv.WriteString("\n")
	for qi, q := range queries {
		fmt.Fprintf(&csv, "%s,%s,%d", q.ID, q.Set, len(q.Targets))
		for _, n := range order {
			fmt.Fprintf(&csv, ",%d", main.results[n][qi])
		}
		csv.WriteString("\n")
	}
	if err := os.WriteFile(*outDir+"/ranks.csv", []byte(csv.String()), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(*outDir+"/tables.md", []byte(md.String()), 0o600); err != nil {
		return err
	}
	rb, _ := json.MarshalIndent(res, "", " ")
	if err := os.WriteFile(*outDir+"/results.json", rb, 0o600); err != nil {
		return err
	}
	fmt.Print(md.String())
	_ = math.Pi
	return nil
}

func fusedIDs(f []embed.Fused) []int64 {
	out := make([]int64, len(f))
	for i, x := range f {
		out[i] = x.ID
	}
	return out
}
