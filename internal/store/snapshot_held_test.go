package store

import (
	"context"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

// A snapshot belongs to every reader of the project, so it carries nothing that
// hangs on a private session: not held findings, not handoffs (REQ-438 D, review 3).
func TestSnapshotLeavesOutHeldFindingsAndPrivateHandoffs(t *testing.T) {
	s, err := Open(t.TempDir() + "/held-capture.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	db := s.DB()
	if _, err := db.Exec(`
		INSERT INTO persons(id,name,token_hash,created_at) VALUES(7,'Robin','x','2026-01-01T00:00:00Z');
		INSERT INTO sessions(id,harness,external_id,project,visibility,shared,started_at,last_seen_at)
		VALUES(9,'codex','priv','p','private',0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
		      (10,'codex','pub','p','project',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO knowledge(id,type,title,body,project,branch,confidence,status,origin,person,session_ref,created_at,updated_at)
		VALUES(1,'note','HELDK','Body','p','','quarantined','active','distilled','','session:9#2','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
		      (2,'note','OPENK','Body','p','','quarantined','active','distilled','','session:10#2','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
		      (3,'note','AGENTK','Body','p','','trusted','active','agent','Robin','session:9#2','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO requests(id,type,title,description,state,project,origin,person,session_ref,created_at,updated_at)
		VALUES(4,'feature','HELDR','Desc','open','p','distilled','','session:9','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
		      (5,'feature','WORKR','Desc','open','p','agent','Robin','','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO request_work(id,request_id,session_id,role,state,started_at,summary)
		VALUES(6,5,9,'primary','paused','2026-01-01T00:00:00Z','PRIVATE-HANDOFF'),(7,5,10,'related','paused','2026-01-01T00:00:00Z','SHARED-HANDOFF');
	`); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	entries, err := captureContextEntries(context.Background(), conn, "p", snapshot.SchemaVersion, snapshot.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, e := range entries {
		all.Write(e.Payload)
	}
	got := all.String()
	for _, leak := range []string{"HELDK", "HELDR", "PRIVATE-HANDOFF"} {
		if strings.Contains(got, leak) {
			t.Errorf("snapshot carries %s", leak)
		}
	}
	for _, keep := range []string{"OPENK", "AGENTK", "WORKR", "SHARED-HANDOFF"} {
		if !strings.Contains(got, keep) {
			t.Errorf("snapshot lost %s", keep)
		}
	}
}
