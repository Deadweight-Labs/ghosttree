package main

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embed/embedtest"
	"github.com/Deadweight-Labs/ghosttree/internal/embedjob"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The queries of a search can go to a server of their own, so that they do not
// wait behind the batches of the background job. These tests use two fake
// servers and count what each of them is asked.

func resolveEmbedFlags(t *testing.T, args ...string) (embedSettings, error) {
	t.Helper()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := addEmbedFlags(fs, true)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return f.resolve()
}

func TestEmbedQueryURLEmptyMeansTheSameServer(t *testing.T) {
	got, err := resolveEmbedFlags(t, "--embed-url", "http://127.0.0.1:8475")
	if err != nil || got.QueryURL != "" || got.separateQuery() {
		t.Fatalf("without --embed-query-url the queries go to --embed-url: %+v %v", got, err)
	}
	// Naming the same server is not a second server: nothing to check, one client.
	for _, same := range []string{"http://127.0.0.1:8475", "http://127.0.0.1:8475/", "http://127.0.0.1:8475/v1/embeddings"} {
		got, err = resolveEmbedFlags(t, "--embed-url", "http://127.0.0.1:8475", "--embed-query-url", same)
		if err != nil || got.separateQuery() || got.URL != "http://127.0.0.1:8475" {
			t.Fatalf("%s: %+v %v", same, got, err)
		}
	}
	got, err = resolveEmbedFlags(t, "--embed-url", "http://127.0.0.1:8475", "--embed-query-url", "http://127.0.0.1:8476/")
	if err != nil || !got.separateQuery() || got.QueryURL != "http://127.0.0.1:8476" || got.URL != "http://127.0.0.1:8475" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestEmbedQueryURLReadsTheEnvironmentAndTheFlagWins(t *testing.T) {
	t.Setenv(envEmbedURL, "http://127.0.0.1:8475")
	t.Setenv(envEmbedQueryURL, "http://127.0.0.1:8476")
	got, err := resolveEmbedFlags(t)
	if err != nil || got.QueryURL != "http://127.0.0.1:8476" {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = resolveEmbedFlags(t, "--embed-query-url", "http://127.0.0.1:9999")
	if err != nil || got.QueryURL != "http://127.0.0.1:9999" {
		t.Fatalf("the flag overrides the environment: %+v %v", got, err)
	}
}

// Without --embed-url nothing makes vectors, so a server for queries has nothing
// to search; that is a mistake in the configuration, not a quiet "off".
func TestEmbedQueryURLWithoutEmbedURLIsAConfigurationError(t *testing.T) {
	const want = "--embed-query-url needs --embed-url"
	if _, err := resolveEmbedFlags(t, "--embed-query-url", "http://127.0.0.1:8476"); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("flag alone: %v", err)
	}
	t.Setenv(envEmbedQueryURL, "http://127.0.0.1:8476")
	if _, err := resolveEmbedFlags(t); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("environment alone: %v", err)
	}
	if _, err := parseServeConfig([]string{"--db", "x.db"}, io.Discard); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("the server must not start: %v", err)
	}
	path := filepath.Join(t.TempDir(), "never-opened.db") // the mistake is found before the database is
	for name, args := range map[string][]string{
		"embed status": {"embed", "status", "--db", path},
		"eval search":  {"eval", "search", "--db", path, "--pause", "0"},
	} {
		code, out := runCtx(args...)
		if code != 2 || !strings.Contains(out, want) {
			t.Errorf("%s: exit %d:\n%s", name, code, out)
		}
	}
}

