package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestHTTPWriterSaturationIsRetryableAcrossMutationDomains(t *testing.T) {
	cfg := store.DefaultWriterConfig()
	cfg.MaxOperations = 1
	st, err := store.OpenRuntime(filepath.Join(t.TempDir(), "api.db"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, err := st.AddPerson("writer")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetContextSnapshotAccess("writer", "p", true, true, true); err != nil {
		t.Fatal(err)
	}
	sid, err := st.UpsertSession(store.Session{Harness: "test", ExternalID: "seed"})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := st.CreateDocument(store.Document{Project: "p", Slug: "seed", Kind: "spec", Title: "seed"}, "body", "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutGhostFile(store.GhostFile{Project: "p", Path: "seed.go", Description: "retained"}); err != nil {
		t.Fatal(err)
	}
	candidate, err := st.PrepareGhostArchive("p", "seed.go")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	release := holdHTTPWriter(t, st)
	for _, tc := range []struct {
		name, method, path string
		body               any
		code               string
	}{
		{"session", "POST", "/api/sessions", store.Session{Harness: "test", ExternalID: "rejected"}, "writer_busy"},
		{"chunks", "POST", fmt.Sprintf("/api/sessions/%d/chunks", sid), map[string]any{"chunks": []store.Chunk{{Seq: 1, Text: "rejected", Raw: "raw"}}}, "writer_busy"},
		{"knowledge", "POST", "/api/knowledge", map[string]string{"type": "note", "title": "rejected", "body": "body"}, "writer_busy"},
		{"ghost", "POST", "/api/ghosts", store.GhostFile{Project: "p", Path: "rejected.go", Description: "body"}, "writer_busy"},
		{"delivery", "GET", "/api/ghosts?project=p&path=seed.go&session=test", nil, "writer_busy"},
		{"archive", "POST", "/api/ghosts/archive", store.GhostArchiveInput{Project: "p", Targets: []store.GhostArchiveTarget{candidate.Target}, Reason: "deleted", ConfirmDeleted: true}, "writer_busy"},
		{"document", "POST", "/api/documents", map[string]string{"project": "p", "slug": "rejected", "kind": "spec", "title": "rejected", "body": "body"}, "writer_busy"},
		{"revision", "PUT", fmt.Sprintf("/api/documents/%d/revisions", doc.ID), documentRevisionRequest{BaseRevision: 1, Body: "rejected"}, "writer_busy"},
		{"request", "POST", "/api/requests", map[string]string{"type": "change", "title": "rejected", "project": "p", "idempotency_key": "rejected"}, "writer_busy"},
		{"snapshot", "POST", "/api/context-snapshots", snapshot.CreateInput{Project: "p", Name: "capture", Git: snapshot.GitProvenance{ObjectFormat: "sha1", Commit: strings.Repeat("a", 40)}}, "snapshot_store_busy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := req(t, tc.method, srv.URL+tc.path, token, tc.body)
			defer response.Body.Close()
			var body struct {
				Code      string `json:"code"`
				Retryable bool   `json:"retryable"`
			}
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusServiceUnavailable || response.Header.Get("Retry-After") != "1" || body.Code != tc.code || !body.Retryable {
				t.Fatalf("status=%d retry=%q body=%+v", response.StatusCode, response.Header.Get("Retry-After"), body)
			}
		})
	}
	response := req(t, "POST", srv.URL+"/api/sessions", token, map[string]string{})
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("validation changed under saturation: %d", response.StatusCode)
	}
	release()
	var n int
	if err := st.DB().QueryRow("SELECT count(*) FROM sessions WHERE external_id='rejected'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("rejected session persisted=%d %v", n, err)
	}
	revision, err := st.DocumentByID(doc.ID)
	if err != nil || revision.HeadRevision != 1 {
		t.Fatalf("rejected revision persisted: %+v %v", revision, err)
	}
}

func holdHTTPWriter(t *testing.T, st *store.Store) func() {
	t.Helper()
	conn, err := st.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before := st.DB().Stats().WaitCount
	done := make(chan error, 1)
	go func() { _, err := st.UpsertSession(store.Session{Harness: "test", ExternalID: "barrier"}); done <- err }()
	var once sync.Once
	release := func() {
		once.Do(func() {
			conn.Close()
			if err := <-done; err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(release)
	deadline := time.Now().Add(3 * time.Second)
	for st.DB().Stats().WaitCount == before {
		if time.Now().After(deadline) {
			t.Fatal("writer did not acquire admission")
		}
		time.Sleep(time.Millisecond)
	}
	return release
}

func TestWriterErrorsPreserveRetryAndPermanentFailures(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
		retry  bool
	}{
		{store.ErrWriterBytesFull, 503, "writer_busy", true},
		{store.ErrWriterClosed, 503, "writer_closed", true},
		{store.ErrWriterOversized, 413, "writer_payload_too_large", false},
		{store.ErrWriterInvalidPayload, 400, "writer_invalid_payload", false},
	} {
		t.Run(tc.code, func(t *testing.T) {
			for _, snapshotOperation := range []bool{false, true} {
				w := httptest.NewRecorder()
				if !writeWriterError(w, fmt.Errorf("wrapped: %w", tc.err), snapshotOperation) {
					t.Fatal("wrapped writer error not recognized")
				}
				var out struct {
					Code      string `json:"code"`
					Retryable bool   `json:"retryable"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				status, code := tc.status, tc.code
				if snapshotOperation {
					if tc.retry {
						code = "snapshot_store_busy"
					} else if tc.status == 413 {
						status = 422
						code = "snapshot_limit_exceeded"
					} else {
						code = "snapshot_invalid_input"
					}
				}
				if w.Code != status || out.Code != code || out.Retryable != tc.retry || (w.Header().Get("Retry-After") == "1") != tc.retry {
					t.Fatalf("snapshot=%v status=%d out=%+v headers=%v", snapshotOperation, w.Code, out, w.Header())
				}
			}
		})
	}
}
