package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

func TestCreateSnapshotRetriesExistingV1WithV1CaptureSemantics(t *testing.T) {
	s, err := Open(t.TempDir() + "/snapshot-v1.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB().Exec(`
		INSERT INTO documents(id,project,slug,kind,title,head_revision,status,created_at,updated_at)
		VALUES(4711,'p','old-slug','spec','Spec',1,'active','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO document_revisions(document_id,revision,body,digest,message,created_at)
		VALUES(4711,1,'doc','digest','message','2026-01-01T00:00:00Z');
	`); err != nil {
		t.Fatal(err)
	}
	in := snapshotCreateInput()
	insertSealedV1Snapshot(t, s, in)
	var before []byte
	if err := s.DB().QueryRow(`SELECT payload FROM context_snapshot_entries WHERE entry_key='old-slug'`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	result, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), func(context.Context) (snapshot.GitProvenance, error) { return in.Git, nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.Created || result.Snapshot.SchemaVersion != snapshot.SchemaVersionV1 {
		t.Fatalf("retry result=%+v", result)
	}
	var after []byte
	if err := s.DB().QueryRow(`SELECT payload FROM context_snapshot_entries WHERE entry_key='old-slug'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("v1 retry changed stored payload bytes")
	}
}

func TestCreateSnapshotV2DocumentIdentitySurvivesSlugChange(t *testing.T) {
	s, err := Open(t.TempDir() + "/snapshot-v2.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB().Exec(`
		INSERT INTO documents(id,project,slug,kind,title,head_revision,status,created_at,updated_at)
		VALUES(4711,'p','old-slug','spec','Spec',1,'active','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO document_revisions(document_id,revision,body,digest,message,created_at)
		VALUES(4711,1,'doc','digest','message','2026-01-01T00:00:00Z');
	`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	in := snapshotCreateInput()
	first, err := s.CreateContextSnapshot(ctx, in, snapshot.DefaultLimits(), func(context.Context) (snapshot.GitProvenance, error) { return in.Git, nil })
	if err != nil {
		t.Fatal(err)
	}
	if first.Snapshot.SchemaVersion != snapshot.SchemaVersionV2 {
		t.Fatalf("first schema=%d", first.Snapshot.SchemaVersion)
	}
	oldEntry := exactSnapshotEntry(t, s, in.Name, "4711")

	if _, err := s.DB().Exec(`UPDATE documents SET slug='new-slug',updated_at='2026-01-02T00:00:00Z' WHERE id=4711`); err != nil {
		t.Fatal(err)
	}
	in.Name = "release-2"
	second, err := s.CreateContextSnapshot(ctx, in, snapshot.DefaultLimits(), func(context.Context) (snapshot.GitProvenance, error) { return in.Git, nil })
	if err != nil {
		t.Fatal(err)
	}
	newEntry := exactSnapshotEntry(t, s, in.Name, "4711")
	if oldEntry.Key != newEntry.Key || oldEntry.PayloadDigest == newEntry.PayloadDigest || first.Snapshot.ContentDigest == second.Snapshot.ContentDigest {
		t.Fatalf("old=%+v new=%+v", oldEntry, newEntry)
	}
	var oldPayload, newPayload documentPayloadV1
	if err := json.Unmarshal(oldEntry.Payload, &oldPayload); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(newEntry.Payload, &newPayload); err != nil {
		t.Fatal(err)
	}
	if oldPayload.Slug != "old-slug" || newPayload.Slug != "new-slug" {
		t.Fatalf("old slug=%q new slug=%q", oldPayload.Slug, newPayload.Slug)
	}
	if unchanged := exactSnapshotEntry(t, s, "release-1", "4711"); !bytes.Equal(unchanged.Payload, oldEntry.Payload) {
		t.Fatal("second capture changed the first snapshot payload")
	}
}

func exactSnapshotEntry(t *testing.T, s *Store, name, key string) snapshot.Entry {
	t.Helper()
	page, err := s.ContextSnapshotEntries(context.Background(), "p", name, snapshot.EntryFilter{Domain: "document", Key: key})
	if err != nil {
		t.Fatal(err)
	}
	if page.Exact == nil {
		t.Fatalf("snapshot %s has no document/%s", name, key)
	}
	return *page.Exact
}

func insertSealedV1Snapshot(t *testing.T, s *Store, in snapshot.CreateInput) {
	t.Helper()
	createdAt := "2026-01-02T00:00:00Z"
	entries, err := captureContextEntries(context.Background(), s.DB(), in.Project, snapshot.SchemaVersionV1, snapshot.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.DB().Exec(`INSERT INTO context_snapshots(project,name,schema_version,state,git_object_format,git_commit,git_dirty,allow_dirty_used,git_metadata_source,actor_id,created_at)
		VALUES(?,?,1,'building',?,?,0,0,?,?,?)`, in.Project, in.Name, in.Git.ObjectFormat, in.Git.Commit, in.Git.MetadataSource, in.ActorID, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	summaries := make([]snapshot.EntrySummary, 0, len(entries))
	counts, _ := snapshot.NewCounts(snapshot.SchemaVersionV1)
	var total int64
	for _, entry := range entries {
		if _, err := s.DB().Exec(`INSERT INTO context_snapshot_entries(snapshot_id,domain,entry_key,payload,payload_digest,payload_size) VALUES(?,?,?,?,?,?)`, id, entry.Domain, entry.Key, []byte(entry.Payload), entry.PayloadDigest[:], entry.PayloadSize); err != nil {
			t.Fatal(err)
		}
		summaries = append(summaries, snapshot.EntrySummary{Domain: entry.Domain, Key: entry.Key, PayloadDigest: entry.PayloadDigest, PayloadSize: entry.PayloadSize})
		counts[entry.Domain]++
		total += entry.PayloadSize
	}
	digest := snapshot.ContentDigest(snapshot.SchemaVersionV1, summaries)
	countsJSON, _ := snapshot.MarshalCanonical(counts)
	headBytes, _ := snapshot.MarshalCanonical(snapshotHeadFingerprintV1{Project: in.Project, Name: in.Name, SchemaVersion: snapshot.SchemaVersionV1, Git: in.Git, ActorID: in.ActorID, CreatedAt: createdAt})
	logical := snapshot.LogicalSize(headBytes, summaries)
	if _, err := s.DB().Exec(`UPDATE context_snapshots SET state='sealed',content_digest=?,entry_count=?,payload_bytes_total=?,counts_json=?,sealed_logical_bytes=? WHERE id=?`, digest[:], len(entries), total, countsJSON, logical, id); err != nil {
		t.Fatal(err)
	}
}

func snapshotCode(err error) string {
	var rule *snapshot.RuleError
	if errors.As(err, &rule) {
		return rule.Code
	}
	return ""
}

func snapshotCreateInput() snapshot.CreateInput {
	return snapshot.CreateInput{Project: "p", Name: "release-1", Git: snapshot.GitProvenance{ObjectFormat: "sha1", Commit: "0123456789012345678901234567890123456789", MetadataSource: "server-verified"}, ActorID: "person:1"}
}

func TestCreateSnapshotSealsAndRetriesIdempotently(t *testing.T) {
	s, err := Open(t.TempDir() + "/snapshot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB().Exec(`INSERT INTO knowledge(type,title,body,project,created_at,updated_at) VALUES('note','one','body','p','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	in := snapshotCreateInput()
	recheck := func(context.Context) (snapshot.GitProvenance, error) { return in.Git, nil }
	first, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), recheck)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || first.Snapshot.State != "sealed" || first.Snapshot.EntryCount != 1 {
		t.Fatalf("first=%+v", first)
	}
	zero := snapshot.DefaultLimits()
	zero.MaxSnapshotsPerProject = 0
	zero.MaxProjectLogicalBytes = 0
	zero.MaxSnapshotsPerStore = 0
	zero.MaxStoreLogicalBytes = 0
	second, err := s.CreateContextSnapshot(context.Background(), in, zero, recheck)
	if err != nil {
		t.Fatal(err)
	}
	if second.Created || second.Snapshot.ID != first.Snapshot.ID || second.Snapshot.ContentDigest != first.Snapshot.ContentDigest {
		t.Fatalf("second=%+v first=%+v", second, first)
	}
	if len(second.Snapshot.Counts) != 5 || second.Snapshot.Counts["knowledge"] != first.Snapshot.Counts["knowledge"] {
		t.Fatalf("idempotent retry lost counts: %+v", second.Snapshot.Counts)
	}
	var heads, entries int
	if err := s.DB().QueryRow(`SELECT count(*),(SELECT count(*) FROM context_snapshot_entries) FROM context_snapshots`).Scan(&heads, &entries); err != nil {
		t.Fatal(err)
	}
	if heads != 1 || entries != 1 {
		t.Fatalf("heads=%d entries=%d", heads, entries)
	}
}

func TestCreateSnapshotNameConflictPrecedesQuota(t *testing.T) {
	s, err := Open(t.TempDir() + "/snapshot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB().Exec(`INSERT INTO knowledge(id,type,title,body,project,created_at,updated_at) VALUES(1,'note','one','body','p','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	in := snapshotCreateInput()
	recheck := func(context.Context) (snapshot.GitProvenance, error) { return in.Git, nil }
	if _, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), recheck); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE knowledge SET body='changed' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	existingCommit := in.Git.Commit
	in.Git.Commit = strings.Repeat("b", 40)
	limits := snapshot.DefaultLimits()
	limits.MaxProjectLogicalBytes = 1
	_, err = s.CreateContextSnapshot(context.Background(), in, limits, recheck)
	if snapshotCode(err) != "snapshot_name_conflict" {
		t.Fatalf("err=%v code=%q", err, snapshotCode(err))
	}
	var rule *snapshot.RuleError
	if !errors.As(err, &rule) || rule.ExistingGitCommit != existingCommit || rule.RequestedGitCommit != in.Git.Commit || rule.ExistingDigest == "" || rule.RequestedDigest == "" {
		t.Fatalf("conflict=%+v", rule)
	}
}

func TestCreateSnapshotLimitFailureRollsBack(t *testing.T) {
	s, err := Open(t.TempDir() + "/snapshot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	in := snapshotCreateInput()
	message := "too long"
	in.Message = &message
	limits := snapshot.DefaultLimits()
	limits.MaxMessageBytes = 2
	_, err = s.CreateContextSnapshot(context.Background(), in, limits, func(context.Context) (snapshot.GitProvenance, error) { return in.Git, nil })
	if snapshotCode(err) != "snapshot_limit_exceeded" {
		t.Fatalf("err=%v", err)
	}
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM context_snapshots`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("heads=%d", n)
	}
}

func TestCreateSnapshotPayloadLimitLeavesNoPartialSnapshot(t *testing.T) {
	s, err := Open(t.TempDir() + "/snapshot-payload-limit.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB().Exec(`INSERT INTO knowledge(type,title,body,project,created_at,updated_at)
		VALUES('note','large',?,'p','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, strings.Repeat("x", 1<<20)); err != nil {
		t.Fatal(err)
	}
	in := snapshotCreateInput()
	limits := snapshot.DefaultLimits()
	limits.MaxEntryPayloadBytes = 128
	if _, err := s.CreateContextSnapshot(context.Background(), in, limits, func(context.Context) (snapshot.GitProvenance, error) { return in.Git, nil }); snapshotCode(err) != "snapshot_limit_exceeded" {
		t.Fatalf("error=%v", err)
	}
	var heads, entries int
	if err := s.DB().QueryRow(`SELECT count(*),(SELECT count(*) FROM context_snapshot_entries) FROM context_snapshots`).Scan(&heads, &entries); err != nil {
		t.Fatal(err)
	}
	if heads != 0 || entries != 0 {
		t.Fatalf("payload limit left heads=%d entries=%d", heads, entries)
	}
}

func TestCreateContextSnapshotRejectsInvalidHeadBeforeSQLite(t *testing.T) {
	s, err := Open(t.TempDir() + "/snapshot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	valid := snapshot.CreateInput{Project: "p", Name: "ok", ActorID: "person:1", Git: snapshot.GitProvenance{ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), MetadataSource: "client-reported"}}
	tests := []snapshot.CreateInput{
		func() snapshot.CreateInput { v := valid; v.Name = "bad/name"; return v }(),
		func() snapshot.CreateInput { v := valid; v.Name = strings.Repeat("a", 129); return v }(),
		func() snapshot.CreateInput { v := valid; v.Git.ObjectFormat = "md5"; return v }(),
		func() snapshot.CreateInput { v := valid; v.Git.Commit = "ABC"; return v }(),
		func() snapshot.CreateInput { v := valid; v.Git.MetadataSource = "invented"; return v }(),
		func() snapshot.CreateInput { v := valid; v.Git.Dirty = true; return v }(),
	}
	for _, input := range tests {
		_, err := s.CreateContextSnapshot(context.Background(), input, snapshot.DefaultLimits(), func(context.Context) (snapshot.GitProvenance, error) { return input.Git, nil })
		if snapshotCode(err) != "snapshot_invalid_input" {
			t.Fatalf("input %+v error=%v", input, err)
		}
	}
}

func TestCreateSnapshotRollsBackEveryInjectedPhase(t *testing.T) {
	for _, phase := range []string{"after_head", "after_capture", "after_entries", "after_reread", "before_seal", "before_commit"} {
		t.Run(phase, func(t *testing.T) {
			s, err := Open(t.TempDir() + "/snapshot.db")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			s.snapshotFault = func(got string) error {
				if got == phase {
					return errors.New("injected " + phase)
				}
				return nil
			}
			in := snapshotCreateInput()
			if _, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), func(context.Context) (snapshot.GitProvenance, error) { return in.Git, nil }); err == nil {
				t.Fatal("expected injected failure")
			}
			var heads, entries int
			if err := s.DB().QueryRow(`SELECT count(*),(SELECT count(*) FROM context_snapshot_entries) FROM context_snapshots`).Scan(&heads, &entries); err != nil {
				t.Fatal(err)
			}
			if heads != 0 || entries != 0 {
				t.Fatalf("phase %s left heads=%d entries=%d", phase, heads, entries)
			}
		})
	}
}

func TestSnapshotSQLiteErrorsAreTyped(t *testing.T) {
	for _, tc := range []struct {
		message, code string
		retryable     bool
	}{
		{"database is locked (SQLITE_BUSY)", "snapshot_store_busy", true},
		{"database or disk is full (SQLITE_FULL)", "snapshot_storage_exhausted", false},
	} {
		err := mapSnapshotSQLiteError(errors.New(tc.message))
		var rule *snapshot.RuleError
		if !errors.As(err, &rule) || rule.Code != tc.code || rule.Retryable != tc.retryable {
			t.Fatalf("%q -> %#v", tc.message, err)
		}
	}
}