// The query URL is checked exactly like --embed-url.
func TestEmbedQueryURLIsCheckedLikeEmbedURL(t *testing.T) {
	for _, bad := range []string{"ftp://127.0.0.1:8476", "nope", "http://user:secret@127.0.0.1:8476", "http://127.0.0.1:8476?x=1", "http://127.0.0.1:8476#f"} {
		_, err := resolveEmbedFlags(t, "--embed-url", "http://127.0.0.1:8475", "--embed-query-url", bad)
		if err == nil || !strings.Contains(err.Error(), "--embed-query-url") {
			t.Errorf("%q accepted or not named: %v", bad, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("the error repeats the credentials: %v", err)
		}
		// The same address is refused as --embed-url too.
		if _, err := resolveEmbedFlags(t, "--embed-url", bad); err == nil {
			t.Errorf("%q accepted as --embed-url", bad)
		}
	}
	if _, err := parseServeConfig([]string{"--db", "x.db", "--embed-url", "http://127.0.0.1:8475", "--embed-query-url", "ftp://x"}, io.Discard); err == nil {
		t.Fatal("a bad query URL must stop the server from starting")
	}
	cfg, err := parseServeConfig([]string{"--db", "x.db", "--embed-url", "http://127.0.0.1:8475", "--embed-query-url", "http://127.0.0.1:8476"}, io.Discard)
	if err != nil || cfg.Embed.QueryURL != "http://127.0.0.1:8476" {
		t.Fatalf("%+v %v", cfg.Embed, err)
	}
}

func bufLogger(s *syncBuffer) *slog.Logger { return slog.New(slog.NewTextHandler(s, nil)) }

func inputsOf(srv *embedtest.Server) []string {
	var out []string
	for _, r := range srv.Requests() {
		out = append(out, r...)
	}
	return out
}

var probeInput = embed.QueryText(embed.ProbeText)

// startedEmbedding opens a store with one entry and starts the embedding setup
// of cfg on it; stop ends it and waits for its goroutines.
func startedEmbedding(t *testing.T, cfg embedSettings, logs *syncBuffer) (st *store.Store, stop func()) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: "auto", Body: "wagen kaputt"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	wait := startEmbedding(ctx, st, cfg, bufLogger(logs))
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			done := make(chan struct{})
			go func() { wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("the embedding goroutines did not stop with their context")
			}
		})
	}
	t.Cleanup(stop)
	return st, stop
}

func waitEmbedded(t *testing.T, st *store.Store, n int) {
	t.Helper()
	ok := embedtest.Wait(context.Background(), 10*time.Second, func() bool {
		s, err := st.KnowledgeEmbeddingStatus(store.EmbedModel{Model: embed.DefaultModel, PrefixVersion: embed.PrefixVersion})
		return err == nil && s.Current >= n
	})
	if !ok {
		t.Fatal("the job did not embed the entry")
	}
}

func waitLog(t *testing.T, logs *syncBuffer, sub string) {
	t.Helper()
	if !embedtest.Wait(context.Background(), 10*time.Second, func() bool { return strings.Contains(logs.String(), sub) }) {
		t.Fatalf("the log never said %q:\n%s", sub, logs.String())
	}
}

func fastJob() embedjob.Config {
	return embedjob.Config{StartDelay: -1, Pause: time.Millisecond, Idle: time.Millisecond}
}

func TestQueriesGoToTheQueryServerAndDocumentsToTheJobServer(t *testing.T) {
	jobSrv, querySrv := embedtest.New(t), embedtest.New(t)
	logs := &syncBuffer{}
	cfg := embedSettings{URL: jobSrv.URL, QueryURL: querySrv.URL, Model: embed.DefaultModel, QueryTimeout: 5 * time.Second, Job: fastJob()}
	st, stop := startedEmbedding(t, cfg, logs)
	waitEmbedded(t, st, 1)
	waitLog(t, logs, "embed_models_agree")

	got, err := st.SearchKnowledge("wagen kaputt", scope.Axes{}, 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if hybrid, fallbacks := st.EmbedSearchStats(); hybrid != 1 || fallbacks != 0 {
		t.Fatalf("the search must be hybrid: %d hybrid, %d fallbacks\n%s", hybrid, fallbacks, logs.String())
	}
	stop()

	query := embed.QueryText("wagen kaputt")
	job, queries := inputsOf(jobSrv), inputsOf(querySrv)
	if slices.Contains(job, query) {
		t.Fatalf("the job server was asked a search query: %q", job)
	}
	if !slices.Contains(queries, query) {
		t.Fatalf("the query server was not asked the search query: %q", queries)
	}
	var docs int
	for _, in := range job {
		switch {
		case strings.HasPrefix(in, "title: "):
			docs++
		case in != probeInput:
			t.Fatalf("the job server got something that is neither a document nor the probe: %q", in)
		}
	}
	if docs != 1 {
		t.Fatalf("the job server embedded %d documents, want 1: %q", docs, job)
	}
	for _, in := range queries {
		if strings.HasPrefix(in, "title: ") {
			t.Fatalf("the query server was given a document: %q", in)
		}
	}
	for _, want := range []string{"embed_config", "query_url=" + querySrv.URL, "search queries go to their own server", "embed_models_agree"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, logs.String())
		}
	}
	if strings.Contains(logs.String(), "level=ERROR") || strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("loopback servers that agree need no warning:\n%s", logs.String())
	}
}

