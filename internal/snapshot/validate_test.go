package snapshot

import "testing"

func TestSnapshotCountsSupportV1AndV2Only(t *testing.T) {
	for _, version := range []uint32{SchemaVersionV1, SchemaVersionV2} {
		counts, err := NewCounts(version)
		if err != nil {
			t.Fatalf("NewCounts(%d): %v", version, err)
		}
		if len(counts) != 5 {
			t.Fatalf("NewCounts(%d)=%v", version, counts)
		}
	}
	if _, err := NewCounts(SchemaVersionV2 + 1); snapshotRuleCode(err) != "unsupported_snapshot_schema" {
		t.Fatalf("future schema error=%v", err)
	}
}

func snapshotRuleCode(err error) string {
	if rule, ok := err.(*RuleError); ok {
		return rule.Code
	}
	return ""
}
