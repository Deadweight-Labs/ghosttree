package searchquality_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embed/embedtest"
	"github.com/Deadweight-Labs/ghosttree/internal/embedjob"
	"github.com/Deadweight-Labs/ghosttree/internal/eval/searchquality"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type entry struct {
	title, body, quote, confidence, status string
}

var entries = []entry{
	{"Deploy order", "stop service install binary restart service", "first stop the service then install the new binary", "trusted", "active"},
	// The quote shares no word with the entry; only the embedding connects them.
	{"Wagen", "wagen kaputt reparieren werkstatt", "unser auto ist heute leider defekt gewesen", "trusted", "active"},
	{"Secret handling", "never print tokens keep secrets out of logs", "never print tokens in logs or output ever", "quarantined", "active"},
	{"Branch naming", "use feature slash short names for branches", "name branches feature slash something short", "trusted", "active"},
	// Not queries: too short, or backing an entry that is not active.
	{"Short quote entry", "something about brevity", "too short", "trusted", "active"},
	{"Gone", "an entry that was archived", "this quote backs an archived entry", "trusted", "archived"},
}

// fixture writes a database with the entries above and their quotes, embeds
// them when embedded is true, and returns the path and the fake server.
func fixture(t *testing.T, embedded bool) (string, *embedtest.Server, *embed.Client) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	srv := embedtest.New(t)
	srv.Dim = 256
	srv.Concepts = map[string]string{"wagen": "auto", "kaputt": "defekt"}
	c, err := embed.New(embed.Config{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := st.UpsertSession(store.Session{Harness: "test", ExternalID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range entries {
		id, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: e.title, Body: e.body, Confidence: e.confidence})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AddEvidence(id, []store.Evidence{{SessionID: sid, ChunkSeq: i, Quote: e.quote}}); err != nil {
			t.Fatal(err)
		}
		if e.status != "active" {
			if _, err := st.DB().Exec(`UPDATE knowledge SET status=? WHERE id=?`, e.status, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	if embedded {
		job := embedjob.New(st, c, embedjob.Config{Batch: 16, StartDelay: -1}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		for {
			res, err := job.Step(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if res == (embedjob.Result{}) {
				break
			}
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return path, srv, c
}

func open(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.OpenReadOnly(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestRunMeasuresFullTextVectorAndHybridOnTheProductionSearch(t *testing.T) {
	path, _, c := fixture(t, true)
	st := open(t, path)
	rep, err := searchquality.Run(context.Background(), st, searchquality.Options{Embedder: c})
	if err != nil {
		t.Fatal(err)
	}
	// Four quotes qualify: not the short one, not the one of the archived entry.
	if rep.Stock != 4 || rep.Sampled != 4 || rep.DistinctTargets != 4 || rep.QuarantinedTargets != 1 {
		t.Fatalf("report %+v", rep)
	}
	if rep.Embedded != 5 || rep.Active != 5 {
		t.Fatalf("coverage %d of %d", rep.Embedded, rep.Active)
	}
	all := rep.Blocks[0].Rows
	if all[searchquality.FullText].N != 4 || all[searchquality.Hybrid].N != 4 || all[searchquality.Vector].N != 4 {
		t.Fatalf("rows %+v", all)
	}
	// The full-text ranking cannot find the entry whose quote shares no word.
	if got := all[searchquality.FullText].R1; got != 0.75 {
		t.Fatalf("full text R@1 = %v, want 0.75", got)
	}
	if got := all[searchquality.Hybrid].R1; got != 1 {
		t.Fatalf("hybrid R@1 = %v, want 1", got)
	}
	if got := all[searchquality.Hybrid].MRR; got != 1 {
		t.Fatalf("hybrid MRR@10 = %v, want 1", got)
	}
	// The subset without quarantined targets, in the view that leaves those
	// entries out altogether.
	open, agent := rep.Blocks[1].Rows, rep.Blocks[2].Rows
	for name, rows := range map[string]map[searchquality.Mode]searchquality.Scores{"operator": open, "agent": agent} {
		if rows[searchquality.FullText].N != 3 || rows[searchquality.Hybrid].N != 3 {
			t.Fatalf("%s view: %+v", name, rows)
		}
		if rows[searchquality.Hybrid].R1 != 1 || rows[searchquality.FullText].R1 < 0.66 || rows[searchquality.FullText].R1 > 0.67 {
			t.Fatalf("%s view scores: %+v", name, rows)
		}
	}
}

func TestReportHoldsNoQuoteAndNoText(t *testing.T) {
	path, _, c := fixture(t, true)
	rep, err := searchquality.Run(context.Background(), open(t, path), searchquality.Options{Embedder: c})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	rep.Write(&out)
	text := out.String()
	for _, e := range entries {
		for _, secret := range []string{e.quote, e.title, e.body} {
			if strings.Contains(text, secret) {
				t.Fatalf("the report contains %q:\n%s", secret, text)
			}
		}
		for _, word := range strings.Fields(e.quote + " " + e.title) {
			if len(word) > 5 && strings.Contains(text, word) && word != "search" {
				t.Fatalf("the report contains the word %q of an entry:\n%s", word, text)
			}
		}
	}
	for _, want := range []string{"fulltext", "vector", "hybrid", "R@1", "R@3", "R@10", "MRR@10", "n"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report lacks %q:\n%s", want, text)
		}
	}
}

func TestRunCallsTheSameSearchAsTheServerAndDoesNotWrite(t *testing.T) {
	path, srv, c := fixture(t, true)
	st := open(t, path)
	if _, err := searchquality.Run(context.Background(), st, searchquality.Options{Embedder: c}); err != nil {
		t.Fatal(err)
	}
	// Each query is embedded once, however many rankings and views use it.
	if got := srv.Inputs(); got != 4+5 { // four queries, five entries embedded by the job
		t.Fatalf("the server embedded %d texts, want the queries once each plus the documents of the fixture", got)
	}
	rw, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	var hits int
	if err := rw.DB().QueryRow(`SELECT COALESCE(SUM(hit_count + search_hits), 0) FROM knowledge`).Scan(&hits); err != nil || hits != 0 {
		t.Fatalf("the measurement must not count its searches as use: %d %v", hits, err)
	}
}

func TestRunWithoutAnEmbeddingServerMeasuresFullTextOnly(t *testing.T) {
	path, _, _ := fixture(t, false)
	rep, err := searchquality.Run(context.Background(), open(t, path), searchquality.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Modes) != 1 || rep.Modes[0] != searchquality.FullText {
		t.Fatalf("modes %v", rep.Modes)
	}
	var out bytes.Buffer
	rep.Write(&out)
	if !strings.Contains(out.String(), "not configured") || strings.Contains(out.String(), "hybrid") {
		t.Fatal(out.String())
	}
}

func TestRunDoesNotPassOffTheFallbackAsAHybridResult(t *testing.T) {
	path, srv, c := fixture(t, true)
	srv.Fail.Store(true)
	if _, err := searchquality.Run(context.Background(), open(t, path), searchquality.Options{Embedder: c}); err == nil {
		t.Fatal("a measurement with a failing embedding server must fail, not report full text as hybrid")
	}
}

func TestRunWarnsWhenNotEverythingIsEmbedded(t *testing.T) {
	path, _, c := fixture(t, false)
	rep, err := searchquality.Run(context.Background(), open(t, path), searchquality.Options{Embedder: c})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	rep.Write(&out)
	if !strings.Contains(out.String(), "WARNING") || rep.Embedded != 0 {
		t.Fatalf("%d embedded:\n%s", rep.Embedded, out.String())
	}
}

func TestSampleIsRepeatableAndBounded(t *testing.T) {
	path, _, _ := fixture(t, false)
	st := open(t, path)
	run := func(seed int64, sample int) searchquality.Report {
		rep, err := searchquality.Run(context.Background(), st, searchquality.Options{Sample: sample, Seed: seed})
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}
	a, b := run(1, 2), run(1, 2)
	if a.Sampled != 2 || a.Stock != 4 || a.Blocks[0].Rows[searchquality.FullText] != b.Blocks[0].Rows[searchquality.FullText] {
		t.Fatalf("%+v %+v", a, b)
	}
	if run(1, 100).Sampled != 4 {
		t.Fatal("a sample larger than the stock uses the whole stock")
	}
	seen := map[float64]bool{}
	for seed := int64(1); seed < 30; seed++ {
		seen[run(seed, 2).Blocks[0].Rows[searchquality.FullText].R1] = true
	}
	if len(seen) < 2 {
		t.Fatal("the seed does not change the draw")
	}
}
