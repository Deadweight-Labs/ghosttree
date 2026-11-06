package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embed/embedtest"
	"github.com/Deadweight-Labs/ghosttree/internal/embedjob"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// meaningTranscript is a session that says its words in other words than the
// query does: s1..s4 are q1..q4 for the fake embedding server.
func meaningTranscript(text, title string) []store.Chunk {
	user, _ := json.Marshal(map[string]any{"type": "user", "timestamp": "2026-10-01T10:00:00Z", "message": map[string]any{"role": "user", "content": text}})
	name, _ := json.Marshal(map[string]any{"type": "ai-title", "aiTitle": title})
	return []store.Chunk{
		{Seq: 0, Role: "user", Text: text, Raw: string(user)},
		{Seq: 1, Role: "other", Text: "", Raw: string(name)},
	}
}

type sessionsAPI struct {
	*accessAPIFixture
	fake *embedtest.Server
	c    *embed.Client
}

// meaningSessions is the access fixture with three sessions that the query
// "q1 q2 q3 q4" finds by meaning only: one shared with the guests (rex), a private
// one of mia and a private one of rex; their texts are embedded, and the search of
// sessions uses the vectors when searchKinds names them.
func meaningSessions(t *testing.T) *sessionsAPI {
	t.Helper()
	return meaningSessionsOpen(t, false)
}

