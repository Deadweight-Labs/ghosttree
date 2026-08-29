package store

import (
	"context"
	"errors"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

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
	limits := snapshot.DefaultLimits()
	limits.MaxProjectLogicalBytes = 1
	_, err = s.CreateContextSnapshot(context.Background(), in, limits, recheck)
	if snapshotCode(err) != "snapshot_name_conflict" {
		t.Fatalf("err=%v code=%q", err, snapshotCode(err))
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
