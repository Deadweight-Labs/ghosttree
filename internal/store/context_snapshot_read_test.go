package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

func TestSnapshotPaginationUsesDescendingMonotonicID(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for i := 0; i < 5; i++ {
		insertSealedReadFixture(t, st, fmt.Sprintf("n%d", i))
	}
	ctx := context.Background()
	page1, err := st.ListContextSnapshots(ctx, snapshot.ListFilter{Project: "p", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	page2, err := st.ListContextSnapshots(ctx, snapshot.ListFilter{Project: "p", Limit: 2, Cursor: page1.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1.Snapshots) != 2 || len(page2.Snapshots) != 2 {
		t.Fatalf("page sizes %d, %d", len(page1.Snapshots), len(page2.Snapshots))
	}
	if !(page1.Snapshots[0].ID > page1.Snapshots[1].ID && page1.Snapshots[1].ID > page2.Snapshots[0].ID && page2.Snapshots[0].ID > page2.Snapshots[1].ID) {
		t.Fatalf("ids are not stable descending: %d %d %d %d", page1.Snapshots[0].ID, page1.Snapshots[1].ID, page2.Snapshots[0].ID, page2.Snapshots[1].ID)
	}
}

func TestSnapshotExactEntryAndFilterValidation(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	insertSealedReadFixture(t, st, "named")
	page, err := st.ContextSnapshotEntries(context.Background(), "p", "named", snapshot.EntryFilter{Domain: "knowledge", Key: "k"})
	if err != nil || page.Exact == nil || string(page.Exact.Payload) != `{"v":1}` {
		t.Fatalf("entry=%+v err=%v", page.Exact, err)
	}
	_, err = st.ContextSnapshotEntries(context.Background(), "p", "named", snapshot.EntryFilter{Key: "k"})
	if !snapshotRuleCode(err, "snapshot_invalid_filter") {
		t.Fatalf("key-only error=%v", err)
	}
	_, err = st.ContextSnapshotEntries(context.Background(), "p", "named", snapshot.EntryFilter{Domain: "knowledge", Key: "absent"})
	if !snapshotRuleCode(err, "snapshot_entry_not_found") {
		t.Fatalf("missing error=%v", err)
	}
}

func insertSealedReadFixture(t *testing.T, st *Store, name string) {
	t.Helper()
	payload := []byte(`{"v":1}`)
	digest := snapshot.EntryDigest(payload)
	result, err := st.db.Exec(`INSERT INTO context_snapshots(project,name,schema_version,state,git_object_format,git_commit,git_dirty,allow_dirty_used,git_metadata_source,actor_id,created_at) VALUES(?,?,3,'building','sha1',?,0,0,'server-verified','person:1','2026-08-29T00:00:00Z')`, "p", name, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	if _, err := st.db.Exec(`INSERT INTO context_snapshot_entries(snapshot_id,domain,entry_key,payload,payload_digest,payload_size) VALUES(?,?,?,?,?,?)`, id, "knowledge", "k", payload, digest[:], len(payload)); err != nil {
		t.Fatal(err)
	}
	digestHead := snapshot.DigestHead{Project: "p", Name: name, SchemaVersion: snapshot.SchemaVersion, Git: snapshot.GitProvenance{ObjectFormat: "sha1", Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", MetadataSource: "server-verified"}, ActorID: "person:1", CreatedAt: "2026-08-29T00:00:00Z"}
	content, err := snapshot.ContentDigest(digestHead, []snapshot.EntrySummary{{Domain: "knowledge", Key: "k", PayloadDigest: digest, PayloadSize: int64(len(payload))}})
	if err != nil {
		t.Fatal(err)
	}
	countsMap, _ := snapshot.NewCounts(snapshot.SchemaVersion)
	countsMap["knowledge"] = 1
	counts, _ := snapshot.MarshalCanonical(countsMap)
	headBytes, _ := snapshot.MarshalCanonical(digestHead)
	logical := snapshot.LogicalSize(headBytes, []snapshot.EntrySummary{{Domain: "knowledge", Key: "k", PayloadDigest: digest, PayloadSize: int64(len(payload))}})
	if _, err := st.db.Exec(`UPDATE context_snapshots SET state='sealed',content_digest=?,entry_count=1,payload_bytes_total=?,counts_json=?,sealed_logical_bytes=? WHERE id=?`, content[:], len(payload), counts, logical, id); err != nil {
		t.Fatal(err)
	}
}

func snapshotRuleCode(err error, code string) bool {
	rule, ok := err.(*snapshot.RuleError)
	return ok && rule.Code == code
}
