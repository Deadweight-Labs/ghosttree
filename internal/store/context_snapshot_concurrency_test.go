package store

import "testing"

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
