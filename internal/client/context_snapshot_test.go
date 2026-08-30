package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

func TestContextSnapshotClientRoundTripsTask8Routes(t *testing.T) {
	head, entries := clientSnapshotFixture()
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/context-snapshots":
			var in snapshot.CreateInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatal(err)
			}
			if in.Project != "project+a" || in.Name != head.Name {
				t.Fatalf("create input = %+v", in)
			}
			writeClientSnapshotJSON(t, w, map[string]any{"snapshot": head, "counts": head.Counts, "created": true, "warnings": []snapshot.Warning{}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/context-snapshots":
			if r.URL.Query().Get("project") != "project+a" || r.URL.Query().Get("cursor") != "opaque+cursor" || r.URL.Query().Get("limit") != "7" {
				t.Fatalf("list query = %q", r.URL.RawQuery)
			}
			writeClientSnapshotJSON(t, w, snapshot.SnapshotPage{Snapshots: []snapshot.Head{head}, NextCursor: "next"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/context-snapshots/"+head.Name:
			writeClientSnapshotJSON(t, w, map[string]any{"snapshot": head, "counts": head.Counts})
		case r.Method == http.MethodGet && r.URL.Path == "/api/context-snapshots/"+head.Name+"/entries":
			q := r.URL.Query()
			if q.Get("project") != "project+a" || q.Get("domain") != "knowledge" || q.Get("key") != "key+one" || q.Get("cursor") != "entry+cursor" || q.Get("limit") != "5" {
				t.Fatalf("entry query = %q", r.URL.RawQuery)
			}
			writeClientSnapshotJSON(t, w, snapshot.EntryPage{Exact: &entries[0]})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := New(config.Config{ServerURL: srv.URL, Token: "token"})
	ctx := context.Background()

	created, err := c.CreateContextSnapshot(ctx, snapshot.CreateInput{Project: "project+a", Name: head.Name})
	if err != nil || !created.Created || created.Snapshot.Counts["knowledge"] != 1 {
		t.Fatalf("create = %+v, err=%v", created, err)
	}
	page, err := c.ContextSnapshots(ctx, snapshot.ListFilter{Project: "project+a", Cursor: "opaque+cursor", Limit: 7})
	if err != nil || page.NextCursor != "next" || len(page.Snapshots) != 1 {
		t.Fatalf("list = %+v, err=%v", page, err)
	}
	gotHead, counts, err := c.ContextSnapshot(ctx, "project+a", head.Name)
	if err != nil || gotHead.Name != head.Name || !reflect.DeepEqual(counts, head.Counts) || !reflect.DeepEqual(gotHead.Counts, counts) {
		t.Fatalf("head = %+v counts=%v err=%v", gotHead, counts, err)
	}
	entryPage, err := c.ContextSnapshotEntries(ctx, "project+a", head.Name, snapshot.EntryFilter{Domain: "knowledge", Key: "key+one", Cursor: "entry+cursor", Limit: 5})
	if err != nil || entryPage.Exact == nil || !bytes.Equal(entryPage.Exact.Payload, entries[0].Payload) {
		t.Fatalf("entries = %+v, err=%v", entryPage, err)
	}
	for _, request := range requests {
		if strings.Contains(request, head.Name) && !strings.Contains(request, "v1.2.3%2Bmeta") {
			t.Fatalf("snapshot name plus was not path-escaped: %s", request)
		}
	}
}

func TestContextSnapshotClientMapsTask8ErrorsToRuleError(t *testing.T) {
	cases := []struct {
		status    int
		code      string
		retryable bool
	}{
		{409, "snapshot_name_conflict", false},
		{409, "snapshot_tag_mismatch", false},
		{409, "snapshot_dirty_worktree", false},
		{409, "snapshot_git_changed", true},
		{403, "snapshot_release_binding_forbidden", false},
		{404, "snapshot_entry_not_found", false},
		{422, "snapshot_limit_exceeded", false},
		{422, "unsupported_snapshot_schema", false},
		{503, "snapshot_store_busy", true},
		{507, "snapshot_storage_exhausted", false},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				writeClientSnapshotJSON(t, w, map[string]any{
					"code": tc.code, "message": "message", "resolution": "resolution",
					"details": map[string]any{"existing_digest": "abc"}, "retryable": tc.retryable,
					"existing_digest": "old-digest", "requested_digest": "new-digest",
					"existing_git_commit": "old-commit", "requested_git_commit": "new-commit",
				})
			}))
			t.Cleanup(srv.Close)
			_, err := New(config.Config{ServerURL: srv.URL}).ContextSnapshots(context.Background(), snapshot.ListFilter{Project: "p"})
			var rule *snapshot.RuleError
			if !errors.As(err, &rule) || rule.Code != tc.code || rule.Retryable != tc.retryable || rule.Message != "message" || rule.Resolution != "resolution" || rule.Details["existing_digest"] != "abc" || rule.ExistingDigest != "old-digest" || rule.RequestedDigest != "new-digest" || rule.ExistingGitCommit != "old-commit" || rule.RequestedGitCommit != "new-commit" {
				t.Fatalf("error = %#v (%T), want structured %s", err, err, tc.code)
			}
		})
	}
}

