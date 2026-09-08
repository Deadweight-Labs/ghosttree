package store

import (
	"errors"
	"testing"
)

func TestRuntimeGhostMutationsUseAdmission(t *testing.T) {
	for _, op := range []string{"put", "move", "delivery", "review", "archive"} {
		t.Run(op, func(t *testing.T) {
			s := runtimeDomainStore(t)
			original := GhostFile{Project: "p", Path: "old.go", Description: "retained", Person: "author"}
			if _, err := s.PutGhostFile(original); err != nil {
				t.Fatal(err)
			}
			in := GhostArchiveInput{Project: "p", Targets: []GhostArchiveTarget{archiveTarget(t, s, "p", "old.go")}, Reason: "deleted", ConfirmDeleted: true, Person: "operator"}
			release := holdRuntimeWriter(t, s.writer, 0)
			var err error
			switch op {
			case "put":
				_, err = s.PutGhostFile(GhostFile{Project: "p", Path: "old.go", Description: "rejected"})
			case "move":
				err = s.MoveGhostFile("p", "old.go", "new.go")
			case "delivery":
				_, err = s.GhostFilesForDelivery("p", "old.go", "session")
			case "review":
				err = s.PutGhostReview(GhostReview{Project: "p", Path: "old.go", GitBlob: "blob"})
			case "archive":
				_, err = s.ArchiveGhostFiles(in)
			}
			if !errors.Is(err, ErrWriterOperationsFull) {
				t.Errorf("%s bypassed admission: %v", op, err)
			}
			release()
			got, err := s.GhostFileByPath("p", "old.go")
			if err != nil || got.Description != original.Description {
				t.Fatalf("rejected mutation persisted: %+v %v", got, err)
			}
			var n int
			if err := s.db.QueryRow("SELECT (SELECT COUNT(*) FROM ghost_deliveries)+(SELECT COUNT(*) FROM ghost_reviews)+(SELECT COUNT(*) FROM ghost_file_versions)").Scan(&n); err != nil || n != 0 {
				t.Fatalf("rejected side effects=%d err=%v", n, err)
			}
			out, err := s.ArchiveGhostFiles(in)
			if err != nil || len(out.Archived) != 1 {
				t.Fatalf("archive after release: %+v %v", out, err)
			}
			out, err = s.ArchiveGhostFiles(in)
			if err != nil || len(out.AlreadyArchived) != 1 {
				t.Fatalf("archive retry: %+v %v", out, err)
			}
			history, err := s.GhostFileHistory("p", "old.go", 0)
			if err != nil || len(history) != 1 || history[0].Person != "author" || history[0].Description != "retained" {
				t.Fatalf("history lost: %+v %v", history, err)
			}
		})
	}
}
