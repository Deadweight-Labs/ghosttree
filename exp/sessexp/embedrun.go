package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
)

const dim = 768

// ------------------------------------------------------------------ plan

// Plan is the order in which items are embedded. The first Bench items are the
// sample for the timing, then the rest of the target sessions, then the
// distractor sessions in random order, one session at a time.
type Plan struct {
	Order       []int   `json:"order"`
	Bench       int     `json:"bench"`
	TargetEnd   int     `json:"target_end"` // Order[:TargetEnd] are the items of target sessions
	Targets     []int64 `json:"targets"`
	Distractors []int64 `json:"distractors"` // random order
}

func cmdPlan(args []string) error {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	itemsPath := fs.String("items", "", "items")
	queries := fs.String("queries", "", "comma separated query files")
	seed := fs.Uint64("seed", 20261022, "seed")
	bench := fs.Int("bench", 104, "items of the timing sample")
	out := fs.String("out", "", "plan file")
	fs.Parse(args)
	items, err := readJSONLFile[Item](*itemsPath)
	if err != nil {
		return err
	}
	bySession := map[int64][]int{}
	var sids []int64
	for _, it := range items {
		if _, ok := bySession[it.SID]; !ok {
			sids = append(sids, it.SID)
		}
		bySession[it.SID] = append(bySession[it.SID], it.Idx)
	}
	isTarget := map[int64]bool{}
	for _, p := range strings.Split(*queries, ",") {
		if p == "" {
			continue
		}
		qs, err := readJSONLFile[Query](p)
		if err != nil {
			return err
		}
		for _, q := range qs {
			for _, t := range q.Targets {
				isTarget[t] = true
			}
		}
	}
	var plan Plan
	var targetItems []int
	for _, sid := range sids {
		if isTarget[sid] {
			plan.Targets = append(plan.Targets, sid)
			targetItems = append(targetItems, bySession[sid]...)
		}
	}
	targetItems = shuffled(targetItems, *seed)
	if *bench > len(targetItems) {
		*bench = len(targetItems)
	}
	plan.Bench = *bench
	head := targetItems[:*bench]
	rest := append([]int(nil), targetItems[*bench:]...)
	sort.Ints(rest)
	plan.Order = append(append(plan.Order, head...), rest...)
	plan.TargetEnd = len(plan.Order)
	var dist []int64
	for _, sid := range sids {
		if !isTarget[sid] {
			dist = append(dist, sid)
		}
	}
	plan.Distractors = shuffled(dist, *seed+1)
	for _, sid := range plan.Distractors {
		plan.Order = append(plan.Order, bySession[sid]...)
	}
	b, _ := json.Marshal(plan)
	logf("plan: %d target sessions (%d items), %d distractor sessions (%d items)",
		len(plan.Targets), plan.TargetEnd, len(plan.Distractors), len(plan.Order)-plan.TargetEnd)
	return os.WriteFile(*out, b, 0o600)
}

// ------------------------------------------------------------------ vectors file

// A record is idx (int32), scale (float32) and dim int8 values.
const recLen = 4 + 4 + dim

type Vec struct {
	Idx   int
	Scale float32
	Q     []int8
}

func readVecs(path string) (map[int]Vec, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[int]Vec{}, nil
		}
		return nil, err
	}
	defer f.Close()
	out := map[int]Vec{}
	br := bufio.NewReaderSize(f, 1<<20)
	buf := make([]byte, recLen)
	for {
		if _, err := io.ReadFull(br, buf); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return out, nil
			}
			return nil, err
		}
		v := Vec{Idx: int(int32(binary.LittleEndian.Uint32(buf[0:4]))), Q: make([]int8, dim)}
		v.Scale = float32frombits(binary.LittleEndian.Uint32(buf[4:8]))
		for i := 0; i < dim; i++ {
			v.Q[i] = int8(buf[8+i])
		}
		out[v.Idx] = v
	}
}

func appendVec(w io.Writer, idx int, scale float32, q []byte) error {
	buf := make([]byte, recLen)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(int32(idx)))
	binary.LittleEndian.PutUint32(buf[4:8], float32bits(scale))
	copy(buf[8:], q)
	_, err := w.Write(buf)
	return err
}

// ------------------------------------------------------------------ http

type embedResp struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// post sends the texts as one request and returns unit vectors and the token count.
func post(ctx context.Context, hc *http.Client, base, model string, texts []string) ([][]float32, int, error) {
	body, _ := json.Marshal(map[string]any{"input": texts, "model": model})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, 0, fmt.Errorf("status %d: %s", resp.StatusCode, strings.Join(strings.Fields(string(b)), " "))
	}
	var r embedResp
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&r); err != nil {
		return nil, 0, err
	}
	if len(r.Data) != len(texts) {
		return nil, 0, fmt.Errorf("%d vectors for %d texts", len(r.Data), len(texts))
	}
	sort.Slice(r.Data, func(a, b int) bool { return r.Data[a].Index < r.Data[b].Index })
	out := make([][]float32, len(texts))
	for i, d := range r.Data {
		if len(d.Embedding) != dim {
			return nil, 0, fmt.Errorf("vector of %d values", len(d.Embedding))
		}
		if err := embed.Normalize(d.Embedding); err != nil {
			return nil, 0, err
		}
		out[i] = d.Embedding
	}
	return out, r.Usage.PromptTokens, nil
}

