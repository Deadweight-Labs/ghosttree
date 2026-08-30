package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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

func TestContextSnapshotHTTPCreateRejectsOversizedBodyBeforeAccessOrStore(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	deniedToken, _ := st.AddPerson("denied")
	authorizedToken, _ := st.AddPerson("authorized")
	if err := st.SetContextSnapshotAccess("authorized", "p", true, true, false); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)

	bodyLimit := contextSnapshotCreateBodyLimit(snapshot.DefaultLimits())
	if bodyLimit <= 0 {
		t.Fatalf("body limit=%d", bodyLimit)
	}
	body := []byte(`{"project":"p","name":"oversized","git":{"git_object_format":"sha1","git_commit":"` + strings.Repeat("a", 40) + `","git_dirty":false,"allow_dirty_used":false,"git_metadata_source":"client-reported"},"git_recheck":{"git_object_format":"sha1","git_commit":"` + strings.Repeat("a", 40) + `","git_dirty":false,"allow_dirty_used":false,"git_metadata_source":"client-reported"}}` + strings.Repeat(" ", int(bodyLimit)))
	for _, token := range []string{deniedToken, authorizedToken} {
		request, err := http.NewRequest(http.MethodPost, srv.URL+"/api/context-snapshots", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.ContentLength = -1
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var rule snapshot.RuleError
		if err := json.NewDecoder(response.Body).Decode(&rule); err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusRequestEntityTooLarge || rule.Code != "snapshot_request_too_large" {
			t.Fatalf("status=%d code=%q", response.StatusCode, rule.Code)
		}
	}
	if _, _, err := st.ContextSnapshot(context.Background(), "p", "oversized"); err == nil {
		t.Fatal("oversized request created a snapshot")
	}

	closedStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := closedStore.Close(); err != nil {
		t.Fatal(err)
	}
	directRequest := httptest.NewRequest(http.MethodPost, "/api/context-snapshots", bytes.NewReader(body))
	directRequest = directRequest.WithContext(context.WithValue(directRequest.Context(), personKey{}, store.Principal{ID: "person:1", Label: "closed"}))
	recorder := httptest.NewRecorder()
	newAPI(closedStore).createContextSnapshot(recorder, directRequest)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("closed-store status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestContextSnapshotHTTPCreateAllowsMaximumMetadata(t *testing.T) {
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

	limits := snapshot.DefaultLimits()
	message := strings.Repeat("m", int(limits.MaxMessageBytes))
	actorID := strings.Repeat("i", int(limits.MaxActorIDBytes))
	actorLabel := strings.Repeat("l", int(limits.MaxActorLabelBytes))
	sessionRef := strings.Repeat("s", int(limits.MaxSessionRefBytes))
	gitRef := strings.Repeat("r", int(limits.MaxGitRefBytes))
	gitBranch := strings.Repeat("b", int(limits.MaxGitBranchBytes))
	git := snapshot.GitProvenance{ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Ref: &gitRef, Branch: &gitBranch, MetadataSource: "client-reported"}
	input := snapshot.CreateInput{Project: "p", Name: "maximum-metadata", Git: git, GitRecheck: &git, Message: &message, ActorID: actorID, ActorLabel: &actorLabel, SessionRef: &sessionRef}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, srv.URL+"/api/context-snapshots", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		responseBody, _ := io.ReadAll(response.Body)
		t.Fatalf("status=%d body=%s request_bytes=%d", response.StatusCode, responseBody, len(body))
	}

	escaped := "\x01"
	escapedMessage := strings.Repeat(escaped, 2_300)
	escapedActorID := strings.Repeat(escaped, int(limits.MaxActorIDBytes))
	escapedActorLabel := strings.Repeat(escaped, int(limits.MaxActorLabelBytes))
	escapedSessionRef := strings.Repeat(escaped, int(limits.MaxSessionRefBytes))
	escapedGitRef := strings.Repeat(escaped, int(limits.MaxGitRefBytes))
	escapedGitBranch := strings.Repeat(escaped, int(limits.MaxGitBranchBytes))
	escapedGit := snapshot.GitProvenance{ObjectFormat: "sha1", Commit: strings.Repeat("b", 40), Ref: &escapedGitRef, Branch: &escapedGitBranch, MetadataSource: "client-reported"}
	escapedInput := snapshot.CreateInput{Project: "p", Name: "escaped-metadata", Git: escapedGit, GitRecheck: &escapedGit, Message: &escapedMessage, ActorID: escapedActorID, ActorLabel: &escapedActorLabel, SessionRef: &escapedSessionRef}
	escapedBody, err := json.Marshal(escapedInput)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(escapedBody)) >= contextSnapshotCreateBodyLimit(limits) {
		t.Fatalf("legal escaped request bytes=%d limit=%d", len(escapedBody), contextSnapshotCreateBodyLimit(limits))
	}
	escapedRequest, err := http.NewRequest(http.MethodPost, srv.URL+"/api/context-snapshots", bytes.NewReader(escapedBody))
	if err != nil {
		t.Fatal(err)
	}
	escapedRequest.Header.Set("Authorization", "Bearer "+token)
	escapedResponse, err := http.DefaultClient.Do(escapedRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer escapedResponse.Body.Close()
	if escapedResponse.StatusCode != http.StatusCreated {
		responseBody, _ := io.ReadAll(escapedResponse.Body)
		t.Fatalf("escaped status=%d body=%s request_bytes=%d", escapedResponse.StatusCode, responseBody, len(escapedBody))
	}

	raisedLimits := snapshot.DefaultLimits()
	raisedLimits.MaxCanonicalHeadBytes = 512 << 10
	raisedLimits.MaxMessageBytes = 400 << 10
	raisedServer := httptest.NewServer(New(st, WithContextSnapshotLimits(raisedLimits)))
	t.Cleanup(raisedServer.Close)
	raisedMessage := strings.Repeat("z", int(raisedLimits.MaxMessageBytes))
	raisedGit := snapshot.GitProvenance{ObjectFormat: "sha1", Commit: strings.Repeat("c", 40), MetadataSource: "client-reported"}
	raisedInput := snapshot.CreateInput{Project: "p", Name: "raised-metadata-limit", Git: raisedGit, GitRecheck: &raisedGit, Message: &raisedMessage}
	raisedBody, err := json.Marshal(raisedInput)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(raisedBody)) <= contextSnapshotCreateBodyLimit(limits) {
		t.Fatalf("raised request did not exceed default envelope: bytes=%d limit=%d", len(raisedBody), contextSnapshotCreateBodyLimit(limits))
	}
	raisedRequest, err := http.NewRequest(http.MethodPost, raisedServer.URL+"/api/context-snapshots", bytes.NewReader(raisedBody))
	if err != nil {
		t.Fatal(err)
	}
	raisedRequest.Header.Set("Authorization", "Bearer "+token)
	raisedResponse, err := http.DefaultClient.Do(raisedRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer raisedResponse.Body.Close()
	if raisedResponse.StatusCode != http.StatusCreated {
		responseBody, _ := io.ReadAll(raisedResponse.Body)
		t.Fatalf("raised status=%d body=%s request_bytes=%d", raisedResponse.StatusCode, responseBody, len(raisedBody))
	}
}

func TestContextSnapshotHTTPCreateAllowsHTMLEscapedMetadataWithinHeadLimit(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("html-client")
	project := strings.Repeat("<", 12_000)
	if err := st.SetContextSnapshotAccess("html-client", project, true, true, false); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)

	git := snapshot.GitProvenance{ObjectFormat: "sha1", Commit: strings.Repeat("d", 40), MetadataSource: "client-reported"}
	input := snapshot.CreateInput{Project: project, Name: "html-escaped-metadata", Git: git, GitRecheck: &git}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(body)) <= 2*snapshot.DefaultLimits().MaxCanonicalHeadBytes {
		t.Fatalf("HTML-escaped body=%d does not exercise the old envelope", len(body))
	}
	request, err := http.NewRequest(http.MethodPost, srv.URL+"/api/context-snapshots", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		responseBody, _ := io.ReadAll(response.Body)
		t.Fatalf("status=%d body=%s request_bytes=%d", response.StatusCode, responseBody, len(body))
	}
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
	in.GitRecheck = &in.Git
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
	in.GitRecheck = &in.Git
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
	in.GitRecheck = &in.Git
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
