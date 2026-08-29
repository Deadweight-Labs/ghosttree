package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

const snapshotHeadColumns = `id,project,name,schema_version,state,content_digest,git_object_format,git_commit,git_ref,git_branch,git_dirty,git_worktree_fingerprint_version,git_worktree_fingerprint,allow_dirty_used,git_metadata_source,message,actor_id,actor_label,session_ref,created_at,entry_count,payload_bytes_total,counts_json`

func (s *Store) ListContextSnapshots(ctx context.Context, filter snapshot.ListFilter) (snapshot.SnapshotPage, error) {
	limit := boundedSnapshotLimit(filter.Limit)
	var before int64
	var err error
	if filter.Cursor != "" {
		before, err = snapshot.DecodeSnapshotCursor(filter.Cursor)
		if err != nil {
			return snapshot.SnapshotPage{}, &snapshot.RuleError{Code: "snapshot_invalid_cursor"}
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+snapshotHeadColumns+` FROM context_snapshots WHERE project=? AND state='sealed' AND (?=0 OR id<?) ORDER BY id DESC LIMIT ?`, filter.Project, before, before, limit+1)
	if err != nil {
		return snapshot.SnapshotPage{}, err
	}
	defer rows.Close()
	page := snapshot.SnapshotPage{Snapshots: make([]snapshot.Head, 0, limit)}
	for rows.Next() {
		head, _, err := scanSnapshotHead(rows)
		if err != nil {
			return snapshot.SnapshotPage{}, err
		}
		page.Snapshots = append(page.Snapshots, head)
	}
	if err := rows.Err(); err != nil {
		return snapshot.SnapshotPage{}, err
	}
	if len(page.Snapshots) > limit {
		page.Snapshots = page.Snapshots[:limit]
		page.NextCursor, err = snapshot.EncodeSnapshotCursor(page.Snapshots[len(page.Snapshots)-1].ID)
		if err != nil {
			return snapshot.SnapshotPage{}, err
		}
	}
	return page, nil
}

func (s *Store) ContextSnapshot(ctx context.Context, project, name string) (snapshot.Head, map[string]int64, error) {
	head, counts, err := scanSnapshotHead(s.db.QueryRowContext(ctx, `SELECT `+snapshotHeadColumns+` FROM context_snapshots WHERE project=? AND name=? AND state='sealed'`, project, name))
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot.Head{}, nil, &snapshot.RuleError{Code: "snapshot_not_found"}
	}
	return head, counts, err
}

func (s *Store) ContextSnapshotEntries(ctx context.Context, project, name string, filter snapshot.EntryFilter) (snapshot.EntryPage, error) {
	if filter.Key != "" && filter.Domain == "" {
		return snapshot.EntryPage{}, &snapshot.RuleError{Code: "snapshot_invalid_filter"}
	}
	var snapshotID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM context_snapshots WHERE project=? AND name=? AND state='sealed'`, project, name).Scan(&snapshotID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return snapshot.EntryPage{}, &snapshot.RuleError{Code: "snapshot_not_found"}
		}
		return snapshot.EntryPage{}, err
	}
	if filter.Key != "" {
		entry, err := scanSnapshotEntry(s.db.QueryRowContext(ctx, `SELECT domain,entry_key,payload,payload_digest,payload_size FROM context_snapshot_entries WHERE snapshot_id=? AND domain=? AND entry_key=?`, snapshotID, filter.Domain, filter.Key))
		if errors.Is(err, sql.ErrNoRows) {
			return snapshot.EntryPage{}, &snapshot.RuleError{Code: "snapshot_entry_not_found"}
		}
		if err != nil {
			return snapshot.EntryPage{}, err
		}
		return snapshot.EntryPage{Exact: &entry}, nil
	}

	limit := boundedSnapshotLimit(filter.Limit)
	startDomain, startKey := "", ""
	if filter.Cursor != "" {
		var err error
		startDomain, startKey, err = snapshot.DecodeEntryCursor(filter.Cursor)
		if err != nil {
			return snapshot.EntryPage{}, &snapshot.RuleError{Code: "snapshot_invalid_cursor"}
		}
		if filter.Domain != "" && startDomain != filter.Domain {
			return snapshot.EntryPage{}, &snapshot.RuleError{Code: "snapshot_invalid_cursor"}
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT domain,entry_key,payload_digest,payload_size FROM context_snapshot_entries WHERE snapshot_id=? AND (?='' OR domain=?) AND (?='' OR domain>? OR (domain=? AND entry_key>?)) ORDER BY domain,entry_key LIMIT ?`, snapshotID, filter.Domain, filter.Domain, startDomain, startDomain, startDomain, startKey, limit+1)
	if err != nil {
		return snapshot.EntryPage{}, err
	}
	defer rows.Close()
	page := snapshot.EntryPage{Entries: make([]snapshot.EntrySummary, 0, limit)}
	for rows.Next() {
		var item snapshot.EntrySummary
		var digest []byte
		if err := rows.Scan(&item.Domain, &item.Key, &digest, &item.PayloadSize); err != nil {
			return snapshot.EntryPage{}, err
		}
		if len(digest) != len(item.PayloadDigest) {
			return snapshot.EntryPage{}, integrityStoreError("entry digest length")
		}
		copy(item.PayloadDigest[:], digest)
		page.Entries = append(page.Entries, item)
	}
	if err := rows.Err(); err != nil {
		return snapshot.EntryPage{}, err
	}
	if len(page.Entries) > limit {
		page.Entries = page.Entries[:limit]
		last := page.Entries[len(page.Entries)-1]
		page.NextCursor, err = snapshot.EncodeEntryCursor(last.Domain, last.Key)
		if err != nil {
			return snapshot.EntryPage{}, err
		}
	}
	return page, nil
}

