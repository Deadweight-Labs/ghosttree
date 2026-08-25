package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestBootstrapActivatesInstructionsByPath(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("test")
	root, _ := st.InsertKnowledge(store.Knowledge{Type: "instruction", Title: "root rule", Body: "root"})
	_ = root
	core, _ := st.InsertKnowledge(store.Knowledge{Type: "instruction", Title: "core rule", Body: "core"})
	if err := st.SetActivation(core, activation.Rule{Paths: []string{"core/**"}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)

	resp := req(t, "GET", srv.URL+"/api/context/bootstrap?repo_path=core", token, nil)
	raw, _ := io.ReadAll(resp.Body)
	out := string(raw)
	if !strings.Contains(out, "root rule") || !strings.Contains(out, "core rule") {
		t.Fatalf("active instructions missing:\n%s", out)
	}
	if !strings.Contains(out, "paths:core/**") {
		t.Fatalf("activation labels missing:\n%s", out)
	}
	resp = req(t, "GET", srv.URL+"/api/context/bootstrap", token, nil)
	raw, _ = io.ReadAll(resp.Body)
	out = string(raw)
	if !strings.Contains(out, "root rule") || strings.Contains(out, "core rule") {
		t.Fatalf("missing activation context must return only ungated instructions:\n%s", out)
	}
}

// A path that escapes the repository is a safety matter and stays rejected.
func TestBootstrapRejectsPathEscape(t *testing.T) {
	srv, token := newTestServer(t)
	resp := req(t, "GET", srv.URL+"/api/context/bootstrap?path=../outside", token, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// An unrecognised task is a wrong guess by the agent, not an attack. Failing
// the bootstrap over it costs the session its entire context.
func TestBootstrapToleratesUnknownTask(t *testing.T) {
	srv, token := newTestServer(t)
	resp := req(t, "GET", srv.URL+"/api/context/bootstrap?task=code%20review", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestBootstrapMentionsOpenRequestsWithoutListingThem(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("test")
	_, _ = st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "secret ledger title", Scope: scope.Axes{Project: "github.com/x/y"}}})
	_, _ = st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "change", Title: "other project", Scope: scope.Axes{Project: "github.com/a/b"}}})
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	resp := req(t, "GET", srv.URL+"/api/context/bootstrap?project=github.com/x/y", token, nil)
	raw, _ := io.ReadAll(resp.Body)
	out := string(raw)
	if !strings.Contains(out, "1 open request") || !strings.Contains(out, "substantial") {
		t.Fatalf("bootstrap missing compact ledger reminder:\n%s", out)
	}
	if strings.Contains(out, "secret ledger title") || strings.Contains(out, "other project") {
		t.Fatalf("bootstrap leaked request details:\n%s", out)
	}
}

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("test")
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	return srv, token
}

func req(t *testing.T, method, url, token string, body any) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq, _ := http.NewRequest(method, url, r)
	if token != "" {
		rq.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestPendingListsUnapprovedWithEvidence(t *testing.T) {
	srv, tok := newTestServer(t)
	req(t, "POST", srv.URL+"/api/knowledge", tok, map[string]any{
		"type": "pitfall", "title": "distilled claim", "body": "b",
		"origin": "distilled", "confidence": "quarantined"})
	req(t, "POST", srv.URL+"/api/knowledge", tok, map[string]any{
		"type": "note", "title": "an agent wrote this", "body": "b"})

	resp := req(t, "GET", srv.URL+"/api/knowledge/pending", tok, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var pending []PendingEntry
	json.NewDecoder(resp.Body).Decode(&pending)
	if len(pending) != 1 {
		t.Fatalf("pending = %d entries, want 1 (the trusted one is not pending)", len(pending))
	}
	if pending[0].Knowledge.Title != "distilled claim" {
		t.Errorf("wrong entry: %+v", pending[0].Knowledge)
	}
}

func TestApprovingKnowledgeRecordsTheAuthenticatedConfirmer(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	philipp, _ := st.AddPerson("philipp")
	id, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: "claim", Body: "b", Person: "robin", Confidence: "staged"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	resp := req(t, "PATCH", srv.URL+"/api/knowledge/"+fmt.Sprint(id), philipp, map[string]string{"confidence": "verified", "confirmed_by": "robin"})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	got, err := st.KnowledgeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfirmedBy != "philipp" {
		t.Fatalf("confirmed_by = %q, want authenticated person philipp", got.ConfirmedBy)
	}
}

func TestTwoPeopleKnowledgePatchShowsBothAuthorAndLastEditor(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	robin, _ := st.AddPerson("robin")
	philipp, _ := st.AddPerson("philipp")
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)

	created := req(t, "POST", srv.URL+"/api/knowledge", robin, map[string]string{
		"type": "note", "title": "Alice schreibt", "body": "b",
	})
	var entry store.Knowledge
	if err := json.NewDecoder(created.Body).Decode(&entry); err != nil {
		t.Fatal(err)
	}
	if resp := req(t, "PATCH", srv.URL+"/api/knowledge/"+fmt.Sprint(entry.ID), philipp,
		map[string]string{"title": "Bob korrigiert"}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("patch status = %d, want 204", resp.StatusCode)
	}
	resp := req(t, "GET", srv.URL+"/api/knowledge/"+fmt.Sprint(entry.ID), philipp, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, want 200", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&entry); err != nil {
		t.Fatal(err)
	}
	if entry.Person != "robin" || entry.LastModifiedBy != "philipp" {
		t.Fatalf("provenance = author %q, last editor %q, want robin/philipp", entry.Person, entry.LastModifiedBy)
	}
	history := req(t, "GET", srv.URL+"/api/knowledge/"+fmt.Sprint(entry.ID)+"/history", philipp, nil)
	if history.StatusCode != http.StatusOK {
		t.Fatalf("history status = %d, want 200", history.StatusCode)
	}
	var versions []store.KnowledgeVersion
	if err := json.NewDecoder(history.Body).Decode(&versions); err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].Title != "Alice schreibt" || versions[0].ChangedBy != "philipp" {
		t.Fatalf("history = %+v, want Alice's version replaced by philipp", versions)
	}
}

