package main

import (
	"bytes"
	"context"
	"flag"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embed/embedtest"
	"github.com/Deadweight-Labs/ghosttree/internal/embedjob"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// embedFixture writes a database with the given entries, each backed by the
// quote of the same index, and has the first `embedded` of them embedded.
func embedFixture(t *testing.T, entries [][3]string, embedded int) (string, *embedtest.Server) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	srv := embedtest.New(t)
	sid, err := st.UpsertSession(store.Session{Harness: "test", ExternalID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range entries {
		id, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: e[0], Body: e[1], Confidence: "trusted"})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AddEvidence(id, []store.Evidence{{SessionID: sid, ChunkSeq: i, Quote: e[2]}}); err != nil {
			t.Fatal(err)
		}
	}
	if embedded > 0 {
		c, err := embed.New(embed.Config{URL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		job := embedjob.New(st, c, embedjob.Config{Batch: embedded, StartDelay: -1}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if res, err := job.Step(context.Background()); err != nil || res.Embedded != embedded {
			t.Fatalf("%+v %v", res, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return path, srv
}

var threeEntries = [][3]string{
	{"Deploy order", "stop service install binary restart service", "first stop the service then install the new binary"},
	{"Branch naming", "use feature slash short names for branches", "name branches feature slash something short"},
	{"Secret handling", "never print tokens keep secrets out of logs", "never print tokens in logs or output ever"},
}

func runCtx(args ...string) (int, string) {
	var out bytes.Buffer
	code := run(args, &out)
	return code, out.String()
}

func TestEmbedStatusCountsVectorsAndProbesTheServer(t *testing.T) {
	path, srv := embedFixture(t, threeEntries, 2)
	code, out := runCtx("embed", "status", "--db", path, "--embed-url", srv.URL)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{
		"model            " + embed.DefaultModel,
		"active entries   3",
		"embedded         2",
		"due              1",
		"ready, one query took",
		"64 dimensions",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "first stop") || strings.Contains(out, "Deploy order") {
		t.Fatalf("status prints no entry text:\n%s", out)
	}

	// A server that does not answer is reported, not hidden.
	srv.SetHealthy(false)
	if _, out = runCtx("embed", "status", "--db", path, "--embed-url", srv.URL); !strings.Contains(out, "not ready") {
		t.Fatalf("an unhealthy server must be named:\n%s", out)
	}
	srv.SetHealthy(true)
	srv.Fail.Store(true)
	if _, out = runCtx("embed", "status", "--db", path, "--embed-url", srv.URL); !strings.Contains(out, "embedding failed") {
		t.Fatalf("a server that fails to embed must be named:\n%s", out)
	}
	srv.Close()
	if _, out = runCtx("embed", "status", "--db", path, "--embed-url", srv.URL); !strings.Contains(out, "not ready") {
		t.Fatalf("a dead server must be named:\n%s", out)
	}

	// Without a URL the counts still come from the database.
	code, out = runCtx("embed", "status", "--db", path)
	if code != 0 || !strings.Contains(out, "embedded         2") || !strings.Contains(out, "not configured") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestEmbedStatusShowsHowTheJobFared(t *testing.T) {
	path, _ := embedFixture(t, threeEntries, 0)
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetEmbedJobState(store.EmbedJobState{LastOKAt: "2026-10-20T10:00:00Z", LastError: "embedding server answered 500: model crashed", LastErrorAt: "2026-10-20T10:05:00Z", Failures: 6}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	_, out := runCtx("embed", "status", "--db", path)
	for _, want := range []string{"failing since 2026-10-20T10:05:00Z, 6 in a row: embedding server answered 500: model crashed", "last progress 2026-10-20T10:00:00Z", "due              3"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestEmbedStatusNamesParkedEntries(t *testing.T) {
	path, _ := embedFixture(t, threeEntries, 1)
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	due, err := st.KnowledgeEmbeddingDue(store.EmbedModel{Model: embed.DefaultModel, PrefixVersion: embed.PrefixVersion}, 8, time.Now(), true)
	if err != nil || len(due.Items) != 2 {
		t.Fatalf("%+v %v", due, err)
	}
	if err := st.RecordEmbeddingFailure(store.EmbeddingFailure{KnowledgeID: due.Items[0].ID, Model: embed.DefaultModel, PrefixVersion: embed.PrefixVersion,
		TextHash: due.Items[0].Hash, Attempts: 1, Permanent: true, RetryAt: "2026-10-20T12:00:00Z", Error: "refused"}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	_, out := runCtx("embed", "status", "--db", path)
	if !strings.Contains(out, "due              2 (1 parked") || !strings.Contains(out, "embedded         1") {
		t.Fatalf("%s", out)
	}
}

func TestEmbedStatusSeesVectorsOfAnotherModelAsDue(t *testing.T) {
	path, srv := embedFixture(t, threeEntries, 3)
	code, out := runCtx("embed", "status", "--db", path, "--embed-model", "another-model", "--embed-url", srv.URL)
	if code != 0 || !strings.Contains(out, "embedded         0") || !strings.Contains(out, "3 have a vector of another model") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestEmbedStatusNeedsTheTablesAndAnArgument(t *testing.T) {
	path, _ := embedFixture(t, threeEntries, 0)
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DROP TABLE knowledge_embeddings`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if code, out := runCtx("embed", "status", "--db", path); code != 1 || !strings.Contains(out, "no embedding tables") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if code, _ := runCtx("embed", "status"); code != 2 {
		t.Fatalf("without --db: exit %d", code)
	}
	if code, _ := runCtx("embed"); code != 2 {
		t.Fatalf("without a subcommand: exit %d", code)
	}
	if code, _ := runCtx("embed", "status", "--db", path, "--embed-url", "ftp://nope"); code != 2 {
		t.Fatalf("with a bad URL: exit %d", code)
	}
}

func TestEvalSearchPrintsAggregatesOnly(t *testing.T) {
	evalProgress = io.Discard
	path, srv := embedFixture(t, threeEntries, 3)
	code, out := runCtx("eval", "search", "--db", path, "--embed-url", srv.URL, "--pause", "0", "--sample", "50")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"fulltext", "vector", "hybrid", "R@10", "MRR@10", "quotes of at least 30 characters: 3", "current vectors for 3 of 3"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, e := range threeEntries {
		for _, secret := range []string{e[0], e[1], e[2]} {
			if strings.Contains(out, secret) {
				t.Fatalf("the output contains %q:\n%s", secret, out)
			}
		}
	}

	code, out = runCtx("eval", "search", "--db", path, "--pause", "0")
	if code != 0 || !strings.Contains(out, "only the full-text ranking") || strings.Contains(out, "hybrid ") {
		t.Fatalf("without an embedding server: exit %d\n%s", code, out)
	}

	srv.Fail.Store(true)
	code, out = runCtx("eval", "search", "--db", path, "--embed-url", srv.URL, "--pause", "0")
	if code != 1 || !strings.Contains(out, "eval search:") {
		t.Fatalf("a failing server must fail the run: exit %d\n%s", code, out)
	}
}

func TestEvalSearchChecksItsArguments(t *testing.T) {
	path, _ := embedFixture(t, threeEntries, 0)
	for name, args := range map[string][]string{
		"no db":             {"eval", "search"},
		"no subcommand":     {"eval"},
		"unknown":           {"eval", "nope"},
		"sample":            {"eval", "search", "--db", path, "--sample", "0"},
		"min chars":         {"eval", "search", "--db", path, "--min-chars", "0"},
		"min similarity":    {"eval", "search", "--db", path, "--embed-min-similarity", "1.5"},
		"bad url":           {"eval", "search", "--db", path, "--embed-url", "nope"},
		"positional":        {"eval", "search", "--db", path, "extra"},
		"negative pause":    {"eval", "search", "--db", path, "--pause", "-1s"},
		"not a number":      {"eval", "search", "--db", path, "--embed-min-similarity", "x"},
		"database is a dir": {"eval", "search", "--db", t.TempDir()},
	} {
		code, out := runCtx(args...)
		want := 2
		if name == "database is a dir" {
			want = 1
		}
		if code != want {
			t.Errorf("%s: exit %d, want %d:\n%s", name, code, want, out)
		}
	}
}

func TestEmbedFlagsReadTheEnvironmentAndTheFlagWins(t *testing.T) {
	resolve := func(args ...string) (embedSettings, error) {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		f := addEmbedFlags(fs, true)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		return f.resolve()
	}
	off, err := resolve()
	if err != nil || off.enabled() || off.Model != embed.DefaultModel || off.QueryTimeout != embed.DefaultQueryTimeout {
		t.Fatalf("without configuration embeddings are off: %+v %v", off, err)
	}
	if off.MinSimilarity != embed.DefaultMinSimilarity || embed.DefaultMinSimilarity < 0.5 || embed.DefaultMinSimilarity > 0.65 {
		t.Fatalf("the default minimum similarity is %v", off.MinSimilarity)
	}
	if none, err := resolve("--embed-min-similarity", "0"); err != nil || none.MinSimilarity != 0 {
		t.Fatalf("an explicit 0 means no minimum: %+v %v", none, err)
	}

	t.Setenv(envEmbedURL, "http://127.0.0.1:8475/")
	t.Setenv(envEmbedModel, "m-env")
	t.Setenv(envEmbedQueryTimeout, "900ms")
	t.Setenv(envEmbedBatch, "4")
	t.Setenv(envEmbedPause, "3s")
	t.Setenv(envEmbedIdle, "1m")
	t.Setenv(envEmbedMaxBackoff, "2m")
	t.Setenv(envEmbedMinSimilarity, "0.35")
	got, err := resolve()
	if err != nil {
		t.Fatal(err)
	}
	if !got.enabled() || got.URL != "http://127.0.0.1:8475" || got.Model != "m-env" || got.QueryTimeout != 900*time.Millisecond ||
		got.Job.Batch != 4 || got.Job.Pause != 3*time.Second || got.Job.Idle != time.Minute || got.Job.MaxBackoff != 2*time.Minute || got.MinSimilarity != 0.35 {
		t.Fatalf("%+v", got)
	}
	got, err = resolve("--embed-url", "http://10.0.0.5:9", "--embed-batch", "16", "--embed-model", "m-flag")
	if err != nil || got.URL != "http://10.0.0.5:9" || got.Job.Batch != 16 || got.Model != "m-flag" {
		t.Fatalf("flags override the environment: %+v %v", got, err)
	}

	for _, args := range [][]string{
		{"--embed-url", "ftp://x"}, {"--embed-batch", "0"}, {"--embed-batch", "65"}, {"--embed-batch", "x"},
		{"--embed-pause", "0s"}, {"--embed-pause", "soon"}, {"--embed-query-timeout", "-1s"}, {"--embed-idle", "0"},
		{"--embed-max-backoff", "x"}, {"--embed-min-similarity", "1"}, {"--embed-min-similarity", "-0.1"},
	} {
		if _, err := resolve(args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestServeConfigTakesTheEmbeddingSettings(t *testing.T) {
	cfg, err := parseServeConfig([]string{"--db", "x.db", "--embed-url", "http://127.0.0.1:8475", "--embed-pause", "5s"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Embed.URL != "http://127.0.0.1:8475" || cfg.Embed.Job.Pause != 5*time.Second {
		t.Fatalf("%+v", cfg.Embed)
	}
	if cfg, err = parseServeConfig([]string{"--db", "x.db"}, io.Discard); err != nil || cfg.Embed.enabled() {
		t.Fatalf("embeddings are off by default: %+v %v", cfg.Embed, err)
	}
	if _, err = parseServeConfig([]string{"--embed-url", "nonsense"}, io.Discard); err == nil {
		t.Fatal("a bad URL must stop the server from starting")
	}
}

// With a URL configured the server turns hybrid search on and the job embeds
// the entries; without one nothing runs.
func TestStartEmbeddingTurnsOnSearchAndTheJob(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: "auto", Body: "wagen kaputt"}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	wait := startEmbedding(context.Background(), st, embedSettings{}, logger)
	wait()
	if st.EmbedderConfigured() {
		t.Fatal("embeddings are on without a URL")
	}

	srv := embedtest.New(t)
	cfg := embedSettings{URL: srv.URL, Model: embed.DefaultModel, QueryTimeout: time.Second, Job: embedjob.Config{StartDelay: -1, Pause: time.Millisecond, Idle: time.Millisecond}}
	ctx, cancel := context.WithCancel(context.Background())
	wait = startEmbedding(ctx, st, cfg, logger)
	if !embedtest.Wait(ctx, 5*time.Second, func() bool { return srv.Inputs() >= 1 }) {
		t.Fatal("the job did not embed the entry")
	}
	cancel()
	wait()
	status, err := st.KnowledgeEmbeddingStatus(store.EmbedModel{Model: embed.DefaultModel, PrefixVersion: embed.PrefixVersion})
	if err != nil || status.Current != 1 {
		t.Fatalf("%+v %v", status, err)
	}
	if !st.EmbedderConfigured() {
		t.Fatal("search is not hybrid")
	}
}
