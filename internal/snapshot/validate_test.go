package snapshot

import "testing"

func TestSnapshotCountsSupportV3Only(t *testing.T) {
	counts, err := NewCounts(SchemaVersion)
	if err != nil {
		t.Fatalf("NewCounts(%d): %v", SchemaVersion, err)
	}
	if len(counts) != 5 {
		t.Fatalf("NewCounts(%d)=%v", SchemaVersion, counts)
	}
	for _, unsupported := range []uint32{1, 2, SchemaVersion + 1} {
		if _, err := NewCounts(unsupported); snapshotRuleCode(err) != "unsupported_snapshot_schema" {
			t.Fatalf("NewCounts(%d) error=%v", unsupported, err)
		}
	}
}

func snapshotRuleCode(err error) string {
	if rule, ok := err.(*RuleError); ok {
		return rule.Code
	}
	return ""
}
