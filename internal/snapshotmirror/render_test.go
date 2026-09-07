package snapshotmirror

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

func TestRenderIndexSortsAndIncludesStableRecoveryCommands(t *testing.T) {
	mainRef := "refs/heads/main"
	heads := []snapshot.Head{
		{Name: "older", CreatedAt: "2026-08-28T10:00:00Z"},
		{Name: "zeta", CreatedAt: "2026-08-29T10:00:00Z", GitCommit: "bbbb", GitDirty: true,
			ContentDigest: digest(2), Counts: map[string]int64{"request": 2, "knowledge": 1}, PayloadBytesTotal: 99},
		{Name: "alpha", CreatedAt: "2026-08-29T10:00:00Z", GitCommit: "aaaa", GitRef: &mainRef,
			ContentDigest: digest(1), Counts: map[string]int64{"knowledge": 3}, PayloadBytesTotal: 42},
	}

	got := string(RenderIndex(heads))
	if !(strings.Index(got, "## alpha") < strings.Index(got, "## zeta") &&
		strings.Index(got, "## zeta") < strings.Index(got, "## older")) {
		t.Fatalf("wrong order:\n%s", got)
	}
	for _, want := range []string{
		"Created: 2026-08-29T10:00:00Z",
		"Git: `aaaa` (`refs/heads/main`)",
		"Dirty: no",
		"Digest: `0101010101010101010101010101010101010101010101010101010101010101`",
		"Counts: knowledge=3",
		"Payload bytes: 42",
		"ctx snapshot show 'alpha'",
		"ctx snapshot export 'alpha'",
		"ctx snapshot verify 'alpha'",
		"Counts: knowledge=1, request=2",
		"Dirty: yes",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n") || strings.HasSuffix(got, "\n\n") {
		t.Fatalf("index must end in exactly one LF: %q", got[len(got)-min(4, len(got)):])
	}
	if original := heads[0].Name; original != "older" {
		t.Fatalf("RenderIndex mutated input: %q", original)
	}
}

func digest(b byte) snapshot.Digest {
	var d snapshot.Digest
	for i := range d {
		d[i] = b
	}
	return d
}

func TestRenderIndexStatesProvenanceAndLegacyDigestScope(t *testing.T) {
	ref := "refs/tags/v1.2.3"
	legacy := snapshot.Head{Name: "release-v1.2.3", SchemaVersion: 2, GitRef: &ref, GitMetadataSource: "client-reported"}
	got := string(RenderIndex([]snapshot.Head{legacy}))
	for _, want := range []string{"Git source: client-reported", "Schema: 2", "Digest scope: entries only"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	legacy.SchemaVersion = 3
	if got := string(RenderIndex([]snapshot.Head{legacy})); !strings.Contains(got, "Digest scope: metadata and entries") {
		t.Fatal(got)
	}
}