// Without --embed-query-url everything is as it was: one server, no probe, no
// check, and the same log line.
func TestOneServerForEverythingWithoutAQueryURL(t *testing.T) {
	srv := embedtest.New(t)
	logs := &syncBuffer{}
	cfg := embedSettings{URL: srv.URL, Model: embed.DefaultModel, QueryTimeout: 5 * time.Second, Job: fastJob()}
	st, stop := startedEmbedding(t, cfg, logs)
	waitEmbedded(t, st, 1)
	if _, err := st.SearchKnowledge("wagen kaputt", scope.Axes{}, 10); err != nil {
		t.Fatal(err)
	}
	if hybrid, fallbacks := st.EmbedSearchStats(); hybrid != 1 || fallbacks != 0 {
		t.Fatalf("%d %d", hybrid, fallbacks)
	}
	stop()
	in := inputsOf(srv)
	if !slices.Contains(in, embed.QueryText("wagen kaputt")) {
		t.Fatalf("the one server is asked the query too: %q", in)
	}
	if slices.Contains(in, probeInput) {
		t.Fatalf("with one server there is nothing to compare, and no probe is sent: %q", in)
	}
	for _, unwanted := range []string{"query_url", "embed_models", "their own server"} {
		if strings.Contains(logs.String(), unwanted) {
			t.Errorf("the log mentions %q without a query server:\n%s", unwanted, logs.String())
		}
	}
}

func TestSearchFallsBackToFullTextWhenTheServersServeDifferentModels(t *testing.T) {
	jobSrv, querySrv := embedtest.New(t), embedtest.New(t)
	querySrv.Dim = 128 // another model
	logs := &syncBuffer{}
	cfg := embedSettings{URL: jobSrv.URL, QueryURL: querySrv.URL, Model: embed.DefaultModel, QueryTimeout: 5 * time.Second, Job: fastJob()}
	st, stop := startedEmbedding(t, cfg, logs)
	waitLog(t, logs, "embed_models_differ")
	waitEmbedded(t, st, 1)

	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "128 dimensions") {
		t.Fatalf("the difference is an error line that says what differs:\n%s", logs.String())
	}
	asked := len(inputsOf(querySrv))
	got, err := st.SearchKnowledge("wagen kaputt", scope.Axes{}, 10)
	if err != nil {
		t.Fatalf("the caller must not see the difference: %v", err)
	}
	want, err := st.SearchKnowledgeWith("wagen kaputt", store.KnowledgeSearch{Mode: store.SearchFullTextOnly, Limit: 20})
	if err != nil || len(want) != 1 {
		t.Fatalf("%v %v", want, err)
	}
	if len(got) != 1 || got[0].ID != want[0].ID || got[0].Match != "" {
		t.Fatalf("answer %+v, want the full-text answer %+v", got, want)
	}
	if hybrid, fallbacks := st.EmbedSearchStats(); hybrid != 0 || fallbacks != 1 {
		t.Fatalf("the search must have used full text alone: %d hybrid, %d fallbacks", hybrid, fallbacks)
	}
	stop()
	if n := len(inputsOf(querySrv)); n != asked {
		t.Fatalf("the query server was asked %d texts after the verdict; a query that cannot be compared is not sent", n-asked)
	}
	// The job is not stopped by it: the vectors are the job server's.
	if !slices.ContainsFunc(inputsOf(jobSrv), func(s string) bool { return strings.HasPrefix(s, "title: ") }) {
		t.Fatal("the job server embedded nothing")
	}
}

