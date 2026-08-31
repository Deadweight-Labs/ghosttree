package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

func TestSnapshotQuotaAllowsEqualityAndRejectsExcess(t *testing.T) {
	s, err := Open(t.TempDir() + "/snapshot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	in := snapshotCreateInput()
	recheck := func(context.Context) (snapshot.GitProvenance, error) { return in.Git, nil }
	limits := snapshot.DefaultLimits()
	limits.MaxSnapshotsPerProject = 1
	limits.MaxSnapshotsPerStore = 1
	if _, err := s.CreateContextSnapshot(context.Background(), in, limits, recheck); err != nil {
		t.Fatal(err)
	}
	in.Name = "release-2"
	_, err = s.CreateContextSnapshot(context.Background(), in, limits, recheck)
	if snapshotCode(err) != "snapshot_limit_exceeded" {
		t.Fatalf("err=%v", err)
	}
}

func TestSnapshotLogicalTotalsIncludeCanonicalHeadsAndEntryFraming(t *testing.T) {
	s, err := Open(t.TempDir() + "/snapshot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	in := snapshotCreateInput()
	if _, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), func(context.Context) (snapshot.GitProvenance, error) { return in.Git, nil }); err != nil {
		t.Fatal(err)
	}
	projectBytes, storeBytes, err := snapshotLogicalTotals(context.Background(), s.DB(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if projectBytes <= 32 || storeBytes != projectBytes {
		t.Fatalf("project=%d store=%d", projectBytes, storeBytes)
	}
}

func TestSnapshotRetryStillEnforcesCanonicalHeadLimit(t *testing.T) {
	s, err := Open(t.TempDir() + "/snapshot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	in := snapshotCreateInput()
	message := strings.Repeat("x", 2_000)
	in.Message = &message
	recheck := func(context.Context) (snapshot.GitProvenance, error) { return in.Git, nil }
	first, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), recheck)
	if err != nil {
		t.Fatal(err)
	}
	headBytes, err := snapshot.MarshalCanonical(snapshot.DigestHead{Project: in.Project, Name: in.Name, SchemaVersion: snapshot.SchemaVersion, Git: in.Git, Message: in.Message, ActorID: in.ActorID, ActorLabel: in.ActorLabel, SessionRef: in.SessionRef, CreatedAt: first.Snapshot.CreatedAt})
	if err != nil {
		t.Fatal(err)
	}
	in.Message = nil
	limits := snapshot.DefaultLimits()
	limits.MaxCanonicalHeadBytes = int64(len(headBytes))
	if _, err := s.CreateContextSnapshot(context.Background(), in, limits, recheck); err != nil {
		t.Fatalf("retry at exact canonical-head limit: %v", err)
	}
	limits.MaxCanonicalHeadBytes = int64(len(headBytes) - 1)
	if _, err := s.CreateContextSnapshot(context.Background(), in, limits, recheck); snapshotCode(err) != "snapshot_limit_exceeded" {
		t.Fatalf("retry err=%v, want snapshot_limit_exceeded", err)
	}
}

func TestSnapshotRetryStillEnforcesLogicalSizeLimit(t *testing.T) {
	s, err := Open(t.TempDir() + "/snapshot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	in := snapshotCreateInput()
	message := strings.Repeat("x", 2_000)
	in.Message = &message
	recheck := func(context.Context) (snapshot.GitProvenance, error) { return in.Git, nil }
	if _, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), recheck); err != nil {
		t.Fatal(err)
	}
	var storedLogical int64
	if err := s.DB().QueryRow(`SELECT sealed_logical_bytes FROM context_snapshots WHERE project=? AND name=?`, in.Project, in.Name).Scan(&storedLogical); err != nil {
		t.Fatal(err)
	}
	in.Message = nil
	limits := snapshot.DefaultLimits()
	limits.MaxSnapshotLogicalBytes = storedLogical
	if _, err := s.CreateContextSnapshot(context.Background(), in, limits, recheck); err != nil {
		t.Fatalf("retry at exact logical-size limit: %v", err)
	}
	limits.MaxSnapshotLogicalBytes = storedLogical - 1
	if _, err := s.CreateContextSnapshot(context.Background(), in, limits, recheck); snapshotCode(err) != "snapshot_limit_exceeded" {
		t.Fatalf("retry err=%v, want snapshot_limit_exceeded", err)
	}
}

