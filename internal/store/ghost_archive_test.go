package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func archiveTarget(t *testing.T, s *Store, project, path string) GhostArchiveTarget {
	t.Helper()
	candidate, err := s.PrepareGhostArchive(project, path)
	if err != nil {
		t.Fatal(err)
	}
	return candidate.Target
}

func TestArchiveGhostFilesPreservesHistoryClearsFTSAndRetries(t *testing.T) {
	s := openTest(t)
	for _, g := range []GhostFile{
		{Project: "p", Path: "old", Kind: "dir", Description: "orphanuniqueword directory", Person: "author"},
		{Project: "p", Path: "old/gone.go", Description: "orphanuniqueword source", Person: "author", ContentSHA: "sha", GitBlob: "blob", LineCount: 42},
		{Project: "p", Path: "old/keep.go", Description: "keep this sibling"},
	} {
		if _, err := s.PutGhostFile(g); err != nil {
			t.Fatal(err)
		}
	}
	in := GhostArchiveInput{Project: "p", Targets: []GhostArchiveTarget{archiveTarget(t, s, "p", "old"), archiveTarget(t, s, "p", "old/gone.go")}, Reason: "migrated to documents", ConfirmDeleted: true, Person: "operator"}
	got, err := s.ArchiveGhostFiles(in)
	if err != nil || len(got.Archived) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.GhostFileByPath("p", "old/gone.go"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if _, err := s.GhostFileByPath("p", "old/keep.go"); err != nil {
		t.Fatal("recursive deletion", err)
	}
	hist, err := s.GhostFileHistory("p", "old/gone.go", 0)
	if err != nil || len(hist) != 1 {
		t.Fatalf("%v %v", hist, err)
	}
	v := hist[0]
	if v.Description != "orphanuniqueword source" || v.ContentSHA != "sha" || v.GitBlob != "blob" || v.LineCount != 42 || v.Person != "author" || !strings.Contains(v.Reason, "operator") || !strings.Contains(v.Reason, in.Reason) {
		t.Fatalf("archive lost provenance: %+v", v)
	}
	hits, err := s.SearchGhostFiles("orphanuniqueword", "p", 20)
	if err != nil || len(hits) != 0 {
		t.Fatalf("FTS retains archived text: %v %v", hits, err)
	}
	got, err = s.ArchiveGhostFiles(in)
	if err != nil || len(got.Archived) != 0 || len(got.AlreadyArchived) != 2 {
		t.Fatalf("retry: %+v %v", got, err)
	}
	hist, _ = s.GhostFileHistory("p", "old/gone.go", 0)
	if len(hist) != 1 {
		t.Fatal("retry duplicated history")
	}
}

func TestArchiveGhostFilesRejectsBroadAndUnconfirmedRequests(t *testing.T) {
	s := openTest(t)
	if _, err := s.PutGhostFile(GhostFile{Project: "p", Path: "old.go", Description: "safe"}); err != nil {
		t.Fatal(err)
	}
	target := archiveTarget(t, s, "p", "old.go")
	base := GhostArchiveInput{Project: "p", Targets: []GhostArchiveTarget{target}, Reason: "gone", ConfirmDeleted: true, Person: "operator"}
	for _, tc := range []struct {
		name   string
		change func(*GhostArchiveInput)
	}{
		{"unconfirmed", func(i *GhostArchiveInput) { i.ConfirmDeleted = false }},
		{"empty", func(i *GhostArchiveInput) { i.Targets = nil }},
		{"root", func(i *GhostArchiveInput) {
			i.Targets = []GhostArchiveTarget{{Path: "", ExpectedToken: target.ExpectedToken}}
		}},
		{"dot", func(i *GhostArchiveInput) {
			i.Targets = []GhostArchiveTarget{{Path: ".", ExpectedToken: target.ExpectedToken}}
		}},
		{"escape", func(i *GhostArchiveInput) {
			i.Targets = []GhostArchiveTarget{{Path: "../old.go", ExpectedToken: target.ExpectedToken}}
		}},
		{"duplicate", func(i *GhostArchiveInput) { i.Targets = []GhostArchiveTarget{target, target} }},
		{"too many", func(i *GhostArchiveInput) { i.Targets = make([]GhostArchiveTarget, 65) }},
		{"no reason", func(i *GhostArchiveInput) { i.Reason = " " }},
		{"no actor", func(i *GhostArchiveInput) { i.Person = "" }},
		{"no project", func(i *GhostArchiveInput) { i.Project = "" }},
		{"no token", func(i *GhostArchiveInput) { i.Targets = []GhostArchiveTarget{{Path: "old.go"}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.change(&in)
			if _, err := s.ArchiveGhostFiles(in); !errors.Is(err, ErrGhostArchiveInvalid) {
				t.Fatalf("want validation error, got %v", err)
			}
		})
	}
	if _, err := s.GhostFileByPath("p", "old.go"); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveGhostFilesConflictsAtomicallyAndRejectsRecreatedPath(t *testing.T) {
	s := openTest(t)
	for _, p := range []string{"a.go", "b.go"} {
		if _, err := s.PutGhostFile(GhostFile{Project: "p", Path: p, Description: "original"}); err != nil {
			t.Fatal(err)
		}
	}
	in := GhostArchiveInput{Project: "p", Targets: []GhostArchiveTarget{archiveTarget(t, s, "p", "a.go"), archiveTarget(t, s, "p", "b.go")}, Reason: "deleted", ConfirmDeleted: true, Person: "operator"}
	b, _ := s.GhostFileByPath("p", "b.go")
	b.Description = "newer"
	if _, err := s.PutGhostFile(b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveGhostFiles(in); !errors.Is(err, ErrGhostArchiveConflict) {
		t.Fatal(err)
	}
	if _, err := s.GhostFileByPath("p", "a.go"); err != nil {
		t.Fatal("partial batch deletion", err)
	}
	in.Targets = in.Targets[:1]
	original, _ := s.GhostFileByPath("p", "a.go")
	if _, err := s.ArchiveGhostFiles(in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutGhostFile(original); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveGhostFiles(in); !errors.Is(err, ErrGhostArchiveConflict) {
		t.Fatalf("old retry deleted recreated path: %v", err)
	}
	if _, err := s.GhostFileByPath("p", "a.go"); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveTokenABAAcrossMove(t *testing.T) {
	s := openTest(t)
	original := GhostFile{Project: "p", Path: "a.go", Description: "same words", Person: "author"}
	if _, err := s.PutGhostFile(original); err != nil {
		t.Fatal(err)
	}
	before, err := s.GhostFileByPath("p", "a.go")
	if err != nil {
		t.Fatal(err)
	}
	stale := GhostArchiveInput{Project: "p", Targets: []GhostArchiveTarget{archiveTarget(t, s, "p", "a.go")}, Reason: "deleted", ConfirmDeleted: true, Person: "operator"}
	if err := s.MoveGhostFile("p", "a.go", "b.go"); err != nil {
		t.Fatal(err)
	}
	other := stale
	other.Targets = []GhostArchiveTarget{archiveTarget(t, s, "p", "b.go")}
	if _, err := s.ArchiveGhostFiles(other); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutGhostFile(before); err != nil {
		t.Fatal(err)
	}
	current, err := s.GhostFileByPath("p", "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE ghost_files SET updated_at=? WHERE project=? AND path=?`, before.UpdatedAt, "p", "a.go"); err != nil {
		t.Fatal(err)
	}
	current, _ = s.GhostFileByPath("p", "a.go")
	out, err := s.ArchiveGhostFiles(stale)
	if !errors.Is(err, ErrGhostArchiveConflict) {
		t.Fatalf("stale token accepted after move/archive/recreate: out=%+v err=%v; before=%+v current=%+v", out, err, before, current)
	}
}
