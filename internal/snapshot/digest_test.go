package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestEntryDigestUsesExactPayloadBytes(t *testing.T) {
	payload := []byte(`{"a":1}`)
	got := EntryDigest(payload)
	want := sha256.Sum256(payload)
	if got != want {
		t.Fatalf("got %x, want %x", got, want)
	}
	payload[0] = '['
	if got == EntryDigest(payload) {
		t.Fatal("payload byte change did not change digest")
	}
}

func TestContentDigestGoldenAndOrderIndependence(t *testing.T) {
	first := EntrySummary{Domain: "knowledge", Key: "42", PayloadDigest: EntryDigest([]byte(`{"x":1}`)), PayloadSize: 7}
	second := EntrySummary{Domain: "ghost", Key: "file/README.md", PayloadDigest: EntryDigest([]byte(`{"x":2}`)), PayloadSize: 7}

	left := ContentDigest(1, []EntrySummary{first, second})
	right := ContentDigest(1, []EntrySummary{second, first})
	if left != right {
		t.Fatalf("digest depends on insertion order: %x != %x", left, right)
	}
	if got, want := hex.EncodeToString(left[:]), "635f752279968897d79a6a4563e56ae5f2e60f6357507bff42b461551b3f9d34"; got != want {
		t.Fatalf("digest = %s, want %s", got, want)
	}

	changed := second
	changed.PayloadDigest = EntryDigest([]byte(`{"x":3}`))
	if left == ContentDigest(1, []EntrySummary{first, changed}) {
		t.Fatal("payload digest change did not change content digest")
	}
	if left == ContentDigest(2, []EntrySummary{first, second}) {
		t.Fatal("schema version change did not change content digest")
	}
}

func TestContentDigestDoesNotMutateCallerOrder(t *testing.T) {
	entries := []EntrySummary{{Domain: "z", Key: "2"}, {Domain: "a", Key: "1"}}
	ContentDigest(1, entries)
	if entries[0].Domain != "z" || entries[1].Domain != "a" {
		t.Fatalf("caller slice reordered: %#v", entries)
	}
}

func TestLogicalSizeUsesCanonicalHeadAndFramedEntryMaterial(t *testing.T) {
	head := []byte(`{"name":"v1"}`)
	entries := []EntrySummary{{Domain: "ghost", Key: "file/a", PayloadSize: 11}, {Domain: "knowledge", Key: "7", PayloadSize: 5}}
	want := int64(len(head) + len("ghost") + len("file/a") + 32 + 11 + len("knowledge") + len("7") + 32 + 5)
	if got := LogicalSize(head, entries); got != want {
		t.Fatalf("LogicalSize = %d, want %d", got, want)
	}
}