// ------------------------------------------------------------------ embed

type queryVec struct {
	ID string    `json:"id"`
	V  []float32 `json:"v"`
}

func cmdEmbed(args []string) error {
	fs := flag.NewFlagSet("embed", flag.ExitOnError)
	url := fs.String("url", "http://127.0.0.1:8475", "job server (never the query server on 8476)")
	itemsPath := fs.String("items", "", "items")
	planPath := fs.String("plan", "", "plan")
	out := fs.String("out", "", "vectors file (appended, resumable)")
	timing := fs.String("timing", "", "timing csv (appended)")
	queries := fs.String("queries", "", "comma separated query files; with -qout, embed these instead of items")
	qout := fs.String("qout", "", "query vectors file")
	batch := fs.Int("batch", 8, "texts per request")
	pause := fs.Duration("pause", time.Second, "pause between requests")
	budget := fs.Duration("budget", 0, "stop after this long (0: no limit)")
	maxItems := fs.Int("max-items", 0, "stop after this many items of the plan (0: all)")
	fs.Parse(args)
	if strings.Contains(*url, ":8476") {
		return fmt.Errorf("8476 is the query server of the production; refusing")
	}
	base, err := embed.ParseURL(*url)
	if err != nil {
		return err
	}
	hc := &http.Client{Transport: &http.Transport{Proxy: nil}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *qout != "" {
		var qs []Query
		for _, p := range strings.Split(*queries, ",") {
			if p == "" {
				continue
			}
			more, err := readJSONLFile[Query](p)
			if err != nil {
				return err
			}
			qs = append(qs, more...)
		}
		f, err := os.OpenFile(*qout, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		enc := json.NewEncoder(f)
		start := time.Now()
		for i, q := range qs {
			vs, _, err := post(ctx, hc, base, embed.DefaultModel, []string{embed.QueryText(q.Text)})
			if err != nil {
				return fmt.Errorf("query %s: %w", q.ID, err)
			}
			if err := enc.Encode(queryVec{ID: q.ID, V: vs[0]}); err != nil {
				return err
			}
			if i%50 == 49 {
				logf("queries %d/%d after %s", i+1, len(qs), time.Since(start).Round(time.Second))
			}
			time.Sleep(200 * time.Millisecond)
		}
		logf("queries done: %d in %s", len(qs), time.Since(start).Round(time.Second))
		return nil
	}

	items, err := readJSONLFile[Item](*itemsPath)
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
	done, err := readVecs(*out)
	if err != nil {
		return err
	}
	logf("resuming with %d vectors", len(done))
	vf, err := os.OpenFile(*out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer vf.Close()
	var tf *os.File
	if *timing != "" {
		if tf, err = os.OpenFile(*timing, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err != nil {
			return err
		}
		defer tf.Close()
	}
	start := time.Now()
	var todo []int
	for i, idx := range plan.Order {
		if *maxItems > 0 && i >= *maxItems {
			break
		}
		if _, ok := done[idx]; !ok {
			todo = append(todo, idx)
		}
	}
	logf("%d items to do", len(todo))
	embedded := 0
	for off := 0; off < len(todo); off += *batch {
		if ctx.Err() != nil {
			logf("interrupted")
			return nil
		}
		if *budget > 0 && time.Since(start) > *budget {
			logf("budget of %s used up after %d items", *budget, embedded)
			return nil
		}
		end := min(off+*batch, len(todo))
		var texts []string
		chars := 0
		for _, idx := range todo[off:end] {
			texts = append(texts, items[idx].Doc)
			chars += len(items[idx].Doc)
		}
		t0 := time.Now()
		var vecs [][]float32
		var tokens int
		for attempt := 0; ; attempt++ {
			vecs, tokens, err = post(ctx, hc, base, embed.DefaultModel, texts)
			if err == nil || attempt >= 4 || ctx.Err() != nil {
				break
			}
			logf("batch at %d failed (%v), retry", off, err)
			time.Sleep(time.Duration(attempt+1) * 10 * time.Second)
		}
		if err != nil {
			return fmt.Errorf("batch at %d: %w", off, err)
		}
		wall := time.Since(t0)
		for i, idx := range todo[off:end] {
			q, scale, err := embed.Quantize(vecs[i])
			if err != nil {
				return err
			}
			if err := appendVec(vf, idx, scale, q); err != nil {
				return err
			}
		}
		if tf != nil {
			fmt.Fprintf(tf, "%d,%d,%d,%d,%d\n", time.Now().Unix(), end-off, chars, tokens, wall.Milliseconds())
		}
		embedded += end - off
		if (off/(*batch))%25 == 24 {
			logf("%d/%d items, %s elapsed, last batch %dms", embedded, len(todo), time.Since(start).Round(time.Second), wall.Milliseconds())
		}
		time.Sleep(*pause)
	}
	logf("done: %d items in %s", embedded, time.Since(start).Round(time.Second))
	return nil
}