func TestContextSnapshotClientExportsStableRawPayloadAcrossPages(t *testing.T) {
	head, entries := clientSnapshotFixture()
	var summaryCursors []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/context-snapshots/"+head.Name:
			writeClientSnapshotJSON(t, w, map[string]any{"snapshot": head, "counts": head.Counts})
		case r.URL.Path == "/api/context-snapshots/"+head.Name+"/entries" && r.URL.Query().Get("key") != "":
			key := r.URL.Query().Get("key")
			for i := range entries {
				if entries[i].Key == key {
					writeClientSnapshotJSON(t, w, snapshot.EntryPage{Exact: &entries[i]})
					return
				}
			}
			http.NotFound(w, r)
		case r.URL.Path == "/api/context-snapshots/"+head.Name+"/entries":
			cursor := r.URL.Query().Get("cursor")
			summaryCursors = append(summaryCursors, cursor)
			if cursor == "" {
				writeClientSnapshotJSON(t, w, snapshot.EntryPage{Entries: []snapshot.EntrySummary{entrySummary(entries[0])}, NextCursor: "page+2"})
				return
			}
			writeClientSnapshotJSON(t, w, snapshot.EntryPage{Entries: []snapshot.EntrySummary{entrySummary(entries[1])}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := New(config.Config{ServerURL: srv.URL})

	var first, second bytes.Buffer
	if err := c.ExportContextSnapshot(context.Background(), "project+a", head.Name, nil, &first); err != nil {
		t.Fatal(err)
	}
	if err := c.ExportContextSnapshot(context.Background(), "project+a", head.Name, nil, &second); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) || sha256.Sum256(first.Bytes()) != sha256.Sum256(second.Bytes()) {
		t.Fatal("repeated exports differ")
	}
	if !bytes.Contains(first.Bytes(), []byte(`"payload":{"a":1}`)) || !bytes.Contains(first.Bytes(), []byte(`"payload":{"b":[2,3]}`)) {
		t.Fatalf("raw payload bytes changed: %s", first.Bytes())
	}
	if !reflect.DeepEqual(summaryCursors, []string{"", "page+2", "", "page+2"}) {
		t.Fatalf("summary cursors = %#v", summaryCursors)
	}
	verification, err := c.VerifyContextSnapshot(context.Background(), "project+a", head.Name)
	if err != nil || verification.Digest != head.ContentDigest || !verification.Full || verification.EntryCount != 2 {
		t.Fatalf("verification = %+v, err=%v", verification, err)
	}
}

func TestContextSnapshotClientRejectsPaginationCyclesAndPropagatesWriterErrors(t *testing.T) {
	head, _ := clientSnapshotFixture()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/entries") {
			writeClientSnapshotJSON(t, w, snapshot.EntryPage{NextCursor: "cycle"})
			return
		}
		writeClientSnapshotJSON(t, w, map[string]any{"snapshot": head, "counts": head.Counts})
	}))
	t.Cleanup(srv.Close)
	c := New(config.Config{ServerURL: srv.URL})
	if err := c.ExportContextSnapshot(context.Background(), "project+a", head.Name, nil, io.Discard); err == nil || !strings.Contains(err.Error(), "repeated cursor") {
		t.Fatalf("cycle error = %v", err)
	}

	writeErr := errors.New("writer failed")
	writer := errorWriter{err: writeErr}
	_, entries := clientSnapshotFixture()
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/entries") && r.URL.Query().Get("key") != "" {
			for i := range entries {
				if entries[i].Key == r.URL.Query().Get("key") {
					writeClientSnapshotJSON(t, w, snapshot.EntryPage{Exact: &entries[i]})
					return
				}
			}
		}
		if strings.HasSuffix(r.URL.Path, "/entries") {
			writeClientSnapshotJSON(t, w, snapshot.EntryPage{Entries: []snapshot.EntrySummary{entrySummary(entries[0]), entrySummary(entries[1])}})
			return
		}
		writeClientSnapshotJSON(t, w, map[string]any{"snapshot": head, "counts": head.Counts})
	})
	if err := c.ExportContextSnapshot(context.Background(), "project+a", head.Name, nil, writer); !errors.Is(err, writeErr) {
		t.Fatalf("writer error = %v", err)
	}
}

