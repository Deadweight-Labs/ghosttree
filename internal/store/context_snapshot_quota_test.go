package store

import (
	"context"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
	"testing"
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