func TestSnapshotAggregateQuotaAddsAllSealedLogicalSizes(t *testing.T) {
	s, err := Open(t.TempDir() + "/snapshot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	in := snapshotCreateInput()
	recheck := func(context.Context) (snapshot.GitProvenance, error) { return in.Git, nil }
	if _, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), recheck); err != nil {
		t.Fatal(err)
	}
	firstProject, _, err := snapshotLogicalTotals(context.Background(), s.DB(), in.Project)
	if err != nil {
		t.Fatal(err)
	}
	in.Name = "release-2"
	if _, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), recheck); err != nil {
		t.Fatal(err)
	}
	projectTotal, storeTotal, err := snapshotLogicalTotals(context.Background(), s.DB(), in.Project)
	if err != nil {
		t.Fatal(err)
	}
	if projectTotal <= firstProject || storeTotal != projectTotal {
		t.Fatalf("first=%d project=%d store=%d", firstProject, projectTotal, storeTotal)
	}
	in.Name = "release-3"
	headBytes, err := snapshot.MarshalCanonical(snapshot.DigestHead{Project: in.Project, Name: in.Name, SchemaVersion: snapshot.SchemaVersion, Git: in.Git, ActorID: in.ActorID, CreatedAt: "2026-08-30T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	thirdLogical := snapshot.LogicalSize(headBytes, nil)
	limits := snapshot.DefaultLimits()
	limits.MaxProjectLogicalBytes = projectTotal + thirdLogical
	if _, err := s.CreateContextSnapshot(context.Background(), in, limits, recheck); err != nil {
		t.Fatalf("third snapshot at exact project aggregate limit: %v", err)
	}
	projectTotal, storeTotal, err = snapshotLogicalTotals(context.Background(), s.DB(), in.Project)
	if err != nil {
		t.Fatal(err)
	}
	in.Name = "release-4"
	limits.MaxProjectLogicalBytes = projectTotal
	if _, err := s.CreateContextSnapshot(context.Background(), in, limits, recheck); snapshotCode(err) != "snapshot_limit_exceeded" {
		t.Fatalf("fourth snapshot err=%v, want project aggregate quota failure", err)
	}
	in.Project = "other"
	in.Name = "release-other"
	limits = snapshot.DefaultLimits()
	limits.MaxStoreLogicalBytes = storeTotal
	if _, err := s.CreateContextSnapshot(context.Background(), in, limits, recheck); snapshotCode(err) != "snapshot_limit_exceeded" {
		t.Fatalf("cross-project snapshot err=%v, want store aggregate quota failure", err)
	}
}

type rejectHistoricalPayloadQueries struct{ db *sql.DB }

func (q rejectHistoricalPayloadQueries) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(strings.ToLower(query), "context_snapshot_entries") {
		return nil, fmt.Errorf("historical payload query rejected: %s", query)
	}
	return q.db.QueryContext(ctx, query, args...)
}

func (q rejectHistoricalPayloadQueries) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return q.db.QueryRowContext(ctx, query, args...)
}

func TestSnapshotLogicalTotalsDoNotReadHistoricalEntries(t *testing.T) {
	s, err := Open(t.TempDir() + "/snapshot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	in := snapshotCreateInput()
	if _, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), func(context.Context) (snapshot.GitProvenance, error) { return in.Git, nil }); err != nil {
		t.Fatal(err)
	}
	projectTotal, storeTotal, err := snapshotLogicalTotals(context.Background(), rejectHistoricalPayloadQueries{db: s.DB()}, in.Project)
	if err != nil {
		t.Fatal(err)
	}
	if projectTotal <= 0 || storeTotal != projectTotal {
		t.Fatalf("project=%d store=%d", projectTotal, storeTotal)
	}
}
