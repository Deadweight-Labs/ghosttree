package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embed/embedtest"
	"github.com/Deadweight-Labs/ghosttree/internal/embedjob"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func resolveWith(t *testing.T, job bool, args ...string) (embedSettings, error) {
	t.Helper()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := addEmbedFlags(fs, job)
	if !job {
		f.addKinds(fs)
	}
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return f.resolve()
}

// Without a setting it is knowledge, as before there were other kinds; the kinds
// that are searched by default are the ones that are embedded.
func TestEmbedKindFlagsAndTheEnvironment(t *testing.T) {
	got, err := resolveWith(t, true, "--embed-url", "http://127.0.0.1:8475")
	if err != nil || !slices.Equal(got.Kinds, []string{store.KindKnowledge}) || !slices.Equal(got.SearchKinds, got.Kinds) ||
		!slices.Equal(got.Job.Kinds, got.Kinds) || got.kindsGiven {
		t.Fatalf("the default is knowledge only: %+v %v", got, err)
	}

	t.Setenv(envEmbedKinds, "requests, ghosts")
	got, err = resolveWith(t, true)
	if err != nil || !slices.Equal(got.Kinds, []string{store.KindRequest, store.KindGhost}) || !slices.Equal(got.SearchKinds, got.Kinds) ||
		!slices.Equal(got.Job.Kinds, got.Kinds) || !got.kindsGiven {
		t.Fatalf("env: %+v %v", got, err)
	}
	t.Setenv(envEmbedSearchKinds, "request")
	got, err = resolveWith(t, true)
	if err != nil || !slices.Equal(got.Kinds, []string{store.KindRequest, store.KindGhost}) || !slices.Equal(got.SearchKinds, []string{store.KindRequest}) {
		t.Fatalf("embed two, search one: %+v %v", got, err)
	}
	got, err = resolveWith(t, true, "--embed-kinds", "all", "--embed-search-kinds", "knowledge,document")
	if err != nil || !slices.Equal(got.Kinds, store.EmbedKinds()) || !slices.Equal(got.SearchKinds, []string{store.KindKnowledge, store.KindDocument}) {
		t.Fatalf("flags beat the environment: %+v %v", got, err)
	}

	for _, args := range [][]string{
		{"--embed-kinds", "sessions"},
		{"--embed-kinds", "request,nope"},
		// A kind whose search is on but that is not embedded has nothing to search by.
		{"--embed-search-kinds", "pain"},
		{"--embed-kinds", "request", "--embed-search-kinds", "request,ghost"},
	} {
		t.Setenv(envEmbedKinds, "")
		t.Setenv(envEmbedSearchKinds, "")
		if _, err := resolveWith(t, true, args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}

	// ctx eval search measures any kind and takes neither flag.
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addEmbedFlags(fs, false)
	if fs.Lookup("embed-kinds") != nil || fs.Lookup("embed-search-kinds") != nil {
		t.Fatal("ctx eval search must not take --embed-kinds")
	}
}

func TestServeConfigTakesTheEmbedKinds(t *testing.T) {
	t.Setenv(envEmbedKinds, "")
	t.Setenv(envEmbedSearchKinds, "")
	cfg, err := parseServeConfig([]string{"--db", "x.db", "--embed-url", "http://127.0.0.1:8475", "--embed-kinds", "knowledge,request,document", "--embed-search-kinds", "request"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Embed.embedKinds(), []string{store.KindKnowledge, store.KindRequest, store.KindDocument}) || !slices.Equal(cfg.Embed.searchKinds(), []string{store.KindRequest}) {
		t.Fatalf("%+v", cfg.Embed)
	}
	if _, err := parseServeConfig([]string{"--db", "x.db", "--embed-kinds", "sessions"}, io.Discard); err == nil {
		t.Fatal("an unknown kind must stop the server from starting")
	}
}

// kindsDB writes a database with an object of every kind that has a concept in
// common with "auto defekt" only by meaning, and has the job embed them all.
type kindsDB struct {
	path                                            string
	srv                                             *embedtest.Server
	knowledge, request, pain, document, ghost       int64
	project, ghostPath, secretTitle, secretQuestion string
}

func newKindsDB(t *testing.T, embedded bool) kindsDB {
	t.Helper()
	k := kindsDB{path: filepath.Join(t.TempDir(), "ghosttree.db"), project: "github.com/dw/p", ghostPath: "internal/wagen.go", secretTitle: "Vertrauliches Wagenproblem", secretQuestion: "auto defekt"}
	st, err := store.Open(k.path)
	if err != nil {
		t.Fatal(err)
	}
	k.srv = embedtest.New(t)
	k.srv.Dim = 256
	k.srv.Concepts = map[string]string{"wagen": "auto", "kaputt": "defekt"}
	ax := scope.Axes{Project: k.project}
	if k.knowledge, err = st.InsertKnowledge(store.Knowledge{Type: "note", Title: "Wagen", Body: "wagen kaputt", Scope: ax, Confidence: "trusted"}); err != nil {
		t.Fatal(err)
	}
	d, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: k.secretTitle, Description: "wagen kaputt", Scope: ax}})
	if err != nil {
		t.Fatal(err)
	}
	k.request = d.Request.ID
	if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Segel", Description: "segel mast", Scope: ax}}); err != nil {
		t.Fatal(err)
	}
	res, err := st.DB().Exec(`INSERT INTO pains(project,title,body,state,created_at,updated_at) VALUES(?,'Wagen','wagen kaputt','active','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, k.project)
	if err != nil {
		t.Fatal(err)
	}
	k.pain, _ = res.LastInsertId()
	doc, err := st.CreateDocument(store.Document{Project: k.project, Slug: "wagen", Kind: "report", Title: "Wagen"}, "wagen kaputt", "m")
	if err != nil {
		t.Fatal(err)
	}
	k.document = doc.ID
	if k.ghost, err = st.PutGhostFile(store.GhostFile{Project: k.project, Path: k.ghostPath, Kind: "file", Description: "wagen kaputt"}); err != nil {
		t.Fatal(err)
	}
	if embedded {
		c, err := embed.New(embed.Config{URL: k.srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		job := embedjob.New(st, c, embedjob.Config{Batch: 16, StartDelay: -1, Kinds: store.EmbedKinds()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		for range 20 {
			if res, err := job.Step(context.Background()); err != nil {
				t.Fatal(err)
			} else if res == (embedjob.Result{}) {
				break
			}
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return k
}

// The kinds the server embeds are embedded, the kinds its search uses are
// searched by the vectors, and no other.
func TestStartEmbeddingEmbedsTheConfiguredKindsAndSearchesBySome(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := embedtest.New(t)
	srv.Dim = 256
	srv.Concepts = map[string]string{"wagen": "auto", "kaputt": "defekt"}
	if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Wagen", Description: "wagen kaputt"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutGhostFile(store.GhostFile{Project: "p", Path: "a.go", Kind: "file", Description: "wagen kaputt"}); err != nil {
		t.Fatal(err)
	}
	cfg := embedSettings{URL: srv.URL, Model: embed.DefaultModel, QueryTimeout: 5 * time.Second, Job: fastJob(),
		Kinds: []string{store.KindRequest, store.KindGhost}, SearchKinds: []string{store.KindRequest}}
	ctx, cancel := context.WithCancel(context.Background())
	wait := startEmbedding(ctx, st, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	model := store.EmbedModel{Model: embed.DefaultModel, PrefixVersion: embed.PrefixVersion}
	ok := embedtest.Wait(ctx, 10*time.Second, func() bool {
		r, err1 := st.EmbeddingStatus(store.KindRequest, model)
		g, err2 := st.EmbeddingStatus(store.KindGhost, model)
		return err1 == nil && err2 == nil && r.Current == 1 && g.Current == 1
	})
	if !ok {
		t.Fatal("the job did not embed both kinds")
	}
	cancel()
	wait()
	if !st.EmbedSearches(store.KindRequest) || st.EmbedSearches(store.KindGhost) || st.EmbedSearches(store.KindKnowledge) {
		t.Fatal("only the request search uses the vectors")
	}
	// The request is found by meaning, the ghost description is not, although its
	// vector exists.
	page, err := st.SearchRequestsWith(requestdomain.SearchFilter{Query: "auto defekt", Limit: 5}, store.SearchAuto)
	if err != nil || len(page.Results) != 1 || page.Results[0].Match != store.MatchSemantic {
		t.Fatalf("%+v %v", page.Results, err)
	}
	gs, err := st.SearchGhostFilesWith("auto defekt", store.GhostSearch{Limit: 5})
	if err != nil || len(gs) != 0 {
		t.Fatalf("a kind that is not searched by the vectors answers from words: %+v %v", gs, err)
	}
}

func TestEmbedStatusSaysForEveryKindWhatIsEmbeddedAndDue(t *testing.T) {
	k := newKindsDB(t, true)
	st, err := store.Open(k.path)
	if err != nil {
		t.Fatal(err)
	}
	// A second request that the job has not seen, and a parked ghost description.
	if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Neu", Description: "ganz neu"}}); err != nil {
		t.Fatal(err)
	}
	g2, err := st.PutGhostFile(store.GhostFile{Project: k.project, Path: "zweite.go", Kind: "file", Description: "geheimer Text"})
	if err != nil {
		t.Fatal(err)
	}
	m := store.EmbedModel{Model: embed.DefaultModel, PrefixVersion: embed.PrefixVersion}
	due, err := st.EmbeddingDue(store.KindGhost, m, 8, time.Now(), true)
	if err != nil || len(due.Items) != 1 || due.Items[0].ID != g2 {
		t.Fatalf("%+v %v", due, err)
	}
	if err := st.RecordEmbedFailure(store.EmbedFailure{Kind: store.KindGhost, ObjectID: g2, Model: m.Model, PrefixVersion: m.PrefixVersion,
		TextHash: due.Items[0].Hash, Attempts: 1, Permanent: true, RetryAt: "2026-10-20T12:00:00Z", Error: "refused"}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	code, out := runCtx("embed", "status", "--db", k.path)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{
		"active entries   1", "embedded         1", "due              0",
		"request          embedded 2 of 3, due 1\n",
		"pain             embedded 1 of 1, due 0\n",
		"document         embedded 1 of 1, due 0\n",
		"ghost            embedded 1 of 2, due 1 (1 parked)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "[embedding") {
		t.Fatalf("without a configuration nothing says what is switched on:\n%s", out)
	}
	for _, secret := range []string{k.secretTitle, "geheimer Text", "wagen kaputt", "zweite.go"} {
		if strings.Contains(out, secret) {
			t.Fatalf("the status prints %q:\n%s", secret, out)
		}
	}

	code, out = runCtx("embed", "status", "--db", k.path, "--embed-kinds", "knowledge,request,ghost", "--embed-search-kinds", "request")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{
		"request          embedded 2 of 3, due 1  [embedding on, search on]",
		"pain             embedded 1 of 1, due 0  [embedding off, search off]",
		"ghost            embedded 1 of 2, due 1 (1 parked)  [embedding on, search off]",
		"knowledge        (counted above)  [embedding on, search off]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if code, out = runCtx("embed", "status", "--db", k.path, "--embed-search-kinds", "pain"); code != 2 {
		t.Fatalf("a search kind that is not embedded: exit %d\n%s", code, out)
	}
}

func writeSet(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "set.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEvalSearchSetMeasuresEveryKindAndPrintsAggregatesOnly(t *testing.T) {
	evalProgress = io.Discard
	k := newKindsDB(t, true)
	line := func(kind, target string) string {
		return fmt.Sprintf(`{"query": %q, "kind": %q, "target": %q, "note": "extra members are ignored"}`, k.secretQuestion, kind, target)
	}
	set := writeSet(t,
		line("knowledge", fmt.Sprint(k.knowledge)),
		line("request", fmt.Sprint(k.request)),
		line("requests", fmt.Sprint(k.request)),
		line("pain", fmt.Sprint(k.pain)),
		line("document", fmt.Sprint(k.document)),
		line("ghost", k.project+":"+k.ghostPath),
		// A target that is gone is counted, not fatal.
		line("request", "9999"),
		"",
	)
	code, out := runCtx("eval", "search", "--db", k.path, "--set", set, "--embed-url", k.srv.URL, "--pause", "0")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{
		"search quality on query sets", "knowledge: 1 queries", "request: 3 queries, 1 of them not measured", "pain: 1 queries", "document: 1 queries", "ghost: 1 queries",
		"current vectors for 1 of 1 active ghost objects", "current vectors for 2 of 2 active request objects",
		"fulltext", "vector", "hybrid", "R@10", "MRR@10",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, secret := range []string{k.secretQuestion, k.secretTitle, "wagen kaputt", k.ghostPath, k.project, "extra members"} {
		if strings.Contains(out, secret) {
			t.Fatalf("the output contains %q:\n%s", secret, out)
		}
	}
	// The query shares no word with the objects, only a meaning: in every block the
	// full-text ranking misses them all, and the embedding ranking and the fusion
	// find them all.
	rows := map[string]int{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) != 6 || (f[0] != "fulltext" && f[0] != "vector" && f[0] != "hybrid") {
			continue
		}
		rows[f[0]]++
		want := "100.0%"
		if f[0] == "fulltext" {
			want = "0.0%"
		}
		if f[4] != want {
			t.Errorf("R@10 of %s is %s, want %s: %q", f[0], f[4], want, l)
		}
	}
	if rows["fulltext"] != 5 || rows["vector"] != 5 || rows["hybrid"] != 5 {
		t.Errorf("one row per ranking in each of the five blocks: %v\n%s", rows, out)
	}

	// Without an embedding server only the full-text ranking is measured.
	code, out = runCtx("eval", "search", "--db", k.path, "--set", set, "--pause", "0")
	if code != 0 || !strings.Contains(out, "only the full-text ranking") || strings.Contains(out, "hybrid ") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	// A failing server fails the run.
	k.srv.Fail.Store(true)
	if code, out = runCtx("eval", "search", "--db", k.path, "--set", set, "--embed-url", k.srv.URL, "--pause", "0"); code != 1 || !strings.Contains(out, "eval search:") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func TestEvalSearchSetRejectsABadSetWithoutPrintingIt(t *testing.T) {
	k := newKindsDB(t, false)
	for name, lines := range map[string][]string{
		"not json":       {`{"query": "geheim", "kind": "request", "target": "1"}`, `this is not json geheim`},
		"unknown kind":   {`{"query": "geheim", "kind": "sessions", "target": "1"}`},
		"no target":      {`{"query": "geheim", "kind": "request", "target": ""}`},
		"no query":       {`{"query": " ", "kind": "request", "target": "1"}`},
		"target no id":   {`{"query": "geheim", "kind": "pain", "target": "abc"}`},
		"ghost no colon": {`{"query": "geheim", "kind": "ghost", "target": "internal/x.go"}`},
		"empty":          {""},
	} {
		code, out := runCtx("eval", "search", "--db", k.path, "--set", writeSet(t, lines...))
		if code != 1 || !strings.Contains(out, "eval search:") || strings.Contains(out, "geheim") {
			t.Errorf("%s: exit %d\n%s", name, code, out)
		}
	}
	if code, out := runCtx("eval", "search", "--db", k.path, "--set", writeSet(t, `{"query": "geheim", "kind": "request", "target": "1"}`, `x`)); code != 1 || !strings.Contains(out, "line 2") {
		t.Fatalf("the line is named: exit %d\n%s", code, out)
	}
	if code, out := runCtx("eval", "search", "--db", k.path, "--set", filepath.Join(t.TempDir(), "missing.jsonl")); code != 1 {
		t.Fatalf("a missing file: exit %d\n%s", code, out)
	}
}

// The ledger itself is a set of queries with known answers: the quote of the
// session in which a wish was spoken leads to its request, the title of a
// duplicate to the request it duplicates.
func TestEvalSearchRequestsBuiltinUsesSightingsAndRelations(t *testing.T) {
	evalProgress = io.Discard
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	srv := embedtest.New(t)
	srv.Dim = 256
	srv.Concepts = map[string]string{"wagen": "auto", "kaputt": "defekt"}
	old, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Wagen reparieren", Description: "wagen kaputt", Scope: scope.Axes{Project: "p"}}})
	if err != nil {
		t.Fatal(err)
	}
	dup, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Auto defekt beheben", Description: "auto defekt", Scope: scope.Axes{Project: "p"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddRequestRelation(dup.Request.ID, requestdomain.Relation{Kind: "duplicates", OtherRequestID: old.Request.ID}, "p"); err != nil {
		t.Fatal(err)
	}
	sid, err := st.UpsertSession(store.Session{Harness: "test", ExternalID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO request_sightings(request_id, session_id, chunk_seq, quote) VALUES(?,?,1,'unser auto ist heute leider defekt gewesen'),(?,?,2,'zu kurz')`, old.Request.ID, sid, old.Request.ID, sid); err != nil {
		t.Fatal(err)
	}
	c, err := embed.New(embed.Config{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	job := embedjob.New(st, c, embedjob.Config{Batch: 16, StartDelay: -1, Kinds: []string{store.KindRequest}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := job.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	st.Close()

	code, out := runCtx("eval", "search", "--db", path, "--requests-builtin", "--embed-url", srv.URL, "--pause", "0")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{
		"requests: quotes of the sessions they came from: 1 queries",
		"requests: the title of a duplicate or successor, the older request wanted: 1 queries",
		"fulltext", "vector", "hybrid",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, secret := range []string{"unser auto", "Auto defekt beheben", "Wagen reparieren"} {
		if strings.Contains(out, secret) {
			t.Fatalf("the output contains %q:\n%s", secret, out)
		}
	}

	// An empty ledger has nothing to measure.
	empty := filepath.Join(t.TempDir(), "empty.db")
	es, err := store.Open(empty)
	if err != nil {
		t.Fatal(err)
	}
	es.Close()
	if code, out = runCtx("eval", "search", "--db", empty, "--requests-builtin"); code != 1 || !strings.Contains(out, "no query to measure") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}