func TestContextSnapshotClientRejectsExactEntryThatDoesNotMatchItsSummary(t *testing.T) {
	head, entries := clientSnapshotFixture()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/entries") && r.URL.Query().Get("key") != "":
			writeClientSnapshotJSON(t, w, snapshot.EntryPage{Exact: &entries[1]})
		case strings.HasSuffix(r.URL.Path, "/entries"):
			writeClientSnapshotJSON(t, w, snapshot.EntryPage{Entries: []snapshot.EntrySummary{entrySummary(entries[0])}})
		default:
			writeClientSnapshotJSON(t, w, map[string]any{"snapshot": head, "counts": head.Counts})
		}
	}))
	t.Cleanup(srv.Close)
	c := New(config.Config{ServerURL: srv.URL})
	filter := &snapshot.ExportFilter{Domain: "knowledge"}
	if err := c.ExportContextSnapshot(context.Background(), "project+a", head.Name, filter, io.Discard); err == nil || !strings.Contains(err.Error(), "does not match summary") {
		t.Fatalf("mismatched exact-entry error = %v", err)
	}
	key := entries[0].Key
	filter = &snapshot.ExportFilter{Domain: entries[0].Domain, Key: &key}
	if err := c.ExportContextSnapshot(context.Background(), "project+a", head.Name, filter, io.Discard); err == nil || !strings.Contains(err.Error(), "does not match requested key") {
		t.Fatalf("mismatched requested-entry error = %v", err)
	}
}

func clientSnapshotFixture() (snapshot.Head, []snapshot.Entry) {
	entries := []snapshot.Entry{
		{Domain: "knowledge", Key: "key+one", Payload: json.RawMessage(`{"a":1}`), PayloadSize: 7},
		{Domain: "request", Key: "REQ-2", Payload: json.RawMessage(`{"b":[2,3]}`), PayloadSize: 11},
	}
	summaries := make([]snapshot.EntrySummary, len(entries))
	counts, _ := snapshot.NewCounts(snapshot.SchemaVersion)
	counts["knowledge"], counts["request"] = 1, 1
	var total int64
	for i := range entries {
		entries[i].PayloadDigest = snapshot.EntryDigest(entries[i].Payload)
		summaries[i] = entrySummary(entries[i])
		total += entries[i].PayloadSize
	}
	head := snapshot.Head{
		ID: 9, Project: "project+a", Name: "v1.2.3+meta", SchemaVersion: snapshot.SchemaVersion, State: "sealed",
		GitObjectFormat: "sha1", GitCommit: strings.Repeat("a", 40), GitMetadataSource: "server-verified",
		ActorID: "person:1", CreatedAt: "2026-08-30T00:00:00Z", EntryCount: int64(len(entries)), PayloadBytesTotal: total, Counts: counts,
	}
	head.ContentDigest = snapshot.ContentDigest(head.SchemaVersion, summaries)
	return head, entries
}

func entrySummary(entry snapshot.Entry) snapshot.EntrySummary {
	return snapshot.EntrySummary{Domain: entry.Domain, Key: entry.Key, PayloadDigest: entry.PayloadDigest, PayloadSize: entry.PayloadSize}
}

func writeClientSnapshotJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

type errorWriter struct{ err error }

func (w errorWriter) Write([]byte) (int, error) { return 0, w.err }
