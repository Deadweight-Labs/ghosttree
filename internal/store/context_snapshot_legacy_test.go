package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

func legacyFixtureDigest(schema uint32, entries []snapshot.EntrySummary) snapshot.Digest {
	entries = append([]snapshot.EntrySummary(nil), entries...)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Domain != entries[j].Domain {
			return entries[i].Domain < entries[j].Domain
		}
		return entries[i].Key < entries[j].Key
	})
	h := sha256.New()
	h.Write([]byte("ghosttree-context-snapshot\x00"))
	binary.Write(h, binary.BigEndian, schema)
	for _, entry := range entries {
		binary.Write(h, binary.BigEndian, uint32(len(entry.Domain)))
		h.Write([]byte(entry.Domain))
		binary.Write(h, binary.BigEndian, uint32(len(entry.Key)))
		h.Write([]byte(entry.Key))
		h.Write(entry.PayloadDigest[:])
	}
	var digest snapshot.Digest
	copy(digest[:], h.Sum(nil))
	return digest
}

func TestLegacySnapshotsRemainReadableAndRetryable(t *testing.T) {
	for _, schema := range []uint32{1, 2} {
		t.Run(fmt.Sprint(schema), func(t *testing.T) {
			s, err := Open(t.TempDir() + "/legacy.db")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			_, err = s.DB().Exec(`INSERT INTO documents(id,project,slug,kind,title,head_revision,status,created_at,updated_at) VALUES(4711,'p','old-slug','spec','Spec',1,'active','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'); INSERT INTO document_revisions(document_id,revision,body,digest,message,created_at) VALUES(4711,1,'doc','digest','message','2026-01-01T00:00:00Z')`)
			if err != nil {
				t.Fatal(err)
			}
			in := snapshotCreateInput()
			insertLegacySnapshot(t, s, in, schema)
			before := snapshotLegacyStoredBytes(t, s)
			head, _, err := s.ContextSnapshot(context.Background(), "p", in.Name)
			if err != nil {
				t.Fatal(err)
			}
			if head.SchemaVersion != schema {
				t.Fatalf("schema changed: %+v", head)
			}
			retry, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), nil)
			if err != nil || retry.Created {
				t.Fatalf("retry=%+v err=%v", retry, err)
			}
			if got := snapshotLegacyStoredBytes(t, s); !bytes.Equal(before, got) {
				t.Fatal("legacy retry changed stored bytes")
			}
			message := "changed"
			in.Message = &message
			if _, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), nil); snapshotCode(err) != "snapshot_name_conflict" {
				t.Fatalf("changed metadata retry: %v", err)
			}
			in.Name = "new-current"
			created, err := s.CreateContextSnapshot(context.Background(), in, snapshot.DefaultLimits(), nil)
			if err != nil || !created.Created || created.Snapshot.SchemaVersion != snapshot.SchemaVersion {
				t.Fatalf("new=%+v err=%v", created, err)
			}
			page, err := s.ListContextSnapshots(context.Background(), snapshot.ListFilter{Project: "p"})
			if err != nil || len(page.Snapshots) != 2 {
				t.Fatalf("mixed list=%+v err=%v", page, err)
			}
		})
	}
}

func snapshotLegacyStoredBytes(t *testing.T, s *Store) []byte {
	t.Helper()
	var raw []byte
	if err := s.DB().QueryRow(`SELECT CAST(schema_version AS TEXT)||hex(content_digest)||hex(payload)||hex(payload_digest) FROM context_snapshots JOIN context_snapshot_entries ON id=snapshot_id WHERE name='release-1'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func insertLegacySnapshot(t *testing.T, s *Store, in snapshot.CreateInput, schema uint32) {
	t.Helper()
	entries, err := captureContextEntries(context.Background(), s.DB(), in.Project, snapshot.SchemaVersion, snapshot.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	createdAt := "2026-01-02T00:00:00Z"
	res, err := s.DB().Exec(`INSERT INTO context_snapshots(project,name,schema_version,state,git_object_format,git_commit,git_dirty,allow_dirty_used,git_metadata_source,actor_id,created_at) VALUES(?,?,?,'building',?,?,0,0,?,?,?)`, in.Project, in.Name, schema, in.Git.ObjectFormat, in.Git.Commit, in.Git.MetadataSource, in.ActorID, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{"document": 0, "ghost": 0, "ghost-review": 0, "knowledge": 0, "request": 0}
	var summaries []snapshot.EntrySummary
	var total int64
	for _, entry := range entries {
		if schema == 1 && entry.Domain == "document" {
			entry.Key = "old-slug"
		}
		_, err := s.DB().Exec(`INSERT INTO context_snapshot_entries(snapshot_id,domain,entry_key,payload,payload_digest,payload_size) VALUES(?,?,?,?,?,?)`, id, entry.Domain, entry.Key, []byte(entry.Payload), entry.PayloadDigest[:], entry.PayloadSize)
		if err != nil {
			t.Fatal(err)
		}
		summaries = append(summaries, snapshot.EntrySummary{Domain: entry.Domain, Key: entry.Key, PayloadDigest: entry.PayloadDigest, PayloadSize: entry.PayloadSize})
		counts[entry.Domain]++
		total += entry.PayloadSize
	}
	digest := legacyFixtureDigest(schema, summaries)
	countsJSON, err := snapshot.MarshalCanonical(counts)
	if err != nil {
		t.Fatal(err)
	}
	headBytes, err := snapshot.MarshalCanonical(snapshot.DigestHead{Project: in.Project, Name: in.Name, SchemaVersion: schema, Git: in.Git, ActorID: in.ActorID, CreatedAt: createdAt})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB().Exec(`UPDATE context_snapshots SET state='sealed',content_digest=?,entry_count=?,payload_bytes_total=?,counts_json=?,sealed_logical_bytes=? WHERE id=?`, digest[:], len(entries), total, countsJSON, snapshot.LogicalSize(headBytes, summaries), id)
	if err != nil {
		t.Fatal(err)
	}
}
