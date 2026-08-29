package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

type snapshotHeadFingerprintV1 struct {
	Project       string                 `json:"project"`
	Name          string                 `json:"name"`
	SchemaVersion uint32                 `json:"schema_version"`
	Git           snapshot.GitProvenance `json:"git"`
	Message       *string                `json:"message"`
	ActorID       string                 `json:"actor_id"`
	ActorLabel    *string                `json:"actor_label"`
	SessionRef    *string                `json:"session_ref"`
	CreatedAt     string                 `json:"created_at"`
}

func (s *Store) CreateContextSnapshot(ctx context.Context, in snapshot.CreateInput, limits snapshot.Limits, recheck func(context.Context) (snapshot.GitProvenance, error)) (result snapshot.CreateResult, err error) {
	if err := validateSnapshotCreateInput(in, limits); err != nil {
		return result, err
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return result, mapSnapshotSQLiteError(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return result, mapSnapshotSQLiteError(err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	existing, found, err := loadContextSnapshotHead(ctx, conn, in.Project, in.Name)
	if err != nil {
		return result, err
	}
	createdAt := now()
	var snapshotID int64
	if found {
		snapshotID = existing.ID
		createdAt = existing.CreatedAt
	} else {
		res, err := conn.ExecContext(ctx, `INSERT INTO context_snapshots(project,name,schema_version,state,git_object_format,git_commit,git_ref,git_branch,git_dirty,git_worktree_fingerprint_version,git_worktree_fingerprint,allow_dirty_used,git_metadata_source,message,actor_id,actor_label,session_ref,created_at) VALUES(?,?,?,'building',?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, in.Project, in.Name, snapshot.SchemaVersion, in.Git.ObjectFormat, in.Git.Commit, in.Git.Ref, in.Git.Branch, in.Git.Dirty, in.Git.WorktreeFingerprintVersion, digestBytes(in.Git.WorktreeFingerprint), in.Git.AllowDirtyUsed, in.Git.MetadataSource, in.Message, in.ActorID, in.ActorLabel, in.SessionRef, createdAt)
		if err != nil {
			return result, mapSnapshotSQLiteError(err)
		}
		snapshotID, err = res.LastInsertId()
		if err != nil {
			return result, err
		}
	}

	entries, err := captureContextEntries(ctx, conn, in.Project)
	if err != nil {
		return result, err
	}
	if err := validateSnapshotEntries(entries, limits); err != nil {
		return result, err
	}
	summaries := make([]snapshot.EntrySummary, 0, len(entries))
	counts := make(map[string]int64)
	var payloadTotal int64
	for _, entry := range entries {
		summaries = append(summaries, snapshot.EntrySummary{Domain: entry.Domain, Key: entry.Key, PayloadDigest: entry.PayloadDigest, PayloadSize: entry.PayloadSize})
		counts[entry.Domain]++
		payloadTotal += entry.PayloadSize
		if !found {
			if _, err := conn.ExecContext(ctx, `INSERT INTO context_snapshot_entries(snapshot_id,domain,entry_key,payload,payload_digest,payload_size) VALUES(?,?,?,?,?,?)`, snapshotID, entry.Domain, entry.Key, []byte(entry.Payload), entry.PayloadDigest[:], entry.PayloadSize); err != nil {
				return result, mapSnapshotSQLiteError(err)
			}
		}
	}
	digest := snapshot.ContentDigest(snapshot.SchemaVersion, summaries)
	gitAfter, err := recheck(ctx)
	if err != nil {
		return result, err
	}
	if !sameGitProvenance(in.Git, gitAfter) {
		return result, &snapshot.RuleError{Code: "snapshot_git_changed", Retryable: true}
	}
	if found {
		if existing.SchemaVersion != snapshot.SchemaVersion || existing.ContentDigest != digest || !headMatchesGit(existing, in.Git) {
			return result, &snapshot.RuleError{Code: "snapshot_name_conflict", ExistingDigest: existing.ContentDigest.String(), RequestedDigest: digest.String()}
		}
		if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
			return result, mapSnapshotSQLiteError(err)
		}
		committed = true
		result.Snapshot = existing
		result.Created = false
		return result, nil
	}

	headBytes, err := snapshot.MarshalCanonical(snapshotHeadFingerprintV1{Project: in.Project, Name: in.Name, SchemaVersion: snapshot.SchemaVersion, Git: in.Git, Message: in.Message, ActorID: in.ActorID, ActorLabel: in.ActorLabel, SessionRef: in.SessionRef, CreatedAt: createdAt})
	if err != nil {
		return result, err
	}
	logical := snapshot.LogicalSize(headBytes, summaries)
	if limitExceeded(int64(len(headBytes)), limits.MaxCanonicalHeadBytes) || limitExceeded(logical, limits.MaxSnapshotLogicalBytes) {
		return result, &snapshot.RuleError{Code: "snapshot_limit_exceeded"}
	}
	if err := checkSnapshotAggregateQuotas(ctx, conn, in.Project, logical, limits); err != nil {
		return result, err
	}
	storedSummaries, storedCounts, storedTotal, err := readStoredSnapshotEntries(ctx, conn, snapshotID)
	if err != nil {
		return result, err
	}
	storedDigest := snapshot.ContentDigest(snapshot.SchemaVersion, storedSummaries)
	if storedDigest != digest || storedTotal != payloadTotal || !equalCounts(storedCounts, counts) {
		return result, &snapshot.RuleError{Code: "snapshot_integrity_error"}
	}
	countsJSON, err := snapshot.MarshalCanonical(storedCounts)
	if err != nil {
		return result, err
	}
	res, err := conn.ExecContext(ctx, `UPDATE context_snapshots SET state='sealed',content_digest=?,entry_count=?,payload_bytes_total=?,counts_json=? WHERE id=? AND state='building'`, storedDigest[:], len(storedSummaries), storedTotal, countsJSON, snapshotID)
	if err != nil {
		return result, mapSnapshotSQLiteError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return result, err
	}
	if n != 1 {
		return result, &snapshot.RuleError{Code: "snapshot_integrity_error"}
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return result, mapSnapshotSQLiteError(err)
	}
	committed = true
	result.Snapshot = snapshot.Head{ID: snapshotID, Project: in.Project, Name: in.Name, SchemaVersion: snapshot.SchemaVersion, State: "sealed", ContentDigest: storedDigest, GitObjectFormat: in.Git.ObjectFormat, GitCommit: in.Git.Commit, GitRef: in.Git.Ref, GitBranch: in.Git.Branch, GitDirty: in.Git.Dirty, GitWorktreeFingerprintVersion: in.Git.WorktreeFingerprintVersion, GitWorktreeFingerprint: in.Git.WorktreeFingerprint, AllowDirtyUsed: in.Git.AllowDirtyUsed, GitMetadataSource: in.Git.MetadataSource, Message: in.Message, ActorID: in.ActorID, ActorLabel: in.ActorLabel, SessionRef: in.SessionRef, CreatedAt: createdAt, EntryCount: int64(len(storedSummaries)), PayloadBytesTotal: storedTotal, Counts: storedCounts}
	result.Created = true
	return result, nil
}

func validateSnapshotCreateInput(in snapshot.CreateInput, l snapshot.Limits) error {
	if in.Project == "" || in.Name == "" || in.ActorID == "" || !utf8.ValidString(in.Project+in.Name+in.ActorID) {
		return &snapshot.RuleError{Code: "snapshot_invalid_input"}
	}
	fields := []struct {
		v   *string
		max int64
	}{{in.Message, l.MaxMessageBytes}, {&in.ActorID, l.MaxActorIDBytes}, {in.ActorLabel, l.MaxActorLabelBytes}, {in.SessionRef, l.MaxSessionRefBytes}, {in.Git.Ref, l.MaxGitRefBytes}, {in.Git.Branch, l.MaxGitBranchBytes}}
	for _, f := range fields {
		if f.v != nil && (!utf8.ValidString(*f.v) || limitExceeded(int64(len(*f.v)), f.max)) {
			return &snapshot.RuleError{Code: "snapshot_limit_exceeded"}
		}
	}
	return nil
}
func validateSnapshotEntries(entries []snapshot.Entry, l snapshot.Limits) error {
	if limitExceeded(int64(len(entries)), l.MaxEntriesPerSnapshot) {
		return &snapshot.RuleError{Code: "snapshot_limit_exceeded"}
	}
	var total int64
	for _, e := range entries {
		if limitExceeded(e.PayloadSize, l.MaxEntryPayloadBytes) {
			return &snapshot.RuleError{Code: "snapshot_limit_exceeded"}
		}
		total += e.PayloadSize
	}
	if limitExceeded(total, l.MaxSnapshotPayloadBytes) {
		return &snapshot.RuleError{Code: "snapshot_limit_exceeded"}
	}
	return nil
}
func limitExceeded(value, limit int64) bool { return limit >= 0 && value > limit }
func digestBytes(d *snapshot.Digest) any {
	if d == nil {
		return nil
	}
	return d[:]
}
func sameGitProvenance(a, b snapshot.GitProvenance) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
func headMatchesGit(h snapshot.Head, g snapshot.GitProvenance) bool {
	return h.GitObjectFormat == g.ObjectFormat && h.GitCommit == g.Commit && sameStringPtr(h.GitRef, g.Ref) && sameStringPtr(h.GitBranch, g.Branch) && h.GitDirty == g.Dirty && sameUint32Ptr(h.GitWorktreeFingerprintVersion, g.WorktreeFingerprintVersion) && sameDigestPtr(h.GitWorktreeFingerprint, g.WorktreeFingerprint) && h.AllowDirtyUsed == g.AllowDirtyUsed && h.GitMetadataSource == g.MetadataSource
}
func sameStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
func sameUint32Ptr(a, b *uint32) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
func sameDigestPtr(a, b *snapshot.Digest) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func loadContextSnapshotHead(ctx context.Context, q snapshotQueryer, project, name string) (snapshot.Head, bool, error) {
	var h snapshot.Head
	var content, worktree []byte
	var ref, branch, message, label, session sql.NullString
	var fpv sql.NullInt64
	var dirty, allow int
	err := q.QueryRowContext(ctx, `SELECT id,project,name,schema_version,state,content_digest,git_object_format,git_commit,git_ref,git_branch,git_dirty,git_worktree_fingerprint_version,git_worktree_fingerprint,allow_dirty_used,git_metadata_source,message,actor_id,actor_label,session_ref,created_at,entry_count,payload_bytes_total,counts_json FROM context_snapshots WHERE project=? AND name=?`, project, name).Scan(&h.ID, &h.Project, &h.Name, &h.SchemaVersion, &h.State, &content, &h.GitObjectFormat, &h.GitCommit, &ref, &branch, &dirty, &fpv, &worktree, &allow, &h.GitMetadataSource, &message, &h.ActorID, &label, &session, &h.CreatedAt, &h.EntryCount, &h.PayloadBytesTotal, new([]byte))
	if errors.Is(err, sql.ErrNoRows) {
		return h, false, nil
	}
	if err != nil {
		return h, false, err
	}
	copy(h.ContentDigest[:], content)
	h.GitRef = nullStringPtr(ref)
	h.GitBranch = nullStringPtr(branch)
	h.Message = nullStringPtr(message)
	h.ActorLabel = nullStringPtr(label)
	h.SessionRef = nullStringPtr(session)
	h.GitDirty = dirty != 0
	h.AllowDirtyUsed = allow != 0
	if fpv.Valid {
		v := uint32(fpv.Int64)
		h.GitWorktreeFingerprintVersion = &v
	}
	if len(worktree) == 32 {
		var d snapshot.Digest
		copy(d[:], worktree)
		h.GitWorktreeFingerprint = &d
	}
	return h, true, nil
}
func nullStringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}
func readStoredSnapshotEntries(ctx context.Context, q snapshotQueryer, id int64) ([]snapshot.EntrySummary, map[string]int64, int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT domain,entry_key,payload,payload_digest,payload_size FROM context_snapshot_entries WHERE snapshot_id=? ORDER BY domain,entry_key`, id)
	if err != nil {
		return nil, nil, 0, err
	}
	defer rows.Close()
	var out []snapshot.EntrySummary
	counts := map[string]int64{}
	var total int64
	for rows.Next() {
		var e snapshot.EntrySummary
		var raw, digest []byte
		if err := rows.Scan(&e.Domain, &e.Key, &raw, &digest, &e.PayloadSize); err != nil {
			return nil, nil, 0, err
		}
		if int64(len(raw)) != e.PayloadSize || snapshot.EntryDigest(raw) != bytesDigest(digest) {
			return nil, nil, 0, &snapshot.RuleError{Code: "snapshot_integrity_error"}
		}
		copy(e.PayloadDigest[:], digest)
		out = append(out, e)
		counts[e.Domain]++
		total += e.PayloadSize
	}
	return out, counts, total, rows.Err()
}
func bytesDigest(raw []byte) snapshot.Digest { var d snapshot.Digest; copy(d[:], raw); return d }
func equalCounts(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
func checkSnapshotAggregateQuotas(ctx context.Context, q snapshotQueryer, project string, growth int64, l snapshot.Limits) error {
	var projectCount, storeCount int64
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM context_snapshots WHERE project=? AND state='sealed'`, project).Scan(&projectCount); err != nil {
		return err
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM context_snapshots WHERE state='sealed'`).Scan(&storeCount); err != nil {
		return err
	}
	if limitExceeded(projectCount+1, l.MaxSnapshotsPerProject) || limitExceeded(storeCount+1, l.MaxSnapshotsPerStore) {
		return &snapshot.RuleError{Code: "snapshot_limit_exceeded"}
	}
	projectBytes, storeBytes, err := snapshotLogicalTotals(ctx, q, project)
	if err != nil {
		return err
	}
	if limitExceeded(projectBytes+growth, l.MaxProjectLogicalBytes) || limitExceeded(storeBytes+growth, l.MaxStoreLogicalBytes) {
		return &snapshot.RuleError{Code: "snapshot_limit_exceeded"}
	}
	return nil
}

func snapshotLogicalTotals(ctx context.Context, q snapshotQueryer, project string) (int64, int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT project,name FROM context_snapshots WHERE state='sealed' ORDER BY id`)
	if err != nil {
		return 0, 0, err
	}
	type identity struct{ project, name string }
	var identities []identity
	for rows.Next() {
		var v identity
		if err := rows.Scan(&v.project, &v.name); err != nil {
			rows.Close()
			return 0, 0, err
		}
		identities = append(identities, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}
	var projectTotal, storeTotal int64
	for _, v := range identities {
		h, found, err := loadContextSnapshotHead(ctx, q, v.project, v.name)
		if err != nil {
			return 0, 0, err
		}
		if !found {
			return 0, 0, &snapshot.RuleError{Code: "snapshot_integrity_error"}
		}
		entries, _, _, err := readStoredSnapshotEntries(ctx, q, h.ID)
		if err != nil {
			return 0, 0, err
		}
		git := snapshot.GitProvenance{ObjectFormat: h.GitObjectFormat, Commit: h.GitCommit, Ref: h.GitRef, Branch: h.GitBranch, Dirty: h.GitDirty, WorktreeFingerprintVersion: h.GitWorktreeFingerprintVersion, WorktreeFingerprint: h.GitWorktreeFingerprint, AllowDirtyUsed: h.AllowDirtyUsed, MetadataSource: h.GitMetadataSource}
		headBytes, err := snapshot.MarshalCanonical(snapshotHeadFingerprintV1{Project: h.Project, Name: h.Name, SchemaVersion: h.SchemaVersion, Git: git, Message: h.Message, ActorID: h.ActorID, ActorLabel: h.ActorLabel, SessionRef: h.SessionRef, CreatedAt: h.CreatedAt})
		if err != nil {
			return 0, 0, err
		}
		logical := snapshot.LogicalSize(headBytes, entries)
		storeTotal += logical
		if h.Project == project {
			projectTotal += logical
		}
	}
	return projectTotal, storeTotal, nil
}
func mapSnapshotSQLiteError(err error) error {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "database is locked") || strings.Contains(lower, "sqlite_busy") {
		return &snapshot.RuleError{Code: "snapshot_store_busy", Retryable: true}
	}
	if strings.Contains(lower, "database or disk is full") || strings.Contains(lower, "sqlite_full") {
		return &snapshot.RuleError{Code: "snapshot_storage_exhausted"}
	}
	return fmt.Errorf("context snapshot storage: %w", err)
}
