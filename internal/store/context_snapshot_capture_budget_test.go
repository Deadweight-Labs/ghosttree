package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

type rejectSnapshotLaterChildren struct {
	db     snapshotQueryer
	needle string
	called bool
}

func (q *rejectSnapshotLaterChildren) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, q.needle) {
		q.called = true
		return nil, errors.New("queried children after exhausted model budget")
	}
	return q.db.QueryContext(ctx, query, args...)
}
func (q *rejectSnapshotLaterChildren) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return q.db.QueryRowContext(ctx, query, args...)
}

func TestSnapshotCaptureStopsAccumulatingOversizedChildren(t *testing.T) {
	for _, domain := range []string{"knowledge", "request"} {
		t.Run(domain, func(t *testing.T) {
			s := openTest(t)
			limits := snapshot.DefaultLimits()
			limits.MaxEntryPayloadBytes = 2048
			collector := newSnapshotCollector(limits)
			q := &rejectSnapshotLaterChildren{db: s.db}
			var capture func(context.Context, snapshotQueryer, string, *snapshotCollector) error
			if domain == "knowledge" {
				id, e := s.InsertKnowledge(Knowledge{Type: "instruction", Title: "seed", Body: "body"})
				if e != nil {
					t.Fatal(e)
				}
				for i := 0; i < 8; i++ {
					if _, e := s.db.Exec("INSERT INTO instruction_activation_path(knowledge_id,pattern) VALUES(?,?)", id, strings.Repeat(string(rune('a'+i)), 1024)); e != nil {
						t.Fatal(e)
					}
				}
				q.needle = "FROM knowledge_evidence"
				capture = captureKnowledge
			} else {
				r, e := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "change", Title: "seed"}})
				if e != nil {
					t.Fatal(e)
				}
				for range 8 {
					if _, e := s.db.Exec("INSERT INTO request_activity(request_id,kind,data,created_at) VALUES(?,'note',?,'now')", r.Request.ID, strings.Repeat("x", 1024)); e != nil {
						t.Fatal(e)
					}
				}
				q.needle = "FROM request_work"
				capture = captureRequests
			}
			conn, err := s.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			q.db = conn
			err = capture(context.Background(), q, "", collector)
			if snapshotCode(err) != "snapshot_limit_exceeded" {
				t.Fatalf("capture=%v", err)
			}
			if q.called {
				t.Fatal("kept accumulating child collections after limit")
			}
			if len(collector.entries) != 0 {
				t.Fatal("rejected model retained")
			}
		})
	}
}

func TestSnapshotCollectorBoundsRetainedKeysAndPayloadTogether(t *testing.T) {
	limits := snapshot.DefaultLimits()
	limits.MaxSnapshotPayloadBytes = 1 << 20
	limits.MaxSnapshotLogicalBytes = 1 << 20
	c := newSnapshotCollector(limits)
	rejected := false
	for i := 0; i < 16; i++ {
		key := strings.Repeat(string(rune('a'+i)), 60000)
		err := c.add("ghost", key, func(w io.Writer) error {
			return snapshot.WriteCanonical(w, struct {
				Path string `json:"path"`
			}{key})
		})
		if snapshotCode(err) == "snapshot_limit_exceeded" {
			rejected = true
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var retained int64
		for _, entry := range c.entries {
			retained += int64(len(entry.Domain) + len(entry.Key) + 32 + cap(entry.Payload))
		}
		if retained > limits.MaxSnapshotLogicalBytes {
			t.Fatalf("retained keys/payload=%d exceeds logical reserve=%d", retained, limits.MaxSnapshotLogicalBytes)
		}
	}
	if !rejected {
		t.Fatal("logical limit never enforced during capture")
	}
}

func TestSnapshotCaptureChildBudgetsAllowExactCanonicalPayload(t *testing.T) {
	s := openTest(t)
	if _, err := s.db.Exec(`
 INSERT INTO knowledge(id,type,title,body,project,created_at,updated_at) VALUES(1,'instruction','Seed','Body','p','now','now');
 INSERT INTO instruction_activation_path(knowledge_id,pattern) VALUES(1,'a/**'),(1,'b/**');
 INSERT INTO sessions(id,harness,external_id,started_at,last_seen_at) VALUES(1,'test','capture','now','now');
 INSERT INTO knowledge_evidence(knowledge_id,session_id,chunk_seq,quote) VALUES(1,1,1,'quote1'),(1,1,2,'quote2');
 INSERT INTO requests(id,type,title,description,state,project,created_at,updated_at) VALUES(1,'change','Seed','Body','open','p','now','now');
 INSERT INTO request_criteria(id,request_id,number,description,state,created_at,updated_at) VALUES(1,1,1,'first','open','now','now'),(2,1,2,'second','open','now','now');
 INSERT INTO request_evidence(request_id,criterion_id,kind,ref,created_at) VALUES(1,1,'test','one','now'),(1,1,'test','two','now'),(1,2,'test','three','now'),(1,NULL,'test','four','now'),(1,NULL,'test','five','now');
 INSERT INTO request_relations(request_id,kind,external_ref,created_at) VALUES(1,'external','https://example.invalid/1','now'),(1,'external','https://example.invalid/2','now');
 INSERT INTO request_activity(request_id,kind,data,created_at) VALUES(1,'note','one','now'),(1,'note','two','now');
 INSERT INTO request_work(request_id,session_id,role,state,started_at,summary) VALUES(1,1,'primary','active','now','work');
 INSERT INTO request_sightings(request_id,session_id,chunk_seq,quote) VALUES(1,1,1,'one'),(1,1,2,'two');
 `); err != nil {
		t.Fatal(err)
	}
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for name, capture := range map[string]func(context.Context, snapshotQueryer, string, *snapshotCollector) error{"knowledge": captureKnowledge, "request": captureRequests} {
		t.Run(name, func(t *testing.T) {
			original := newSnapshotCollector(snapshot.DefaultLimits())
			if err := capture(context.Background(), conn, "p", original); err != nil {
				t.Fatal(err)
			}
			if len(original.entries) != 1 {
				t.Fatalf("entries=%d", len(original.entries))
			}
			entry := original.entries[0]
			limits := snapshot.DefaultLimits()
			limits.MaxEntryPayloadBytes = entry.PayloadSize
			limits.MaxSnapshotPayloadBytes = entry.PayloadSize
			exact := newSnapshotCollector(limits)
			if err := capture(context.Background(), conn, "p", exact); err != nil {
				t.Fatalf("valid exact boundary rejected: %v", err)
			}
			if len(exact.entries) != 1 || !bytes.Equal(entry.Payload, exact.entries[0].Payload) {
				t.Fatal("exact boundary changed canonical bytes")
			}
			limits.MaxEntryPayloadBytes--
			if err := capture(context.Background(), conn, "p", newSnapshotCollector(limits)); snapshotCode(err) != "snapshot_limit_exceeded" {
				t.Fatalf("one byte over limit: %v", err)
			}
		})
	}
}
