package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embed/embedtest"
	"github.com/Deadweight-Labs/ghosttree/internal/embedjob"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func sessionPrompt(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "timestamp": "2026-10-01T10:00:00Z", "message": map[string]any{"role": "user", "content": text}})
	return string(b)
}

func sessionChunks(texts ...string) []store.Chunk {
	var out []store.Chunk
	for i, text := range texts {
		out = append(out, store.Chunk{Seq: i, Role: "user", Text: text, Raw: sessionPrompt(text)})
	}
	return out
}

func wagenServer(t *testing.T) *embedtest.Server {
	t.Helper()
	srv := embedtest.New(t)
	srv.Dim = 256
	srv.Concepts = map[string]string{"wagen": "auto", "kaputt": "defekt"}
	return srv
}

// sessionsDB writes a database with two sessions, one of which says "wagen kaputt"
// and is the evidence of a knowledge entry whose title says "auto defekt": no
// word in common, only a meaning. With embedded the job has made the vectors.
type sessionsDB struct {
	path    string
	srv     *embedtest.Server
	wagen   store.Session
	segel   store.Session
	title   string
	prompts []string
}

func newSessionsDB(t *testing.T, embedded bool) sessionsDB {
	t.Helper()
	d := sessionsDB{path: filepath.Join(t.TempDir(), "ghosttree.db"), srv: wagenServer(t), title: "auto defekt und was wir dagegen taten"}
	st, err := store.Open(d.path)
	if err != nil {
		t.Fatal(err)
	}
	add := func(ext string, texts ...string) store.Session {
		id, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: ext, Scope: scope.Axes{Project: "github.com/dw/p"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AppendChunks(id, sessionChunks(texts...)); err != nil {
			t.Fatal(err)
		}
		sess, err := st.SessionByID(id)
		if err != nil {
			t.Fatal(err)
		}
		return sess
	}
	d.prompts = []string{"wagen kaputt, was machen wir damit am besten", "segel mast und boot, das sind andere dinge als dieses"}
	d.wagen = add("wagen", d.prompts[0], "wir lassen ihn reparieren, wagen kaputt hin oder her")
	d.segel = add("segel", d.prompts[1])
	kid, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: d.title, Body: "text", Scope: scope.Axes{Project: "github.com/dw/p"}, Confidence: "trusted"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO knowledge_evidence(knowledge_id, session_id, chunk_seq, quote) VALUES(?,?,0,'q')`, kid, d.wagen.ID); err != nil {
		t.Fatal(err)
	}
	if embedded {
		c, err := embed.New(embed.Config{URL: d.srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		job := embedjob.New(st, c, embedjob.Config{Batch: 16, StartDelay: -1, Kinds: []string{store.KindSession}, SessionWindow: embedjob.Window{Always: true}},
			slog.New(slog.NewTextHandler(io.Discard, nil)))
		for range 20 {
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
	return d
}

// The texts of the sessions go to the server of the job and nowhere else, the
// queries of the search to the query server; the sessions are one more kind of the
// one switch.
func TestSessionTextsGoToTheJobServerAndQueriesToTheQueryServer(t *testing.T) {
	jobSrv, querySrv := wagenServer(t), wagenServer(t)
	logs := &syncBuffer{}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	id, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendChunks(id, sessionChunks("wagen kaputt, was machen wir damit am besten", "und das zweite, auch wagen kaputt, aber anders")); err != nil {
		t.Fatal(err)
	}
	job := fastJob()
	job.SessionWindow = embedjob.Window{Always: true}
	cfg := embedSettings{URL: jobSrv.URL, QueryURL: querySrv.URL, Model: embed.DefaultModel, QueryTimeout: 5 * time.Second, Job: job,
		Kinds: []string{store.KindSession}, SearchKinds: []string{store.KindSession}, SessionWeight: 2}
	ctx, cancel := context.WithCancel(context.Background())
	wait := startEmbedding(ctx, st, cfg, bufLogger(logs))
	stop := func() { cancel(); wait() }
	t.Cleanup(stop)
	model := store.EmbedModel{Model: embed.DefaultModel, PrefixVersion: embed.PrefixVersion}
	if !embedtest.Wait(ctx, 10*time.Second, func() bool {
		s, err := st.EmbeddingStatus(store.KindSession, model)
		return err == nil && s.Current == 2
	}) {
		t.Fatalf("the job did not embed the texts:\n%s", logs.String())
	}
	waitLog(t, logs, "embed_models_agree")

	hits, err := st.SearchSessionsWith("auto defekt", store.SessionSearch{Limit: 5, Prefilter: store.SessionPrefilter{Unrestricted: true}})
	if err != nil || len(hits) != 1 || hits[0].Match != store.MatchSemantic {
		t.Fatalf("%+v %v\n%s", hits, err, logs.String())
	}
	stop()

	var docs int
	for _, in := range inputsOf(jobSrv) {
		switch {
		case strings.HasPrefix(in, "title: none | text: "):
			docs++
		case in != probeInput:
			t.Fatalf("the job server got something that is neither a text of a session nor the probe: %q", in)
		}
	}
	if docs != 2 {
		t.Fatalf("the job server embedded %d texts, want 2", docs)
	}
	queries := inputsOf(querySrv)
	if !slices.Contains(queries, embed.QueryText("auto defekt")) {
		t.Fatalf("the query server was not asked the query: %q", queries)
	}
	for _, in := range queries {
		if strings.HasPrefix(in, "title: ") {
			t.Fatalf("the query server was given a text of a session: %q", in)
		}
	}
	if !strings.Contains(logs.String(), "session_window=always") || !strings.Contains(logs.String(), "session_weight=2") {
		t.Errorf("the log does not say how the sessions are served:\n%s", logs.String())
	}
}

func TestEmbedSessionFlagsAndTheEnvironment(t *testing.T) {
	got, err := resolveWith(t, true, "--embed-url", "http://127.0.0.1:8475", "--embed-kinds", "sessions")
	if err != nil || !slices.Equal(got.Kinds, []string{store.KindSession}) || got.Job.SessionWindow != (embedjob.Window{}) || got.SessionWeight != embed.DefaultSessionVectorWeight {
		t.Fatalf("the defaults: %+v %v", got, err)
	}
	got, err = resolveWith(t, true, "--embed-url", "http://127.0.0.1:8475", "--embed-kinds", "session",
		"--embed-session-window", "22:00-04:30", "--embed-session-weight", "3.5")
	if err != nil || got.Job.SessionWindow != (embedjob.Window{Start: 22 * 60, End: 4*60 + 30}) || got.SessionWeight != 3.5 {
		t.Fatalf("the flags: %+v %v", got, err)
	}
	t.Setenv(envEmbedSessionWindow, "always")
	t.Setenv(envEmbedSessionWeight, "1")
	got, err = resolveWith(t, true, "--embed-url", "http://127.0.0.1:8475", "--embed-kinds", "session")
	if err != nil || !got.Job.SessionWindow.Always || got.SessionWeight != 1 {
		t.Fatalf("the environment: %+v %v", got, err)
	}
	got, err = resolveWith(t, true, "--embed-url", "http://127.0.0.1:8475", "--embed-session-window", "01:00-02:00")
	if err != nil || got.Job.SessionWindow.Start != 60 {
		t.Fatalf("a flag beats the environment: %+v %v", got, err)
	}
	t.Setenv(envEmbedSessionWindow, "")
	t.Setenv(envEmbedSessionWeight, "")
	for _, bad := range [][]string{
		{"--embed-session-window", "25:00-26:00"},
		{"--embed-session-window", "10:00-10:00"},
		{"--embed-session-window", "10:00"},
		{"--embed-session-window", "ten to six"},
		{"--embed-session-weight", "0"},
		{"--embed-session-weight", "-1"},
		{"--embed-session-weight", "many"},
		{"--embed-session-weight", "101"},
	} {
		if _, err := resolveWith(t, true, append([]string{"--embed-url", "http://127.0.0.1:8475"}, bad...)...); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	// A server whose configuration is wrong does not start.
	if _, err := parseServeConfig([]string{"--db", "x.db", "--embed-url", "http://127.0.0.1:8475", "--embed-session-window", "never"}, io.Discard); err == nil {
		t.Fatal("a bad window must stop the server from starting")
	}
	// The measurement takes the weight, not the window.
	if got, err := resolveWith(t, false, "--embed-session-weight", "4"); err != nil || got.SessionWeight != 4 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestEmbedStatusShowsTheSessionsWithTheirWindowAndProgress(t *testing.T) {
	d := newSessionsDB(t, true)
	// A text that the job has not seen.
	st, err := store.Open(d.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendChunks(d.segel.ID, []store.Chunk{{Seq: 1, Role: "user", Text: "x", Raw: sessionPrompt("und noch eine frage zu segel und mast bitte")}}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	defer func(old func() time.Time) { embedNow = old }(embedNow)

	embedNow = func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.Local) }
	code, out := runCtx("embed", "status", "--db", d.path, "--embed-kinds", "knowledge,session", "--embed-session-window", "22:00-04:00")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// Three texts of the first session and the first of the second are embedded;
	// the new one is due, and the answer of the first session closes with no prompt after it, so it is not yet.
	for _, want := range []string{
		"session          embedded 3 of 4, due 1  [embedding on, search on]  [window 22:00-04:00 local time of the server, opens in 10h0m0s]\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	embedNow = func() time.Time { return time.Date(2026, 10, 21, 23, 30, 0, 0, time.Local) }
	_, out = runCtx("embed", "status", "--db", d.path, "--embed-kinds", "knowledge,session", "--embed-session-window", "22:00-04:00")
	if !strings.Contains(out, "[window 22:00-04:00 local time of the server, open now]") {
		t.Errorf("the window is open at 23:30:\n%s", out)
	}
	// Without a window given the default is said.
	_, out = runCtx("embed", "status", "--db", d.path, "--embed-kinds", "knowledge,session")
	if !strings.Contains(out, "[window 00:00-06:00 local time of the server, ") {
		t.Errorf("the default window is not said:\n%s", out)
	}
	for _, secret := range d.prompts {
		if strings.Contains(out, secret) {
			t.Fatalf("the status prints a text of a session:\n%s", out)
		}
	}
}

// Counting the sessions reads and cleans the line of every text that has no vector,
// which the other kinds do not need. The command does it only when the
// configuration names the sessions, and they are then part of the total of the
// background job; otherwise it says so in one line and does not touch the tables
// of the sessions (here: one of them is gone, and the line does not mind).
func TestEmbedStatusDoesNotCountTheSessionsUnlessTheyAreEmbedded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "only", Scope: scope.Axes{Project: "github.com/dw/p"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendChunks(id, sessionChunks("a prompt that is long enough to be embedded, about the boat")); err != nil {
		t.Fatal(err)
	}
	st.Close()

	const notCounted = "session          not counted: the sessions are not among the embedded kinds"
	for name, args := range map[string][]string{
		"no configuration":        {},
		"kinds without sessions":  {"--embed-kinds", "knowledge,request"},
		"a window is no kind":     {"--embed-session-window", "22:00-04:00"},
		"search kinds, no window": {"--embed-kinds", "knowledge,ghost", "--embed-search-kinds", "ghost"},
	} {
		code, out := runCtx(append([]string{"embed", "status", "--db", path}, args...)...)
		if code != 0 || !strings.Contains(out, notCounted) {
			t.Fatalf("%s: exit %d, no line that says the sessions are not counted:\n%s", name, code, out)
		}
		// The text of the session is not read, so it is not in the total either: nothing is due.
		if !strings.Contains(out, "background job   nothing to do") || strings.Contains(out, "[window") {
			t.Errorf("%s: the sessions are in the total or the window is said:\n%s", name, out)
		}
	}
	_, out := runCtx("embed", "status", "--db", path, "--embed-kinds", "knowledge,request")
	if !strings.Contains(out, notCounted+" (name them in --embed-kinds, for example knowledge,session)  [embedding off, search off]") {
		t.Errorf("a configuration without sessions says them off:\n%s", out)
	}

	// Named, they are counted and part of the total.
	code, out := runCtx("embed", "status", "--db", path, "--embed-kinds", "knowledge,session")
	if code != 0 || !strings.Contains(out, "session          embedded 0 of 1, due 1  [embedding on, search on]  [window ") || !strings.Contains(out, "background job   no progress recorded yet") {
		t.Fatalf("exit %d:\n%s", code, out)
	}

	// The proof that the tables of the sessions are not read when the sessions are not
	// named: without one of them the command that names them has to say so, and the
	// command that does not name them does not notice.
	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DROP TABLE session_embeddings`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	_, out = runCtx("embed", "status", "--db", path)
	if !strings.Contains(out, notCounted) || strings.Contains(out, "tables not created yet") {
		t.Errorf("the status without the sessions read their table:\n%s", out)
	}
	_, out = runCtx("embed", "status", "--db", path, "--embed-kinds", "knowledge,session")
	if !strings.Contains(out, "session          tables not created yet") {
		t.Errorf("the status with the sessions did not read their table:\n%s", out)
	}
}

func TestEvalSearchSetMeasuresSessions(t *testing.T) {
	evalProgress = io.Discard
	d := newSessionsDB(t, true)
	line := func(target string) string {
		return fmt.Sprintf(`{"query": %q, "kind": "session", "target": %q}`, "auto defekt", target)
	}
	st, err := store.OpenReadOnly(d.path, 1)
	if err != nil {
		t.Fatal(err)
	}
	pid := d.wagen.PublicID
	st.Close()
	set := writeSet(t, line(fmt.Sprint(d.wagen.ID)), line(pid), `{"query": "auto defekt", "kind": "sessions", "target": "424242"}`)
	code, out := runCtx("eval", "search", "--db", d.path, "--set", set, "--embed-url", d.srv.URL, "--embed-min-similarity", "0", "--pause", "0")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"session: 3 queries, 1 of them not measured", "current vectors for 3 of 3 active session objects", "fulltext", "vector", "hybrid"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	rows := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) != 6 || (f[0] != "fulltext" && f[0] != "vector" && f[0] != "hybrid") {
			continue
		}
		rows[f[0]] = true
		want := "100.0%"
		if f[0] == "fulltext" {
			want = "0.0%"
		}
		if f[1] != "2" || f[4] != want {
			t.Errorf("%s: n=%s R@10=%s, want n=2 and %s: %q", f[0], f[1], f[4], want, l)
		}
	}
	if len(rows) != 3 {
		t.Errorf("one row for each ranking: %v\n%s", rows, out)
	}
	for _, secret := range append(d.prompts, "auto defekt", d.wagen.PublicID, "wagen") {
		if strings.Contains(out, secret) {
			t.Fatalf("the output contains %q:\n%s", secret, out)
		}
	}
	for name, bad := range map[string]string{
		"no target": `{"query": "auto defekt", "kind": "session", "target": ""}`,
		"no query":  `{"query": " ", "kind": "session", "target": "1"}`,
	} {
		if code, out := runCtx("eval", "search", "--db", d.path, "--set", writeSet(t, bad)); code != 1 || strings.Contains(out, "auto defekt") {
			t.Errorf("%s: exit %d\n%s", name, code, out)
		}
	}
	// An address that no session has is counted, not fatal, like a number.
	if code, out := runCtx("eval", "search", "--db", d.path, "--set", writeSet(t, line("abcdefghijkm"), line(pid)), "--embed-url", d.srv.URL, "--embed-min-similarity", "0", "--pause", "0"); code != 0 || !strings.Contains(out, "session: 2 queries, 1 of them not measured") {
		t.Errorf("an address that is nobody's: exit %d\n%s", code, out)
	}

	// The knowledge of the instance is a set of queries too: its titles, and the
	// sessions that are the evidence of them.
	code, out = runCtx("eval", "search", "--db", d.path, "--sessions-builtin", "--embed-url", d.srv.URL, "--embed-min-similarity", "0", "--pause", "0")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"sessions: the title of a knowledge entry, a session of its evidence wanted: 1 queries", "vector", "hybrid"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, d.title) {
		t.Fatalf("the output contains a title:\n%s", out)
	}
	if code, out = runCtx("eval", "search", "--db", d.path, "--sessions-builtin", "--sessions-sample", "0"); code != 2 {
		t.Fatalf("a sample of nothing: exit %d\n%s", code, out)
	}
}