type snapshotScanner interface{ Scan(...any) error }

func scanSnapshotHead(row snapshotScanner) (snapshot.Head, map[string]int64, error) {
	var head snapshot.Head
	var schemaVersion int64
	var digest, fingerprint, countsRaw []byte
	var gitRef, gitBranch, message, actorLabel, sessionRef sql.NullString
	var dirty, allowDirty int64
	var fingerprintVersion sql.NullInt64
	err := row.Scan(&head.ID, &head.Project, &head.Name, &schemaVersion, &head.State, &digest, &head.GitObjectFormat, &head.GitCommit, &gitRef, &gitBranch, &dirty, &fingerprintVersion, &fingerprint, &allowDirty, &head.GitMetadataSource, &message, &head.ActorID, &actorLabel, &sessionRef, &head.CreatedAt, &head.EntryCount, &head.PayloadBytesTotal, &countsRaw)
	if err != nil {
		return snapshot.Head{}, nil, err
	}
	if schemaVersion <= 0 || len(digest) != len(head.ContentDigest) {
		return snapshot.Head{}, nil, integrityStoreError("invalid snapshot head")
	}
	head.SchemaVersion, head.GitDirty, head.AllowDirtyUsed = uint32(schemaVersion), dirty != 0, allowDirty != 0
	copy(head.ContentDigest[:], digest)
	head.GitRef, head.GitBranch, head.Message, head.ActorLabel, head.SessionRef = nullStringPointer(gitRef), nullStringPointer(gitBranch), nullStringPointer(message), nullStringPointer(actorLabel), nullStringPointer(sessionRef)
	if fingerprintVersion.Valid {
		version := uint32(fingerprintVersion.Int64)
		head.GitWorktreeFingerprintVersion = &version
		if len(fingerprint) != len(snapshot.Digest{}) {
			return snapshot.Head{}, nil, integrityStoreError("invalid worktree fingerprint")
		}
		var value snapshot.Digest
		copy(value[:], fingerprint)
		head.GitWorktreeFingerprint = &value
	}
	counts := make(map[string]int64)
	if !json.Valid(countsRaw) || json.Unmarshal(countsRaw, &counts) != nil {
		return snapshot.Head{}, nil, integrityStoreError("invalid counts")
	}
	head.Counts = counts
	return head, counts, nil
}

func scanSnapshotEntry(row snapshotScanner) (snapshot.Entry, error) {
	var entry snapshot.Entry
	var digest []byte
	if err := row.Scan(&entry.Domain, &entry.Key, &entry.Payload, &digest, &entry.PayloadSize); err != nil {
		return snapshot.Entry{}, err
	}
	if len(digest) != len(entry.PayloadDigest) {
		return snapshot.Entry{}, integrityStoreError("entry digest length")
	}
	copy(entry.PayloadDigest[:], digest)
	return entry, nil
}

func nullStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}
func boundedSnapshotLimit(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > 1000 {
		return 1000
	}
	return limit
}
func integrityStoreError(message string) error {
	return fmt.Errorf("%s: %w", message, &snapshot.RuleError{Code: "snapshot_integrity_error"})
}
