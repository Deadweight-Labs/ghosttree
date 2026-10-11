package store

import (
	"context"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

// Zitate aus privaten Sessions gelangen weder in einen Snapshot noch zu einem
// Mitglied, das nicht der Besitzer ist (REQ-438 D).
func TestSnapshotLeavesOutQuotesOfPrivateSessions(t *testing.T) {
	s, err := Open(t.TempDir() + "/private-capture.db")
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
		INSERT INTO knowledge(id,type,title,body,project,branch,confidence,status,origin,person,created_at,updated_at)
		VALUES(1,'note','K','Body','p','','trusted','active','human','Robin','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO knowledge_evidence(knowledge_id,session_id,chunk_seq,quote)
		VALUES(1,9,2,'quote-from-private'),(1,10,3,'quote-from-shared');
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
	if strings.Contains(all.String(), "quote-from-private") {
		t.Error("snapshot carries a quote from a private session")
	}
	if !strings.Contains(all.String(), "quote-from-shared") {
		t.Error("snapshot lost the quote from a shared session")
	}
}

func TestEvidenceQuotesOfPrivateSessionsStayWithTheOwner(t *testing.T) {
	f := newVisFixture(t)
	ev := []Evidence{{SessionID: f.a.ID, Quote: "from-robin-private"}, {SessionID: f.c.ID, Quote: "from-rex-project"}}
	for _, c := range []struct {
		who, label string
		want       []string
	}{
		{"person:1", "robin", []string{"from-robin-private", "from-rex-project"}},
		{"person:2", "lena", []string{"from-rex-project"}},
		{"person:3", "mia", []string{"from-rex-project"}},
	} {
		got, n := viewer(f.st, c.who, c.label).ReadableEvidence(roleProject, ev, 2)
		if len(got) != len(c.want) || n != len(c.want) {
			t.Fatalf("%s: %+v sessions=%d, want %v", c.label, got, n, c.want)
		}
		for i, q := range c.want {
			if got[i].Quote != q {
				t.Errorf("%s: quote %d = %q, want %q", c.label, i, got[i].Quote, q)
			}
		}
	}
}
