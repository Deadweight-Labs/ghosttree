package storebench

import "testing"

func TestCompareArtifactsIgnoresMapIterationButNotContent(t *testing.T) {
	want := []Artifact{{Path: "a.md", Digest: "a"}, {Path: "b.md", Digest: "b"}}
	got := map[string]string{"b.md": "b", "a.md": "a"}
	if err := compareArtifacts("migration-1", got, want); err != nil {
		t.Fatal(err)
	}
	got["b.md"] = "changed"
	if err := compareArtifacts("migration-1", got, want); err == nil {
		t.Fatal("digest mismatch passed")
	}
}
