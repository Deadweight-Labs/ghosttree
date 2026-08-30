package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

type rejectQueriesAfterDocuments struct {
	db          *sql.DB
	laterCalled bool
}

type rejectRequestChildren struct {
	db          *sql.DB
	childCalled bool
}

func (q *rejectRequestChildren) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, "FROM request_criteria") {
		q.childCalled = true
		return nil, errors.New("request children queried")
	}
	return q.db.QueryContext(ctx, query, args...)
}

func (q *rejectRequestChildren) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return q.db.QueryRowContext(ctx, query, args...)
}

func (q *rejectQueriesAfterDocuments) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, "FROM ghost_reviews") {
		q.laterCalled = true
		return nil, errors.New("later domain queried")
	}
	return q.db.QueryContext(ctx, query, args...)
}

func (q *rejectQueriesAfterDocuments) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return q.db.QueryRowContext(ctx, query, args...)
}

func TestSnapshotCollectorEnforcesCountBeforeEncoding(t *testing.T) {
	limits := snapshot.DefaultLimits()
	limits.MaxEntriesPerSnapshot = 1
	collector := newSnapshotCollector(limits)
	if err := collector.add("knowledge", "1", func(w io.Writer) error {
		_, err := io.WriteString(w, `null`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	secondEncoderCalls := 0
	err := collector.add("knowledge", "2", func(io.Writer) error {
		secondEncoderCalls++
		return nil
	})
	if snapshotCode(err) != "snapshot_limit_exceeded" {
		t.Fatalf("second add error=%v", err)
	}
	if secondEncoderCalls != 0 {
		t.Fatalf("second encoder called %d times", secondEncoderCalls)
	}
	if len(collector.entries) != 1 {
		t.Fatalf("collector retained %d entries, want 1", len(collector.entries))
	}
}

func TestSnapshotCollectorEnforcesEntryAndRemainingTotalLimits(t *testing.T) {
	for _, tc := range []struct {
		name       string
		entryLimit int64
		totalLimit int64
	}{
		{name: "entry", entryLimit: 4, totalLimit: 100},
		{name: "remaining-total", entryLimit: 100, totalLimit: 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limits := snapshot.DefaultLimits()
			limits.MaxEntryPayloadBytes = tc.entryLimit
			limits.MaxSnapshotPayloadBytes = tc.totalLimit
			collector := newSnapshotCollector(limits)
			if tc.name == "remaining-total" {
				if err := collector.add("knowledge", "1", func(w io.Writer) error {
					_, err := io.WriteString(w, `null`)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			before := len(collector.entries)
			beforeTotal := collector.payloadBytes
			err := collector.add("knowledge", "2", func(w io.Writer) error {
				return snapshot.WriteCanonical(w, strings.Repeat("x", 64))
			})
			if snapshotCode(err) != "snapshot_limit_exceeded" {
				t.Fatalf("oversized add error=%v", err)
			}
			if len(collector.entries) != before || collector.payloadBytes != beforeTotal {
				t.Fatalf("failed candidate changed collector: entries=%d total=%d", len(collector.entries), collector.payloadBytes)
			}
		})
	}
}

func TestSnapshotCollectorAllowsExactEntryAndTotalLimits(t *testing.T) {
	limits := snapshot.DefaultLimits()
	limits.MaxEntryPayloadBytes = 4
	limits.MaxSnapshotPayloadBytes = 8
	collector := newSnapshotCollector(limits)
	for _, key := range []string{"1", "2"} {
		if err := collector.add("knowledge", key, func(w io.Writer) error {
			_, err := io.WriteString(w, `null`)
			return err
		}); err != nil {
			t.Fatalf("exact-limit add %s: %v", key, err)
		}
	}
	if collector.payloadBytes != 8 || len(collector.entries) != 2 {
		t.Fatalf("entries=%d payload=%d", len(collector.entries), collector.payloadBytes)
	}
	if err := collector.add("knowledge", "3", func(w io.Writer) error {
		_, err := io.WriteString(w, `null`)
		return err
	}); snapshotCode(err) != "snapshot_limit_exceeded" {
		t.Fatalf("one payload beyond exact total error=%v", err)
	}
}

func TestBoundedPayloadBufferAllowsEqualityAndStoresNoExcess(t *testing.T) {
	limitErr := errors.New("limit")
	buffer := newBoundedPayloadBuffer(4, limitErr)
	if n, err := io.WriteString(buffer, "1234"); err != nil || n != 4 {
		t.Fatalf("exact write n=%d err=%v", n, err)
	}
	if got := buffer.Len(); got != 4 {
		t.Fatalf("exact buffer len=%d", got)
	}
	if n, err := io.WriteString(buffer, "5"); n != 0 || !errors.Is(err, limitErr) {
		t.Fatalf("excess write n=%d err=%v", n, err)
	}
	if got := buffer.Len(); got != 4 {
		t.Fatalf("excess byte was buffered: len=%d", got)
	}
}

func TestCaptureStopsBeforeLaterDomainsAfterPayloadLimit(t *testing.T) {
	s, err := Open(t.TempDir() + "/capture-limit.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB().Exec(`
		INSERT INTO documents(id,project,slug,kind,title,head_revision,status,created_at,updated_at)
		VALUES(1,'p','large','spec','Spec',1,'active','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO document_revisions(document_id,revision,body,digest,message,created_at)
		VALUES(1,1,?,'digest','message','2026-01-01T00:00:00Z');
	`, strings.Repeat("x", 1<<20)); err != nil {
		t.Fatal(err)
	}
	limits := snapshot.DefaultLimits()
	limits.MaxEntryPayloadBytes = 64
	queryer := &rejectQueriesAfterDocuments{db: s.DB()}
	if _, err := captureContextEntries(context.Background(), queryer, "p", snapshot.SchemaVersionV2, limits); snapshotCode(err) != "snapshot_limit_exceeded" {
		t.Fatalf("capture error=%v", err)
	}
	if queryer.laterCalled {
		t.Fatal("capture queried a later domain after the payload limit")
	}
}

func TestCaptureRequestChecksCountBeforeScanAndChildQueries(t *testing.T) {
	s, err := Open(t.TempDir() + "/capture-count.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB().Exec(`INSERT INTO requests(id,type,title,description,state,project,origin,created_at,updated_at)
		VALUES(1,'feature','large',?,'open','p','human','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, strings.Repeat("x", 1<<20)); err != nil {
		t.Fatal(err)
	}
	limits := snapshot.DefaultLimits()
	limits.MaxEntriesPerSnapshot = 0
	collector := newSnapshotCollector(limits)
	queryer := &rejectRequestChildren{db: s.DB()}
	if err := captureRequests(context.Background(), queryer, "p", collector); snapshotCode(err) != "snapshot_limit_exceeded" {
		t.Fatalf("capture error=%v", err)
	}
	if queryer.childCalled {
		t.Fatal("capture queried request children after the count limit")
	}
}

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
	entries, err := captureContextEntries(context.Background(), conn, "p", snapshot.SchemaVersion, snapshot.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"document:3", "ghost:file/a.go", "ghost-review:b.go", "knowledge:1", "request:4"}
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

func TestCaptureDocumentKeyKeepsV1SlugAndUsesV2PersistentID(t *testing.T) {
	s, err := Open(t.TempDir() + "/document-schema.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB().Exec(`
		INSERT INTO documents(id,project,slug,kind,title,head_revision,status,created_at,updated_at)
		VALUES(4711,'p','mutable-slug','spec','Spec',1,'active','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO document_revisions(document_id,revision,body,digest,message,created_at)
		VALUES(4711,1,'doc','digest','message','2026-01-01T00:00:00Z');
	`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		version uint32
		wantKey string
	}{
		{version: snapshot.SchemaVersionV1, wantKey: "mutable-slug"},
		{version: snapshot.SchemaVersionV2, wantKey: "4711"},
	} {
		entries, err := captureContextEntries(context.Background(), s.DB(), "p", tc.version, snapshot.DefaultLimits())
		if err != nil {
			t.Fatalf("schema %d: %v", tc.version, err)
		}
		if len(entries) != 1 || entries[0].Domain != "document" || entries[0].Key != tc.wantKey {
			t.Fatalf("schema %d entries=%+v", tc.version, entries)
		}
	}
}

func TestCaptureRequestPreservesRequestLevelEvidenceAfterLiveMutation(t *testing.T) {
	s, err := Open(t.TempDir() + "/request-evidence.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB().Exec(`
		INSERT INTO persons(id,name,token_hash,created_at) VALUES(7,'Robin','x','2026-01-01T00:00:00Z');
		INSERT INTO requests(id,type,title,description,state,project,origin,person,created_at,updated_at)
		VALUES(4,'feature','R','Desc','done','p','human','Robin','2026-01-01T00:00:00Z','2026-01-02T00:00:00Z');
		INSERT INTO request_criteria(id,request_id,number,description,state,created_at,updated_at)
		VALUES(5,4,1,'AC','met','2026-01-01T00:00:00Z','2026-01-02T00:00:00Z');
		INSERT INTO request_evidence(id,request_id,criterion_id,kind,ref,person,created_at)
		VALUES(6,4,5,'test','internal/store/request_test.go','Robin','2026-01-02T00:00:00Z'),
		      (7,4,NULL,'commit','abc123','Robin','2026-01-02T00:01:00Z');
	`); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	in := snapshotCreateInput()
	in.Project = "p"
	if _, err := s.CreateContextSnapshot(ctx, in, snapshot.DefaultLimits(), func(context.Context) (snapshot.GitProvenance, error) { return in.Git, nil }); err != nil {
		t.Fatal(err)
	}
	page, err := s.ContextSnapshotEntries(ctx, "p", in.Name, snapshot.EntryFilter{Domain: "request", Key: "4"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Exact == nil {
		t.Fatal("request snapshot entry not found")
	}
	captured := append([]byte(nil), page.Exact.Payload...)
	var payload struct {
		Evidence []requestEvidenceV1  `json:"evidence"`
		Criteria []requestCriterionV1 `json:"criteria"`
	}
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Evidence) != 1 {
		t.Fatalf("request evidence=%+v, want one completion evidence", payload.Evidence)
	}
	got := payload.Evidence[0]
	if got.ID != 7 || got.CriterionID != 0 || got.Kind != "commit" || got.Ref != "abc123" || got.Person.ID != "person:7" || got.Person.Label != "Robin" || got.CreatedAt != "2026-01-02T00:01:00Z" {
		t.Fatalf("request evidence=%+v", got)
	}
	if len(payload.Criteria) != 1 || len(payload.Criteria[0].Evidence) != 1 || payload.Criteria[0].Evidence[0].ID != 6 {
		t.Fatalf("criterion evidence=%+v", payload.Criteria)
	}
	if _, err := s.DB().Exec(`DELETE FROM request_evidence WHERE request_id=4`); err != nil {
		t.Fatal(err)
	}
	after, err := s.ContextSnapshotEntries(ctx, "p", in.Name, snapshot.EntryFilter{Domain: "request", Key: "4"})
	if err != nil {
		t.Fatal(err)
	}
	if after.Exact == nil || !bytes.Equal(captured, after.Exact.Payload) {
		t.Fatal("materialized request payload changed after live evidence deletion")
	}
}

func TestCaptureRequestUsesEmptyArrayForNoRequestLevelEvidence(t *testing.T) {
	s, err := Open(t.TempDir() + "/empty-request-evidence.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB().Exec(`INSERT INTO requests(id,type,title,description,state,project,origin,created_at,updated_at)
		VALUES(4,'feature','R','Desc','open','p','human','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	conn, err := s.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	entries, err := captureContextEntries(context.Background(), conn, "p", snapshot.SchemaVersion, snapshot.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(entries[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if got, ok := payload["evidence"]; !ok || string(got) != "[]" {
		t.Fatalf("evidence=%s present=%v, want canonical empty array", got, ok)
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
	entries, err := captureContextEntries(context.Background(), s.DB(), "p", snapshot.SchemaVersion, snapshot.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("captured excluded entries: %+v", entries)
	}
}
