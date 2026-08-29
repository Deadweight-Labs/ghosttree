package snapshot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestExportV1IsClosedCanonicalAndByteExact(t *testing.T) {
	entries := exportFixtureEntries(t)
	head := exportFixtureHead(entries)
	counts := map[string]int64{"document": 0, "ghost": 0, "instruction": 0, "knowledge": 2, "request": 0}

	var first, second bytes.Buffer
	if err := WriteExport(&first, head, counts, entries, nil); err != nil {
		t.Fatal(err)
	}
	if err := WriteExport(&second, head, counts, entries, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("full exports differ")
	}
	if !bytes.HasSuffix(first.Bytes(), []byte("\n")) || bytes.HasSuffix(first.Bytes(), []byte("\n\n")) {
		t.Fatalf("export must end in exactly one LF: %q", first.Bytes())
	}
	if strings.Index(first.String(), `"key":"a"`) > strings.Index(first.String(), `"key":"z"`) {
		t.Fatal("entries are not sorted")
	}
	if !strings.Contains(first.String(), `"payload":{"raw":"<>& "}`) {
		t.Fatalf("canonical payload bytes were changed: %s", first.Bytes())
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(first.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	var exportedHead map[string]json.RawMessage
	if err := json.Unmarshal(envelope["snapshot"], &exportedHead); err != nil {
		t.Fatal(err)
	}
	wantFields := []string{"actor_id", "actor_label", "allow_dirty_used", "content_digest", "created_at", "entry_count", "git_branch", "git_commit", "git_dirty", "git_metadata_source", "git_object_format", "git_ref", "git_worktree_fingerprint", "git_worktree_fingerprint_version", "message", "name", "payload_bytes_total", "project", "schema_version", "session_ref"}
	gotFields := make([]string, 0, len(exportedHead))
	for field := range exportedHead {
		gotFields = append(gotFields, field)
	}
	for i := 0; i < len(gotFields); i++ {
		for j := i + 1; j < len(gotFields); j++ {
			if gotFields[j] < gotFields[i] {
				gotFields[i], gotFields[j] = gotFields[j], gotFields[i]
			}
		}
	}
	if !reflect.DeepEqual(gotFields, wantFields) {
		t.Fatalf("closed export head fields = %v", gotFields)
	}
	for _, field := range []string{"actor_label", "git_branch", "git_ref", "git_worktree_fingerprint", "git_worktree_fingerprint_version", "message", "session_ref"} {
		if string(exportedHead[field]) != "null" {
			t.Fatalf("%s must be explicit null", field)
		}
	}
	if _, err := VerifyExport(bytes.NewReader(first.Bytes())); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyExportRejectsCorruption(t *testing.T) {
	entries := exportFixtureEntries(t)
	head := exportFixtureHead(entries)
	counts := map[string]int64{"knowledge": 2}
	var out bytes.Buffer
	if err := WriteExport(&out, head, counts, entries, nil); err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Replace(out.Bytes(), []byte(`"raw":"<>& "`), []byte(`"raw":"changed"`), 1)
	if _, err := VerifyExport(bytes.NewReader(corrupt)); !isRuleCode(err, "snapshot_integrity_error") {
		t.Fatalf("corrupt payload error = %v", err)
	}
}

func TestVerifyExportRejectsAggregateCorruptionMatrix(t *testing.T) {
	entries := exportFixtureEntries(t)
	head := exportFixtureHead(entries)
	counts := map[string]int64{"knowledge": 2}
	var out bytes.Buffer
	if err := WriteExport(&out, head, counts, entries, nil); err != nil {
		t.Fatal(err)
	}
	valid := out.Bytes()
	zeroDigest := strings.Repeat("0", 64)
	cases := map[string][]byte{
		"payload":        bytes.Replace(valid, []byte(`"raw":"<>& "`), []byte(`"raw":"changed"`), 1),
		"payload_size":   bytes.Replace(valid, []byte(fmt.Sprintf(`"payload_size":%d`, entries[0].PayloadSize)), []byte(`"payload_size":999`), 1),
		"payload_digest": bytes.Replace(valid, []byte(entries[0].PayloadDigest.String()), []byte(zeroDigest), 1),
		"counts":         bytes.Replace(valid, []byte(`"knowledge":2`), []byte(`"knowledge":3`), 1),
		"entry_count":    bytes.Replace(valid, []byte(`"entry_count":2`), []byte(`"entry_count":3`), 1),
		"payload_total":  bytes.Replace(valid, []byte(fmt.Sprintf(`"payload_bytes_total":%d`, head.PayloadBytesTotal)), []byte(`"payload_bytes_total":999`), 1),
		"content_digest": bytes.Replace(valid, []byte(head.ContentDigest.String()), []byte(zeroDigest), 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if bytes.Equal(raw, valid) {
				t.Fatal("mutation did not change fixture")
			}
			if _, err := VerifyExport(bytes.NewReader(raw)); !isRuleCode(err, "snapshot_integrity_error") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestExportV1RejectsNonCanonicalPayload(t *testing.T) {
	entry := Entry{Domain: "knowledge", Key: "x", Payload: json.RawMessage(`{"b":1,"a":2}`)}
	entry.PayloadDigest = EntryDigest(entry.Payload)
	entry.PayloadSize = int64(len(entry.Payload))
	head := exportFixtureHead([]Entry{entry})
	if err := WriteExport(&bytes.Buffer{}, head, map[string]int64{"knowledge": 1}, []Entry{entry}, nil); !isRuleCode(err, "snapshot_integrity_error") {
		t.Fatalf("error = %v", err)
	}
}

func TestVerifyExportRejectsUnknownSnapshotSchemaAndInvalidProjection(t *testing.T) {
	entries := exportFixtureEntries(t)
	head := exportFixtureHead(entries)
	counts := map[string]int64{"knowledge": 2}
	var out bytes.Buffer
	if err := WriteExport(&out, head, counts, entries, nil); err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Replace(out.Bytes(), []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1)
	if _, err := VerifyExport(bytes.NewReader(unknown)); !isRuleCode(err, "unsupported_snapshot_schema") {
		t.Fatalf("unknown schema error = %v", err)
	}
	key := "a"
	if err := WriteExport(&bytes.Buffer{}, head, counts, entries, &ExportFilter{Domain: "ghost", Key: &key}); !isRuleCode(err, "snapshot_invalid_filter") {
		t.Fatalf("projection mismatch error = %v", err)
	}
}

func exportFixtureEntries(t *testing.T) []Entry {
	t.Helper()
	payloads := []struct{ key, raw string }{{"z", `{"v":2}`}, {"a", `{"raw":"<>& "}`}}
	entries := make([]Entry, 0, len(payloads))
	for _, fixture := range payloads {
		raw := json.RawMessage(fixture.raw)
		if err := ValidateCanonical(raw); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, Entry{Domain: "knowledge", Key: fixture.key, Payload: raw, PayloadDigest: EntryDigest(raw), PayloadSize: int64(len(raw))})
	}
	return entries
}

func exportFixtureHead(entries []Entry) Head {
	summaries := make([]EntrySummary, len(entries))
	var total int64
	for i, entry := range entries {
		summaries[i] = EntrySummary{Domain: entry.Domain, Key: entry.Key, PayloadDigest: entry.PayloadDigest, PayloadSize: entry.PayloadSize}
		total += entry.PayloadSize
	}
	return Head{ID: 77, Project: "p", Name: "n", SchemaVersion: 1, State: "sealed", ContentDigest: ContentDigest(1, summaries), GitObjectFormat: "sha1", GitCommit: strings.Repeat("a", 40), GitDirty: false, GitMetadataSource: "server", ActorID: "person:1", CreatedAt: "2026-08-29T00:00:00Z", EntryCount: int64(len(entries)), PayloadBytesTotal: total}
}

func isRuleCode(err error, code string) bool {
	rule, ok := err.(*RuleError)
	return ok && rule.Code == code
}
