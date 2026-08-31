package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

func (a *api) createContextSnapshot(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, contextSnapshotCreateBodyLimit(a.snapshotLimits))
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeSnapshotRuleError(w, http.StatusRequestEntityTooLarge, &snapshot.RuleError{Code: "snapshot_request_too_large"})
			return
		}
		writeSnapshotRuleError(w, http.StatusBadRequest, &snapshot.RuleError{Code: "snapshot_invalid_input"})
		return
	}
	var input snapshot.CreateInput
	if err := json.Unmarshal(body, &input); err != nil {
		writeSnapshotRuleError(w, http.StatusBadRequest, &snapshot.RuleError{Code: "snapshot_invalid_input"})
		return
	}
	if !validSnapshotProject(input.Project) {
		writeSnapshotRuleError(w, http.StatusBadRequest, &snapshot.RuleError{Code: "snapshot_invalid_input"})
		return
	}
	nameClass := collector.ClassifySnapshotName(input.Name)
	if nameClass == collector.SnapshotNameInvalidReleaseLike {
		writeSnapshotRuleError(w, http.StatusBadRequest, &snapshot.RuleError{Code: "snapshot_invalid_input"})
		return
	}
	principal := principalOf(r)
	access, err := a.st.ContextSnapshotAccess(principal.ID, input.Project)
	if err != nil {
		a.writeSnapshotError(w, err)
		return
	}
	if !access.Read || !access.Create {
		writeSnapshotRuleError(w, http.StatusForbidden, &snapshot.RuleError{Code: "snapshot_access_forbidden"})
		return
	}
	// Crossing the HTTP trust boundary makes provenance client-reported even
	// when the caller resolved it with Ghosttree's own Git implementation.
	input.Git.MetadataSource = "client-reported"
	if input.GitRecheck == nil {
		writeSnapshotRuleError(w, http.StatusBadRequest, &snapshot.RuleError{Code: "snapshot_invalid_input"})
		return
	}
	input.GitRecheck.MetadataSource = "client-reported"
	if nameClass == collector.SnapshotNameRelease && !access.ReleaseBind {
		writeSnapshotRuleError(w, http.StatusForbidden, &snapshot.RuleError{Code: "snapshot_release_binding_forbidden"})
		return
	}
	input.ActorID = principal.ID
	input.ActorLabel = &principal.Label
	result, err := a.st.CreateContextSnapshot(r.Context(), input, a.snapshotLimits, func(context.Context) (snapshot.GitProvenance, error) { return *input.GitRecheck, nil })
	if err != nil {
		a.writeSnapshotError(w, err)
		return
	}
	if a.snapshotMirror != nil {
		if err := a.snapshotMirror.Rebuild(r.Context(), input.Project); err != nil {
			result.Warnings = append(result.Warnings, snapshot.Warning{Code: "snapshot_mirror_degraded", Message: err.Error()})
		}
	}
	response := struct {
		Snapshot snapshot.Head      `json:"snapshot"`
		Counts   map[string]int64   `json:"counts"`
		Created  bool               `json:"created"`
		Warnings []snapshot.Warning `json:"warnings"`
	}{result.Snapshot, result.Snapshot.Counts, result.Created, result.Warnings}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, response)
}

func contextSnapshotCreateBodyLimit(limits snapshot.Limits) int64 {
	headLimit := limits.MaxCanonicalHeadBytes
	if headLimit <= 0 {
		headLimit = snapshot.DefaultLimits().MaxCanonicalHeadBytes
	}
	const maxJSONEscapeExpansionTimesGitCopies = 12
	const maxInt64 = 1<<63 - 1
	if headLimit > maxInt64/maxJSONEscapeExpansionTimesGitCopies {
		return maxInt64
	}
	return maxJSONEscapeExpansionTimesGitCopies * headLimit
}