func TestBootstrapNamesAuthorAndConfirmerWhenTheyChangeTheMeaning(t *testing.T) {
	out := RenderBootstrap([]store.Knowledge{
		{Type: "pitfall", Title: "human", Body: "b", Origin: "human", Person: "robin", Confidence: "trusted", Status: "active"},
		{Type: "pitfall", Title: "agent", Body: "b", Origin: "agent", Person: "philipp", Confidence: "trusted", Status: "active"},
		{Type: "pitfall", Title: "distilled", Body: "b", Origin: "distilled", Confidence: "trusted", Status: "active"},
		{Type: "pitfall", Title: "verified", Body: "b", Origin: "agent", Person: "robin", ConfirmedBy: "philipp", Confidence: "verified", Status: "active"},
	}, 12000)
	for _, want := range []string{"by robin", "by philipp", "confirmed by philipp"} {
		if !strings.Contains(out, want) {
			t.Errorf("bootstrap lacks %q:\n%s", want, out)
		}
	}
}

func TestApproveRaisesConfidence(t *testing.T) {
	srv, tok := newTestServer(t)
	req(t, "POST", srv.URL+"/api/knowledge", tok, map[string]any{
		"type": "pitfall", "title": "distilled claim", "body": "b",
		"origin": "distilled", "confidence": "quarantined"})
	if resp := req(t, "PATCH", srv.URL+"/api/knowledge/1", tok,
		map[string]string{"confidence": "verified"}); resp.StatusCode != 204 {
		t.Fatalf("patch status = %d", resp.StatusCode)
	}
	resp := req(t, "GET", srv.URL+"/api/knowledge/pending", tok, nil)
	var pending []PendingEntry
	json.NewDecoder(resp.Body).Decode(&pending)
	if len(pending) != 0 {
		t.Errorf("approved entry must leave the pending list, got %+v", pending)
	}
}

func TestBootstrapExcludesStagedUnlessPreviewed(t *testing.T) {
	confirmed := store.Knowledge{Type: "pitfall", Title: "confirmed thing", Body: "b", Confidence: "trusted"}
	staged := store.Knowledge{Type: "pitfall", Title: "unconfirmed thing", Body: "b", Confidence: "staged"}
	out := RenderBootstrap([]store.Knowledge{confirmed, staged}, 4000)
	if !strings.Contains(out, "confirmed thing") || strings.Contains(out, "unconfirmed thing") {
		t.Fatalf("binding bootstrap leaked staged knowledge:\n%s", out)
	}
	preview := RenderBootstrapPreview([]store.Knowledge{confirmed, staged}, 4000)
	if !strings.Contains(preview, "unconfirmed thing") || !strings.Contains(preview, "Unconfirmed preview") {
		t.Fatalf("explicit preview omitted staged knowledge:\n%s", preview)
	}
}

