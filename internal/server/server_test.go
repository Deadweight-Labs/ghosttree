package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

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
