package store

import (
	"context"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

func TestCaptureContextIncludesAllFiveDomainsAndExcludesOtherProjects(t *testing.T) {
	s, err := Open(t.TempDir() + "/capture.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	db := s.DB()
	if _, err := db.Exec(`
		INSERT INTO persons(id,name,token_hash,created_at) VALUES(7,'Robin','x','2026-01-01T00:00:00Z');
		INSERT INTO sessions(id,harness,external_id,project,started_at,last_seen_at) VALUES(9,'codex','s','p','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO knowledge(id,type,title,body,project,branch,confidence,status,origin,person,created_at,updated_at)
		VALUES(1,'instruction','K','Body','p','dev','trusted','active','human','Robin','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
		      (2,'note','Other','Body','q','','trusted','active','human','Robin','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO instruction_activation_path(knowledge_id,pattern) VALUES(1,'internal/**');
		INSERT INTO knowledge_evidence(knowledge_id,session_id,chunk_seq,quote) VALUES(1,9,2,'evidence');
		INSERT INTO ghost_files(project,path,kind,description,content_sha,git_blob,line_count,person,described_at,updated_at)
		VALUES('p','a.go','file','A','sha','blob',2,'Robin','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO ghost_reviews(project,path,git_blob,person,at) VALUES('p','b.go','blob2','Robin','2026-01-01T00:00:00Z');
		INSERT INTO documents(id,project,slug,kind,title,head_revision,status,person,created_at,updated_at)
		VALUES(3,'p','spec','spec','Spec',1,'archived','Robin','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO document_revisions(document_id,revision,body,digest,message,person,created_at)
		VALUES(3,1,'doc','digest','message','Robin','2026-01-01T00:00:00Z');
		INSERT INTO requests(id,type,title,description,state,project,origin,person,created_at,updated_at)
		VALUES(4,'feature','R','Desc','open','p','human','Robin','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO request_criteria(id,request_id,number,description,state,created_at,updated_at)
		VALUES(5,4,1,'AC','open','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO request_work(id,request_id,session_id,role,state,started_at,summary)
		VALUES(6,4,9,'primary','active','2026-01-01T00:00:00Z','handoff');
	`); err != nil {
		t.Fatal(err)
	}

	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	entries, err := captureContextEntries(context.Background(), conn, "p")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"document:spec", "ghost:file/a.go", "ghost-review:b.go", "knowledge:1", "request:4"}
	if len(entries) != len(want) {
		t.Fatalf("entries=%d, want %d: %+v", len(entries), len(want), entries)
	}
	for i, entry := range entries {
		got := entry.Domain + ":" + entry.Key
		if got != want[i] {
			t.Errorf("entry[%d]=%s, want %s", i, got, want[i])
		}
		if err := snapshot.ValidateCanonical(entry.Payload); err != nil {
			t.Errorf("%s payload: %v", got, err)
		}
		if entry.PayloadDigest != snapshot.EntryDigest(entry.Payload) {
			t.Errorf("%s digest mismatch", got)
		}
	}
}

func TestCaptureExcludesGlobalAndMachineOnlyKnowledge(t *testing.T) {
	s, err := Open(t.TempDir() + "/capture.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB().Exec(`INSERT INTO knowledge(type,title,body,project,machine,created_at,updated_at) VALUES
		('note','global','x','','','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
		('note','machine','x','','host','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	entries, err := captureContextEntries(context.Background(), s.DB(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("captured excluded entries: %+v", entries)
	}
}