// The budget must cut the uncertain material first, not the proven material.
// Both are pitfalls because only pushed types reach the budget at all.
func TestBootstrapBudgetDropsStagedFirst(t *testing.T) {
	entries := []store.Knowledge{
		{Type: "pitfall", Title: "trusted one", Body: strings.Repeat("x", 150), Confidence: "trusted"},
		{Type: "pitfall", Title: "staged one", Body: strings.Repeat("y", 150), Confidence: "staged"},
	}
	out := RenderBootstrap(entries, 260)
	if !strings.Contains(out, "trusted one") {
		t.Errorf("trusted entry must survive a tight budget:\n%s", out)
	}
	if strings.Contains(out, "staged one") {
		t.Errorf("staged entry should have been cut first:\n%s", out)
	}
}

func TestBootstrapPutsInstructionsFirstAndComplete(t *testing.T) {
	long := strings.Repeat("x", 600)
	entries := []store.Knowledge{
		{Type: "pitfall", Title: "some pitfall", Body: "b", Confidence: "trusted"},
		{Type: "instruction", Title: "how to build", Body: long, Confidence: "verified"},
		{Type: "instruction", Title: "unreviewed rule", Body: "b", Confidence: "staged"},
	}
	out := RenderBootstrap(entries, 800)

	iInstr := strings.Index(out, "how to build")
	iPit := strings.Index(out, "some pitfall")
	if iInstr < 0 {
		t.Fatalf("instruction missing:\n%s", out)
	}
	if iPit >= 0 && iInstr > iPit {
		t.Errorf("instructions must come before everything else:\n%s", out)
	}
	if !strings.Contains(out, long) {
		t.Error("instruction bodies must not be truncated")
	}
	if strings.Contains(out, "unreviewed rule") {
		t.Error("a staged instruction must not be binding")
	}
	out = RenderBootstrapPreview(entries, 800)
	line := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "unreviewed rule") {
			line = l
		}
	}
	if !strings.Contains(strings.ToLower(line), "preview only") {
		t.Errorf("staged instruction must be marked on its line, got %q", line)
	}
}

func TestRawExportReturnsNDJSON(t *testing.T) {
	srv, tok := newTestServer(t)
	req(t, "POST", srv.URL+"/api/sessions", tok, store.Session{
		Harness: "claude-code", ExternalID: "e1", CWD: "/x", StartedAt: "2026-08-23T00:00:00Z"})
	req(t, "POST", srv.URL+"/api/sessions/1/chunks", tok, map[string]any{"chunks": []store.Chunk{
		{Seq: 0, Role: "user", Text: "hello", Raw: `{"n":0}`},
		{Seq: 1, Role: "other", Text: "", Raw: `{"n":1}`},
	}})

	resp := req(t, "GET", srv.URL+"/api/sessions/1/raw", tok, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "x-ndjson") {
		t.Errorf("content type = %q, want x-ndjson", ct)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "{\"n\":0}\n{\"n\":1}\n" {
		t.Errorf("body = %q", b)
	}
}

func TestRawExportRequiresAuth(t *testing.T) {
	srv, _ := newTestServer(t)
	if resp := req(t, "GET", srv.URL+"/api/sessions/1/raw", "", nil); resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestAuthRequired(t *testing.T) {
	srv, _ := newTestServer(t)
	if resp := req(t, "GET", srv.URL+"/api/knowledge", "", nil); resp.StatusCode != 401 {
		t.Errorf("no token: status = %d, want 401", resp.StatusCode)
	}
	if resp := req(t, "GET", srv.URL+"/api/health", "", nil); resp.StatusCode != 200 {
		t.Errorf("health must be public, got %d", resp.StatusCode)
	}
}

func TestKnowledgeAutoScopeAndBootstrap(t *testing.T) {
	srv, tok := newTestServer(t)
	body := map[string]any{
		"type": "pitfall", "title": "sequence ids collide", "body": "upstream not concurrency-safe",
		"auto_scope": map[string]any{"context": map[string]string{
			"project": "github.com/x/y", "branch": "main", "machine": "workstation-a"}},
	}
	resp := req(t, "POST", srv.URL+"/api/knowledge", tok, body)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var saved store.Knowledge
	json.NewDecoder(resp.Body).Decode(&saved)
	// The branch of the writing session is deliberately not carried over: a
	// pitfall found on main is not a property of main.
	if saved.Scope.Project != "github.com/x/y" || saved.Scope.Branch != "" || saved.Scope.Machine != "" {
		t.Errorf("auto scope wrong: %+v", saved.Scope)
	}
	resp = req(t, "GET", srv.URL+"/api/context/bootstrap?project=github.com/x/y&branch=main&machine=workstation-a", tok, nil)
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "sequence ids collide") {
		t.Errorf("bootstrap missing entry: %s", b)
	}
	// And it reaches a different branch, which is the point of the change.
	resp = req(t, "GET", srv.URL+"/api/context/bootstrap?project=github.com/x/y&branch=feat/other&machine=workstation-a", tok, nil)
	b, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "sequence ids collide") {
		t.Errorf("entry did not survive a branch switch: %s", b)
	}
}

