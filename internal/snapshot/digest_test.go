package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
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
	head := digestFixtureHead()

	left, err := ContentDigest(head, []EntrySummary{first, second})
	if err != nil {
		t.Fatal(err)
	}
	right, err := ContentDigest(head, []EntrySummary{second, first})
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("digest depends on insertion order: %x != %x", left, right)
	}
	if got, want := hex.EncodeToString(left[:]), "ef3d1b22e04f26dcbf2b19c88a3e3037c359f2d6dc226a4cd70db49e6dbe9422"; got != want {
		t.Fatalf("digest = %s, want %s", got, want)
	}

	changed := second
	changed.PayloadDigest = EntryDigest([]byte(`{"x":3}`))
	changedDigest, err := ContentDigest(head, []EntrySummary{first, changed})
	if err != nil {
		t.Fatal(err)
	}
	if left == changedDigest {
		t.Fatal("payload digest change did not change content digest")
	}
	head.SchemaVersion++
	changedDigest, err = ContentDigest(head, []EntrySummary{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if left == changedDigest {
		t.Fatal("schema version change did not change content digest")
	}
}

func TestContentDigestDoesNotMutateCallerOrder(t *testing.T) {
	entries := []EntrySummary{{Domain: "z", Key: "2"}, {Domain: "a", Key: "1"}}
	if _, err := ContentDigest(digestFixtureHead(), entries); err != nil {
		t.Fatal(err)
	}
	if entries[0].Domain != "z" || entries[1].Domain != "a" {
		t.Fatalf("caller slice reordered: %#v", entries)
	}
}

func digestFixtureHead() DigestHead {
	return DigestHead{
		Project:       "github.com/example/project",
		Name:          "release-candidate",
		SchemaVersion: SchemaVersion,
		Git: GitProvenance{
			ObjectFormat:   "sha1",
			Commit:         strings.Repeat("a", 40),
			MetadataSource: "server-verified",
		},
		ActorID:   "person:1",
		CreatedAt: "2026-08-31T00:00:00Z",
	}
}

func TestContentDigestBindsHead(t *testing.T) {
	head := digestFixtureHead()
	entries := []EntrySummary{{Domain: "knowledge", Key: "42", PayloadDigest: EntryDigest([]byte(`{"x":1}`)), PayloadSize: 7}}
	base, err := ContentDigest(head, entries)
	if err != nil {
		t.Fatal(err)
	}
	changed := head
	changed.Git.Commit = strings.Repeat("b", 40)
	got, err := ContentDigest(changed, entries)
	if err != nil {
		t.Fatal(err)
	}
	if got == base {
		t.Fatal("git commit is not bound into content digest")
	}
	text := "value"
	ref := "refs/heads/main"
	branch := "main"
	fingerprintVersion := WorktreeFingerprintVersion
	fingerprint := EntryDigest([]byte("worktree"))
	cases := map[string]func(*DigestHead){
		"project":         func(h *DigestHead) { h.Project = "github.com/example/other" },
		"name":            func(h *DigestHead) { h.Name = "other" },
		"schema":          func(h *DigestHead) { h.SchemaVersion++ },
		"object format":   func(h *DigestHead) { h.Git.ObjectFormat = "sha256" },
		"ref":             func(h *DigestHead) { h.Git.Ref = &ref },
		"branch":          func(h *DigestHead) { h.Git.Branch = &branch },
		"dirty":           func(h *DigestHead) { h.Git.Dirty = true },
		"fingerprint ver": func(h *DigestHead) { h.Git.WorktreeFingerprintVersion = &fingerprintVersion },
		"fingerprint":     func(h *DigestHead) { h.Git.WorktreeFingerprint = &fingerprint },
		"allow dirty":     func(h *DigestHead) { h.Git.AllowDirtyUsed = true },
		"metadata source": func(h *DigestHead) { h.Git.MetadataSource = "client-reported" },
		"message":         func(h *DigestHead) { h.Message = &text },
		"actor id":        func(h *DigestHead) { h.ActorID = "person:2" },
		"actor label":     func(h *DigestHead) { h.ActorLabel = &text },
		"session ref":     func(h *DigestHead) { h.SessionRef = &text },
		"created at":      func(h *DigestHead) { h.CreatedAt = "2026-08-31T00:00:01Z" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := head
			mutate(&changed)
			got, err := ContentDigest(changed, entries)
			if err != nil {
				t.Fatal(err)
			}
			if got == base {
				t.Fatalf("%s is not bound into content digest", name)
			}
		})
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