func (a *api) listContextSnapshots(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if !a.requireSnapshotRead(w, r, project) {
		return
	}
	page, err := a.st.ListContextSnapshots(r.Context(), snapshot.ListFilter{Project: project, Cursor: r.URL.Query().Get("cursor"), Limit: intParam(r, "limit", 100)})
	if err != nil {
		a.writeSnapshotError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (a *api) getContextSnapshot(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if !a.requireSnapshotRead(w, r, project) {
		return
	}
	head, counts, err := a.st.ContextSnapshot(r.Context(), project, r.PathValue("name"))
	if err != nil {
		a.writeSnapshotError(w, err)
		return
	}
	response := struct {
		Snapshot snapshot.Head    `json:"snapshot"`
		Counts   map[string]int64 `json:"counts"`
	}{head, counts}
	canonical, err := snapshot.MarshalCanonical(response)
	if err != nil {
		a.writeSnapshotError(w, err)
		return
	}
	etag := sha256.Sum256(canonical)
	w.Header().Set("ETag", `"`+fmt.Sprintf("%x", etag[:])+`"`)
	writeJSON(w, http.StatusOK, response)
}

func (a *api) contextSnapshotEntries(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if !a.requireSnapshotRead(w, r, project) {
		return
	}
	filter := snapshot.EntryFilter{Domain: r.URL.Query().Get("domain"), Key: r.URL.Query().Get("key"), Cursor: r.URL.Query().Get("cursor"), Limit: intParam(r, "limit", 100)}
	if filter.Key != "" && filter.Domain == "" {
		writeSnapshotRuleError(w, http.StatusBadRequest, &snapshot.RuleError{Code: "snapshot_invalid_filter"})
		return
	}
	page, err := a.st.ContextSnapshotEntries(r.Context(), project, r.PathValue("name"), filter)
	if err != nil {
		a.writeSnapshotError(w, err)
		return
	}
	if page.Exact != nil {
		w.Header().Set("ETag", `"`+page.Exact.PayloadDigest.String()+`"`)
		raw, err := snapshot.MarshalCanonical(page)
		if err != nil {
			a.writeSnapshotError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(append(raw, '\n'))
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (a *api) requireSnapshotRead(w http.ResponseWriter, r *http.Request, project string) bool {
	if !validSnapshotProject(project) {
		writeSnapshotRuleError(w, http.StatusBadRequest, &snapshot.RuleError{Code: "snapshot_invalid_input"})
		return false
	}
	access, err := a.st.ContextSnapshotAccess(principalOf(r).ID, project)
	if err != nil {
		a.writeSnapshotError(w, err)
		return false
	}
	if !access.Read {
		writeSnapshotRuleError(w, http.StatusForbidden, &snapshot.RuleError{Code: "snapshot_access_forbidden"})
		return false
	}
	return true
}

func validSnapshotProject(project string) bool {
	return project != "" && project == scope.NormalizeRemote(project)
}

var snapshotStatusByCode = map[string]int{
	"snapshot_invalid_input": 400, "snapshot_invalid_filter": 400, "snapshot_invalid_cursor": 400,
	"snapshot_request_too_large": 413,
	"snapshot_access_forbidden":  403, "snapshot_release_binding_forbidden": 403,
	"snapshot_not_found": 404, "snapshot_entry_not_found": 404,
	"snapshot_name_conflict": 409, "snapshot_tag_mismatch": 409, "snapshot_dirty_worktree": 409, "snapshot_git_changed": 409,
	"snapshot_limit_exceeded": 422, "unsupported_snapshot_schema": 422,
	"snapshot_store_busy": 503, "snapshot_storage_exhausted": 507,
	"snapshot_integrity_error": 500,
}

func (a *api) writeSnapshotError(w http.ResponseWriter, err error) {
	var rule *snapshot.RuleError
	if errors.As(err, &rule) {
		status := snapshotStatusByCode[rule.Code]
		if status != 0 && status != http.StatusInternalServerError {
			writeSnapshotRuleError(w, status, rule)
			return
		}
	}
	operationID, generatorErr := a.operationIDGenerator()
	if generatorErr != nil || operationID == "" {
		if generatorErr == nil {
			generatorErr = errors.New("operation ID generator returned an empty ID")
		}
		operationID = fallbackOperationID()
	}
	a.snapshotErrorLogger(operationID, err, generatorErr)
	writeSnapshotRuleError(w, http.StatusInternalServerError, &snapshot.RuleError{
		Code: "snapshot_internal_error", Message: "internal snapshot operation failed",
		Details: map[string]any{"operation_id": operationID},
	})
}

func writeSnapshotRuleError(w http.ResponseWriter, status int, rule *snapshot.RuleError) {
	if rule.Retryable {
		w.Header().Set("Retry-After", strconv.Itoa(1))
	}
	writeJSON(w, status, rule)
}
