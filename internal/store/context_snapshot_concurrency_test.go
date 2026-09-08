package store

import (
	"context"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
	"testing"
)

func TestConcurrentSnapshotSchemaNeverExposesBuildingHeads(t *testing.T) {
	s, err := Open(t.TempDir() + "/snapshot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM context_snapshots WHERE state='building'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("building heads=%d", n)
	}
}

func TestConcurrentSnapshotBuildingStateIsInvisibleToSecondConnection(t *testing.T) {
	path := t.TempDir() + "/snapshot.db"
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	in := snapshotCreateInput()
	observed := false
	_, err = writer.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), func(context.Context) (snapshot.GitProvenance, error) {
		var heads, entries int
		if err := reader.DB().QueryRow(`SELECT count(*),(SELECT count(*) FROM context_snapshot_entries) FROM context_snapshots`).Scan(&heads, &entries); err != nil {
			return snapshot.GitProvenance{}, err
		}
		if heads != 0 || entries != 0 {
			t.Fatalf("reader saw partial heads=%d entries=%d", heads, entries)
		}
		observed = true
		return in.Git, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !observed {
		t.Fatal("recheck observation did not run")
	}
}
