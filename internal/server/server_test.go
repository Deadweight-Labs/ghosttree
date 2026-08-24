package server

import (
	"bytes"
	"encoding/json"
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

func TestBootstrapActivatesInstructionsByPathAndTask(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("test")
	root, _ := st.InsertKnowledge(store.Knowledge{Type: "instruction", Title: "root rule", Body: "root"})
	_ = root
	core, _ := st.InsertKnowledge(store.Knowledge{Type: "instruction", Title: "core rule", Body: "core"})
	if err := st.SetActivation(core, activation.Rule{Paths: []string{"core/**"}, Tasks: []string{"code"}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)

	resp := req(t, "GET", srv.URL+"/api/context/bootstrap?repo_path=core&task=code", token, nil)
	raw, _ := io.ReadAll(resp.Body)
	out := string(raw)
	if !strings.Contains(out, "root rule") || !strings.Contains(out, "core rule") {
		t.Fatalf("active instructions missing:\n%s", out)
	}
	if !strings.Contains(out, "paths:core/**") || !strings.Contains(out, "tasks:code") {
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
func TestBootstrapBudgetDropsStagedFirst(t *testing.T) {
	entries := []store.Knowledge{
		{Type: "note", Title: "trusted one", Body: strings.Repeat("x", 150), Confidence: "trusted"},
		{Type: "note", Title: "staged one", Body: strings.Repeat("y", 150), Confidence: "staged"},
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
	if saved.Scope.Project != "github.com/x/y" || saved.Scope.Branch != "main" || saved.Scope.Machine != "" {
		t.Errorf("auto scope wrong: %+v", saved.Scope)
	}
	resp = req(t, "GET", srv.URL+"/api/context/bootstrap?project=github.com/x/y&branch=main&machine=workstation-a", tok, nil)
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "sequence ids collide") {
		t.Errorf("bootstrap missing entry: %s", b)
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
