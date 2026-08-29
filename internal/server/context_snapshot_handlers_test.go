package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type failingSnapshotMirror struct{ calls int }

func (m *failingSnapshotMirror) Rebuild(context.Context, string) error {
	m.calls++
	return errors.New("disk unavailable")
}

func TestContextSnapshotHTTPCreateReadAndActorOverride(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("alice")
	if err := st.SetContextSnapshotAccess("alice", "p", true, true, false); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	in := snapshot.CreateInput{Project: "p", Name: "baseline+one", ActorID: "forged", Git: snapshot.GitProvenance{ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), MetadataSource: "client-reported"}}
	resp := req(t, "POST", srv.URL+"/api/context-snapshots", token, in)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d", resp.StatusCode)
	}
	var created snapshot.CreateResult
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Snapshot.ActorID == "forged" || created.Snapshot.ActorID == "" || created.Snapshot.ActorLabel == nil || *created.Snapshot.ActorLabel != "alice" {
		t.Fatalf("actor not server-owned: %+v", created.Snapshot)
	}
	resp = req(t, "POST", srv.URL+"/api/context-snapshots", token, in)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retry status=%d", resp.StatusCode)
	}
	resp = req(t, "GET", srv.URL+"/api/context-snapshots/baseline+one?project=p", token, nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") == "" {
		t.Fatalf("head status=%d etag=%q", resp.StatusCode, resp.Header.Get("ETag"))
	}
	resp = req(t, "GET", srv.URL+"/api/context-snapshots?project=p&limit=1", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d", resp.StatusCode)
	}
}

func TestContextSnapshotHTTPAccessFiltersAndTypedErrors(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("writer")
	if err := st.SetContextSnapshotAccess("writer", "p", true, true, false); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	in := snapshot.CreateInput{Project: "p", Name: "v1.2.3", Git: snapshot.GitProvenance{ObjectFormat: "sha1", Commit: strings.Repeat("b", 40), MetadataSource: "client-reported"}}
	resp := req(t, "POST", srv.URL+"/api/context-snapshots", token, in)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("release status=%d", resp.StatusCode)
	}
	var rule snapshot.RuleError
	if err := json.NewDecoder(resp.Body).Decode(&rule); err != nil {
		t.Fatal(err)
	}
	if rule.Code != "snapshot_release_binding_forbidden" {
		t.Fatalf("code=%q", rule.Code)
	}
	resp = req(t, "GET", srv.URL+"/api/context-snapshots/n/entries?project=p&key=orphan", token, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("key-only status=%d", resp.StatusCode)
	}
}

func TestContextSnapshotHTTPMirrorFailureIsWarningAfterCommit(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("alice")
	if err := st.SetContextSnapshotAccess("alice", "p", true, true, false); err != nil {
		t.Fatal(err)
	}
	mirror := &failingSnapshotMirror{}
	srv := httptest.NewServer(New(st, WithSnapshotMirror(mirror)))
	t.Cleanup(srv.Close)
	in := snapshot.CreateInput{Project: "p", Name: "baseline", Git: snapshot.GitProvenance{ObjectFormat: "sha1", Commit: strings.Repeat("c", 40), MetadataSource: "client-reported"}}
	resp := req(t, "POST", srv.URL+"/api/context-snapshots", token, in)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var result snapshot.CreateResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if mirror.calls != 1 || len(result.Warnings) != 1 || result.Warnings[0].Code != "snapshot_mirror_degraded" {
		t.Fatalf("mirror=%d warnings=%+v", mirror.calls, result.Warnings)
	}
	if _, _, err := st.ContextSnapshot(context.Background(), "p", "baseline"); err != nil {
		t.Fatalf("snapshot was not committed: %v", err)
	}
}

func TestContextSnapshotHTTPStatusContract(t *testing.T) {
	want := map[string]int{"snapshot_name_conflict": 409, "snapshot_tag_mismatch": 409, "snapshot_dirty_worktree": 409, "snapshot_git_changed": 409, "snapshot_release_binding_forbidden": 403, "snapshot_entry_not_found": 404, "snapshot_limit_exceeded": 422, "unsupported_snapshot_schema": 422, "snapshot_store_busy": 503, "snapshot_storage_exhausted": 507}
	for code, status := range want {
		if snapshotStatusByCode[code] != status {
			t.Fatalf("%s=%d want %d", code, snapshotStatusByCode[code], status)
		}
	}
}