// meaningSessionsOpen with singleConn opens the database with one connection:
// a query that needs a second one while a cursor is open hangs.
func meaningSessionsOpen(t *testing.T, singleConn bool) *sessionsAPI {
	t.Helper()
	f := accessAPIOpen(t, true, singleConn)
	fake := embedtest.New(t)
	fake.Dim = 256
	fake.Concepts = map[string]string{"s1": "q1", "s2": "q2", "s3": "q3", "s4": "q4"}
	c, err := embed.New(embed.Config{URL: fake.URL})
	if err != nil {
		t.Fatal(err)
	}
	add := func(key string, account int64, owner, machine, branch, cwd, title, visibility, text string) {
		t.Helper()
		id, err := f.st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: key, AccountID: account, CWD: cwd,
			Scope: scope.Axes{Project: accProject, Machine: machine, Branch: branch}})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.st.AppendChunks(id, meaningTranscript(text, title)); err != nil {
			t.Fatal(err)
		}
		if visibility != store.VisPrivate {
			if err := f.st.SetSessionVisibility(id, f.st.Access(store.Principal{ID: "person:" + string(rune('0'+account)), Label: owner}), visibility); err != nil {
				t.Fatal(err)
			}
		}
		f.id[key] = id
	}
	add("g-rex", 4, "rex", "rex-box", "feat/x", "/home/rex/p", "Guest shared title", store.VisGuests, "s1 s2 s3 and the guest shared words around them")
	add("m-mia", 3, "mia", "mia-box", "main", "/home/mia/p", "Mia private title", store.VisPrivate, "s1 s2 s3 s4 together with the private words of mia")
	add("p-rex", 4, "rex", "rex-box", "feat/y", "/home/rex/q", "Rex private title", store.VisPrivate, "s1 s2 s3 s4 together with the private words of rex")
	job := embedjob.New(f.st, c, embedjob.Config{Batch: 16, StartDelay: -1, Kinds: []string{store.KindSession}, SessionWindow: embedjob.Window{Always: true}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	for {
		res, err := job.Step(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if res == (embedjob.Result{}) {
			break
		}
	}
	return &sessionsAPI{f, fake, c}
}

func (s *sessionsAPI) on(kinds ...string) {
	s.st.SetEmbedder(store.EmbedSearchConfig{Embedder: s.c, Kinds: kinds})
}

func (s *sessionsAPI) search(t *testing.T, who, extra string) (body string, hits []store.SessionHit) {
	t.Helper()
	body = s.expect(t, who, 200, "GET", "/api/search?kind=sessions&limit=10&q="+url.QueryEscape("q1 q2 q3 q4")+extra, nil)
	var out struct {
		Sessions []store.SessionHit `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("%v\n%s", err, body)
	}
	return body, out.Sessions
}

func titles(hits []store.SessionHit) string {
	var out []string
	for _, h := range hits {
		out = append(out, h.Session.Title)
	}
	return strings.Join(out, "|")
}

func TestSearchAPISessionsAreFoundByMeaningForWhoMayReadThem(t *testing.T) {
	f := meaningSessions(t)
	f.on(store.KindSession)

	// A guest: the one session shared with guests, as a guest sees a session, marked
	// as found by meaning.
	body, hits := f.search(t, "gus", "")
	if len(hits) != 1 || hits[0].Session.Title != "Guest shared title" || hits[0].Match != store.MatchSemantic || hits[0].Snippet == "" {
		t.Fatalf("a guest: %s", body)
	}
	if hits[0].Session.PublicID == "" || hits[0].Session.ID != 0 {
		t.Errorf("a guest knows the session by its address, not by its number: %+v", hits[0].Session)
	}
	for _, leak := range []string{"rex-box", "mia-box", "feat/", "/home/", "g-rex", `"owner"`, "Mia private", "Rex private", "private words"} {
		if strings.Contains(body, leak) {
			t.Errorf("a guest is told %q: %s", leak, body)
		}
	}
	// The host details of a session are no way to probe: a filter on them finds nothing.
	for _, filter := range []string{"&machine=rex-box", "&branch=feat/x"} {
		if _, hits := f.search(t, "gus", filter); len(hits) != 0 {
			t.Errorf("a guest probes %s: %v", filter, titles(hits))
		}
		if _, hits := f.search(t, "mia", filter); len(hits) != 1 || hits[0].Match != store.MatchSemantic {
			t.Errorf("a member lost the filter %s: %v", filter, titles(hits))
		}
	}
	// A member reads her own private session and the shared one, not the private one
	// of another.
	_, hits = f.search(t, "mia", "")
	if len(hits) != 2 || !strings.Contains(titles(hits), "Mia private title") || !strings.Contains(titles(hits), "Guest shared title") || strings.Contains(titles(hits), "Rex private") {
		t.Errorf("a member: %v", titles(hits))
	}
	// Not the project owner either: a private session is its owner's.
	_, hits = f.search(t, "robin", "")
	if titles(hits) != "Guest shared title" {
		t.Errorf("the owner of the project: %v", titles(hits))
	}
	if _, hits = f.search(t, "nora", ""); len(hits) != 0 {
		t.Errorf("somebody without a role: %v", titles(hits))
	}
	for _, who := range []string{"gus", "nora", "lena"} {
		if b, _ := f.search(t, who, ""); strings.Contains(b, "Rex private") || strings.Contains(b, "Mia private") {
			t.Errorf("%s: %s", who, b)
		}
	}
}

// Without the switch the answer is the one of the words search, to the byte, with
// vectors or without an embedding server at all.
func TestSearchAPISessionsAreUnchangedWithoutTheSwitch(t *testing.T) {
	f := meaningSessions(t)
	for _, who := range []string{"gus", "mia", "robin", "nora"} {
		f.st.SetEmbedder(store.EmbedSearchConfig{})
		plain, _ := f.search(t, who, "")
		for name, kinds := range map[string][]string{"nothing named": nil, "other kinds": {store.KindKnowledge, store.KindRequest, store.KindPain, store.KindDocument, store.KindGhost}} {
			f.on(kinds...)
			if got, _ := f.search(t, who, ""); got != plain {
				t.Errorf("%s, %s: the answer changed\n%s\n%s", who, name, plain, got)
			}
		}
	}
}

// The hint a prompt brings asks for one word and is never sent to the embedding
// server; a search is.
func TestSessionHintStaysWithWordsWhileSearchUsesMeaning(t *testing.T) {
	f := meaningSessions(t)
	f.on(store.KindSession)
	a := &api{st: f.st}
	pa := f.st.Access(store.Principal{ID: "person:3", Label: "mia"})
	before := f.fake.Inputs()
	hits, err := a.searchSessions(pa, "q1 q2 q3", scope.Axes{}, "", 5, 5, store.SearchFullTextOnly)
	if err != nil || len(hits) != 0 || f.fake.Inputs() != before {
		t.Fatalf("a hint was answered by meaning or sent to the server: %d hits, %d texts sent, %v", len(hits), f.fake.Inputs()-before, err)
	}
	hits, err = a.searchSessions(pa, "q1 q2 q3", scope.Axes{}, "", 5, 5, store.SearchAuto)
	if err != nil || len(hits) != 2 || f.fake.Inputs() != before+1 {
		t.Fatalf("a search: %d hits, %d texts sent, %v", len(hits), f.fake.Inputs()-before, err)
	}
}

// The search by meaning runs one query at a time: with a single connection to the
// database nothing hangs, neither on the API nor in the web view's search nor in
// the purge of the job.
func TestSearchOfSessionsByMeaningDoesNotHangWithASingleConnection(t *testing.T) {
	f := meaningSessionsOpen(t, true)
	f.on(store.KindSession)
	if _, hits := f.search(t, "mia", ""); len(hits) != 2 {
		t.Fatalf("%v", titles(hits))
	}
	if _, hits := f.search(t, "gus", ""); len(hits) != 1 {
		t.Fatalf("%v", titles(hits))
	}
	page, err := f.st.SearchTranscripts(f.st.Access(store.Principal{ID: "person:3", Label: "mia"}), store.SearchQuery{Q: "q1 q2 q3 q4", Limit: 10})
	if err != nil || len(page.Groups) != 2 || page.Groups[0].Match != store.MatchSemantic {
		t.Fatalf("%+v %v", page, err)
	}
	if _, err := f.st.PurgeEmbeddings(store.KindSession); err != nil {
		t.Fatal(err)
	}
	model := store.EmbedModel{Model: f.c.Model(), PrefixVersion: f.c.PrefixVersion()}
	if s, err := f.st.EmbeddingStatus(store.KindSession, model); err != nil || s.Current != 3 {
		t.Fatalf("%+v %v", s, err)
	}
	if due, err := f.st.EmbeddingDue(store.KindSession, model, 8, time.Now(), false); err != nil || len(due.Items) != 0 {
		t.Fatalf("%+v %v", due, err)
	}
}