func TestSessionFlowAndSearch(t *testing.T) {
	srv, tok := newTestServer(t)
	resp := req(t, "POST", srv.URL+"/api/sessions", tok, store.Session{
		Harness: "claude-code", ExternalID: "s1", CWD: "/x", StartedAt: "2026-08-23T00:00:00Z"})
	var created struct {
		ID int64 `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp = req(t, "POST", srv.URL+"/api/sessions/1/chunks", tok,
		map[string]any{"chunks": []store.Chunk{{Seq: 0, Role: "user", Text: "debugging livekit sfu", Raw: "{}"}}})
	if resp.StatusCode != 204 {
		t.Fatalf("chunks status = %d", resp.StatusCode)
	}
	resp = req(t, "GET", srv.URL+"/api/search?q=livekit&kind=all", tok, nil)
	var res struct {
		Knowledge []store.Knowledge  `json:"knowledge"`
		Sessions  []store.SessionHit `json:"sessions"`
	}
	json.NewDecoder(resp.Body).Decode(&res)
	if len(res.Sessions) != 1 {
		t.Errorf("session hits = %d, want 1", len(res.Sessions))
	}
}

func TestPatchAndListSessions(t *testing.T) {
	srv, tok := newTestServer(t)
	resp := req(t, "POST", srv.URL+"/api/knowledge", tok, map[string]any{
		"type": "note", "title": "temp", "body": "x"})
	var saved store.Knowledge
	json.NewDecoder(resp.Body).Decode(&saved)
	if resp = req(t, "PATCH", srv.URL+"/api/knowledge/1", tok,
		map[string]string{"status": "deprecated"}); resp.StatusCode != 204 {
		t.Fatalf("patch status = %d", resp.StatusCode)
	}
	resp = req(t, "GET", srv.URL+"/api/knowledge", tok, nil)
	var ks []store.Knowledge
	json.NewDecoder(resp.Body).Decode(&ks)
	if len(ks) != 0 {
		t.Errorf("deprecated entry must not be returned: %+v", ks)
	}

	req(t, "POST", srv.URL+"/api/sessions", tok, store.Session{
		Harness: "codex", ExternalID: "s9", StartedAt: "2026-08-23T00:00:00Z",
		Scope: scope.Axes{Project: "github.com/x/y", Machine: "workstation-a"}})
	resp = req(t, "GET", srv.URL+"/api/sessions?machine=workstation-a", tok, nil)
	var sessions []store.Session
	json.NewDecoder(resp.Body).Decode(&sessions)
	if len(sessions) != 1 || sessions[0].ExternalID != "s9" {
		t.Errorf("sessions = %+v", sessions)
	}
	resp = req(t, "GET", srv.URL+"/api/sessions/1?from=0", tok, nil)
	var chunks []store.Chunk
	json.NewDecoder(resp.Body).Decode(&chunks)
	if len(chunks) != 0 {
		t.Errorf("chunks = %+v", chunks)
	}
}

// Machine- and global-scoped knowledge is bounded by construction: there is one
// machine and one world, and neither grows when a repository is added. Project
// knowledge is unbounded. Letting the unbounded set crowd out the bounded one
// is how releasing 136 distilled items removed the two machine notes that were
// the only entries in the archive with a measured effect.
func TestBootstrapReservesRoomForBroadScope(t *testing.T) {
	var entries []store.Knowledge
	for i := 0; i < 40; i++ {
		entries = append(entries, store.Knowledge{
			Type: "pitfall", Title: fmt.Sprintf("project note %d", i), Body: strings.Repeat("p", 120),
			Scope: scope.Axes{Project: "github.com/x/y"}, Confidence: "trusted",
		})
	}
	// Same type as the project entries and last in rank order, so only scope
	// can save it.
	entries = append(entries, store.Knowledge{
		Type: "pitfall", Title: "machine wide truth", Body: strings.Repeat("m", 120),
		Scope: scope.Axes{Machine: "workstation-a"}, Confidence: "trusted",
	})
	out := RenderBootstrap(entries, 1200)
	if !strings.Contains(out, "machine wide truth") {
		t.Errorf("machine-scoped entry was crowded out by project knowledge:\n%s", out)
	}
	if !strings.Contains(out, "truncated") {
		t.Fatalf("test needs a budget that actually truncates:\n%s", out)
	}
}

// A pitfall stops a mistake that is about to happen; a decision explains one
// already made. When the budget cuts, it must cut the explanation.
func TestBootstrapPrefersPitfallsOverDecisions(t *testing.T) {
	var entries []store.Knowledge
	for i := 0; i < 20; i++ {
		entries = append(entries, store.Knowledge{
			Type: "decision", Title: fmt.Sprintf("decision %d", i), Body: strings.Repeat("d", 120),
			Confidence: "trusted",
		})
	}
	entries = append(entries, store.Knowledge{
		Type: "pitfall", Title: "the trap", Body: strings.Repeat("t", 120), Confidence: "trusted",
	})
	out := RenderBootstrap(entries, 900)
	if !strings.Contains(out, "the trap") {
		t.Errorf("pitfall must outrank decisions under a tight budget:\n%s", out)
	}
}

// The bootstrap is pushed into every session in every project, so it may only
// carry what changes behaviour before anyone thinks to ask. A pitfall qualifies:
// nobody searches for a mistake they do not know they are about to make. An
// inventory of the local Ollama models does not — it is reference material, and
// you know when you need it. Measured on 2026-08-24: that note had been
// delivered 62 times and matched a search zero times.
func TestBootstrapPushesPitfallsAndIndexesTheRest(t *testing.T) {
	entries := []store.Knowledge{
		{Type: "instruction", Title: "german docs", Body: "b", Confidence: "trusted"},
		{Type: "pitfall", Title: "dead binary in PATH", Body: "b", Confidence: "trusted"},
		{Type: "note", Title: "ollama inventory", Body: "b", Confidence: "trusted"},
		{Type: "decision", Title: "sqlite over postgres", Body: "b", Confidence: "trusted"},
		{Type: "decision", Title: "no migration framework", Body: "b", Confidence: "trusted"},
		{Type: "plan", Title: "distiller piece 3", Body: "b", Confidence: "trusted"},
	}
	out := RenderBootstrap(entries, 4000)

	for _, want := range []string{"german docs", "dead binary in PATH"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q must be pushed:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"ollama inventory", "sqlite over postgres", "distiller piece 3"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("%q is reference material and must not be pushed:\n%s", unwanted, out)
		}
	}
	// Withholding without saying so would hide the knowledge instead of
	// deferring it: the agent has to learn that there is something to search for.
	if !strings.Contains(out, "2 decisions") || !strings.Contains(out, "1 note") || !strings.Contains(out, "1 plan") {
		t.Errorf("held-back knowledge must be announced by kind and count:\n%s", out)
	}
	if !strings.Contains(out, "context_search") {
		t.Errorf("the index must name the way to reach it:\n%s", out)
	}
}

// Nothing held back means nothing to announce.
func TestBootstrapOmitsTheIndexWhenEverythingWasPushed(t *testing.T) {
	out := RenderBootstrap([]store.Knowledge{
		{Type: "pitfall", Title: "only a pitfall", Body: "b", Confidence: "trusted"},
	}, 4000)
	if strings.Contains(out, "context_search for") {
		t.Errorf("empty index must not be printed:\n%s", out)
	}
}

// Broad and project knowledge are written in two passes so the bounded set gets
// its reserve, but they are one list to the reader. A type heading per pass made
// the same heading appear twice.
func TestBootstrapWritesEachTypeHeadingOnce(t *testing.T) {
	out := RenderBootstrap([]store.Knowledge{
		{Type: "pitfall", Title: "machine one", Body: "b", Scope: scope.Axes{Machine: "workstation-a"}, Confidence: "trusted"},
		{Type: "pitfall", Title: "project one", Body: "b", Scope: scope.Axes{Project: "github.com/x/y"}, Confidence: "trusted"},
	}, 4000)
	if n := strings.Count(out, "### pitfall"); n != 1 {
		t.Errorf("heading appears %d times, want 1:\n%s", n, out)
	}
	for _, want := range []string{"machine one", "project one"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing:\n%s", want, out)
		}
	}
}

func TestGhostEndpointsStoreAndDeliver(t *testing.T) {
	srv, token := newTestServer(t)

	res := req(t, "POST", srv.URL+"/api/ghosts", token, map[string]any{
		"project": "p", "path": "internal/store/knowledge.go", "kind": "file",
		"description": "Lese- und Schreibpfade", "content_sha": "sha1",
		"git_blob": "blob1", "line_count": 545,
	})
	if res.StatusCode != 200 {
		t.Fatalf("POST /api/ghosts: %d", res.StatusCode)
	}
	res.Body.Close()

	var got []store.GhostFile
	res = req(t, "GET", srv.URL+"/api/ghosts?project=p&path=internal/store/knowledge.go&session=claude:s1", token, nil)
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(got) != 1 || got[0].Description != "Lese- und Schreibpfade" {
		t.Fatalf("delivery returned %+v", got)
	}

	// Zweiter Aufruf derselben Session: schon gesagt.
	res = req(t, "GET", srv.URL+"/api/ghosts?project=p&path=internal/store/knowledge.go&session=claude:s1", token, nil)
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(got) != 0 {
		t.Fatalf("the same path must not be delivered twice in one session: %+v", got)
	}
}

// Eine Route, die nicht registriert ist, ist still kaputt: der Client bekommt
// 404 und schluckt es, weil die Auskunft eine Zugabe ist.
func TestGhostHistoryAndMoveEndpoints(t *testing.T) {
	srv, token := newTestServer(t)
	put := func(path, desc string) {
		t.Helper()
		res := req(t, "POST", srv.URL+"/api/ghosts", token, map[string]any{
			"project": "p", "path": path, "kind": "file", "description": desc})
		if res.StatusCode != 200 {
			t.Fatalf("POST /api/ghosts: %d", res.StatusCode)
		}
		res.Body.Close()
	}
	put("alt.go", "die erste Fassung")
	put("alt.go", "die zweite Fassung")

	var versions []store.GhostVersion
	res := req(t, "GET", srv.URL+"/api/ghosts/history?project=p&path=alt.go", token, nil)
	if err := json.NewDecoder(res.Body).Decode(&versions); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(versions) != 1 || versions[0].Description != "die erste Fassung" {
		t.Fatalf("die verdraengte Fassung muss abrufbar sein: %+v", versions)
	}

	// Wer den Unterschied sehen will, braucht zur abgeloesten Fassung ihren
	// Nachfolger — und der steht nicht in der Historie, sondern ist die
	// aktuelle Beschreibung.
	res = req(t, "GET", srv.URL+"/api/ghosts/history?project=p&path=alt.go&chain=1", token, nil)
	if err := json.NewDecoder(res.Body).Decode(&versions); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(versions) != 2 || versions[0].Description != "die zweite Fassung" {
		t.Fatalf("die Kette beginnt bei der aktuellen Fassung: %+v", versions)
	}

	// Der Hook holt nur die Zahl.
	var counted struct {
		Count int `json:"count"`
	}
	res = req(t, "GET", srv.URL+"/api/ghosts/history?project=p&path=alt.go&count=1", token, nil)
	if err := json.NewDecoder(res.Body).Decode(&counted); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if counted.Count != 1 {
		t.Fatalf("count = %d, want 1", counted.Count)
	}

	res = req(t, "POST", srv.URL+"/api/ghosts/move", token,
		map[string]any{"project": "p", "from": "alt.go", "to": "neu.go"})
	if res.StatusCode != 200 {
		t.Fatalf("POST /api/ghosts/move: %d", res.StatusCode)
	}
	res.Body.Close()

	res = req(t, "GET", srv.URL+"/api/ghosts/history?project=p&path=neu.go", token, nil)
	if err := json.NewDecoder(res.Body).Decode(&versions); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(versions) != 2 {
		t.Fatalf("die Historie zieht mit um, plus Umzugsvermerk: %+v", versions)
	}
}

func TestGhostSearchEndpointFindsByDescription(t *testing.T) {
	srv, token := newTestServer(t)
	res := req(t, "POST", srv.URL+"/api/ghosts", token, map[string]any{
		"project": "p", "path": "a.go", "kind": "file", "description": "Rangfolge nach Vertrauen",
	})
	res.Body.Close()

	res = req(t, "GET", srv.URL+"/api/ghosts/search?q=Vertrauen&project=p", token, nil)
	var got []store.GhostFile
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(got) != 1 || got[0].Path != "a.go" {
		t.Fatalf("search returned %+v", got)
	}
}