func TestNonLoopbackQueryServerIsWarnedAbout(t *testing.T) {
	jobSrv := embedtest.New(t)
	logs := &syncBuffer{}
	// 192.0.2.0/24 is reserved for documentation; nothing answers there, and the
	// check only has to stop when its context ends.
	cfg := embedSettings{URL: jobSrv.URL, QueryURL: "http://192.0.2.1:8476", Model: embed.DefaultModel, QueryTimeout: time.Second, Job: fastJob()}
	_, stop := startedEmbedding(t, cfg, logs)
	stop()
	out := logs.String()
	if !strings.Contains(out, "the query server is not on this machine; every search query is sent to it") || !strings.Contains(out, "192.0.2.1:8476") {
		t.Fatalf("a query server on another machine needs a warning:\n%s", out)
	}
	if strings.Contains(out, "the embedding server is not on this machine") {
		t.Fatalf("the job server is on this machine:\n%s", out)
	}
}

func TestEmbedStatusShowsBothServersWhenTheyDiffer(t *testing.T) {
	path, jobSrv := embedFixture(t, threeEntries, 2)
	querySrv := embedtest.New(t)

	code, out := runCtx("embed", "status", "--db", path, "--embed-url", jobSrv.URL, "--embed-query-url", querySrv.URL)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{
		"job server       " + jobSrv.URL + ": ready, one query took",
		"query server     " + querySrv.URL + ": ready, one query took",
		"same model       yes (the same text has a cosine of 1.00000, 64 dimensions)",
		"embedded         2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	// Another model on the query server.
	other := embedtest.New(t)
	other.Dim = 128
	_, out = runCtx("embed", "status", "--db", path, "--embed-url", jobSrv.URL, "--embed-query-url", other.URL)
	if !strings.Contains(out, "same model       NO: the query server returns vectors of 128 dimensions, the job server of 64") ||
		!strings.Contains(out, "full text") {
		t.Fatalf("a difference must be named:\n%s", out)
	}

	// A query server that is down is named, and nothing is claimed about the models.
	querySrv.Close()
	_, out = runCtx("embed", "status", "--db", path, "--embed-url", jobSrv.URL, "--embed-query-url", querySrv.URL)
	if !strings.Contains(out, "query server     "+querySrv.URL+": not ready") || !strings.Contains(out, "same model       not checked: query server:") {
		t.Fatalf("%s", out)
	}

	// One server, or the same address twice, prints the line it always printed.
	for _, args := range [][]string{
		{"--embed-url", jobSrv.URL},
		{"--embed-url", jobSrv.URL, "--embed-query-url", jobSrv.URL + "/"},
	} {
		_, out = runCtx(append([]string{"embed", "status", "--db", path}, args...)...)
		if !strings.Contains(out, "embedding server "+jobSrv.URL+": ready") || strings.Contains(out, "query server") || strings.Contains(out, "same model") {
			t.Fatalf("%v:\n%s", args, out)
		}
	}
}

func TestEvalSearchEmbedsItsQueriesOnTheQueryServer(t *testing.T) {
	evalProgress = io.Discard
	path, jobSrv := embedFixture(t, threeEntries, 3)
	querySrv := embedtest.New(t)
	before := jobSrv.Inputs()

	code, out := runCtx("eval", "search", "--db", path, "--embed-url", jobSrv.URL, "--embed-query-url", querySrv.URL, "--pause", "0", "--sample", "50")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"fulltext", "vector", "hybrid", "current vectors for 3 of 3"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	queries := 0
	for _, in := range inputsOf(querySrv) {
		if strings.HasPrefix(in, "task: search result | query: ") && in != probeInput {
			queries++
		}
	}
	if queries != 3 {
		t.Fatalf("the query server embedded %d of the 3 quotes: %q", queries, inputsOf(querySrv))
	}
	if n := jobSrv.Inputs() - before; n != 1 {
		t.Fatalf("the job server is asked the probe and nothing else, got %d texts", n)
	}

	// Another model: the numbers would mean nothing, so no query is sent.
	other := embedtest.New(t)
	other.Dim = 128
	code, out = runCtx("eval", "search", "--db", path, "--embed-url", jobSrv.URL, "--embed-query-url", other.URL, "--pause", "0")
	if code != 1 || !strings.Contains(out, "do not serve the same model") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if n := other.Inputs(); n != 1 {
		t.Fatalf("only the probe may reach a query server that disagrees, got %d texts", n)
	}
}
