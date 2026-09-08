package storebench

import (
	"path/filepath"
	"testing"
)

func TestGenerateBuildsValidDependenciesAndPayloadSizes(t *testing.T) {
	workload, _ := Generate(99, Scale{
		Projects: 1, Sessions: 3, ChunksPerSession: 2,
		GhostFiles: 5, GhostBodyBytes: 80,
		Documents: 2, DocumentRevisions: 3, DocumentBodyBytes: 160,
		MigrationArtifacts: 4, ReadEvery: 2,
	})
	seen := map[string]bool{}
	for _, operation := range workload.Operations {
		if operation.ID == "" || seen[operation.ID] {
			t.Fatalf("operation id %q is empty or duplicated", operation.ID)
		}
		for _, dependency := range operation.DependsOn {
			if !seen[dependency] {
				t.Fatalf("operation %q depends on later or missing %q", operation.ID, dependency)
			}
		}
		switch payload := operation.Payload.(type) {
		case GhostPutPayload:
			if filepath.IsAbs(payload.Path) || payload.Path == ".." || filepath.Dir(payload.Path) == ".." {
				t.Fatalf("unsafe ghost path %q", payload.Path)
			}
			if operation.PayloadBytes != int64(len(payload.Description)) {
				t.Fatalf("ghost payload bytes = %d, want %d", operation.PayloadBytes, len(payload.Description))
			}
		case DocumentCreatePayload:
			if operation.PayloadBytes != int64(len(payload.Body)) {
				t.Fatalf("document payload bytes = %d, want %d", operation.PayloadBytes, len(payload.Body))
			}
		case DocumentPushPayload:
			if operation.PayloadBytes != int64(len(payload.Body)) {
				t.Fatalf("revision payload bytes = %d, want %d", operation.PayloadBytes, len(payload.Body))
			}
		case ChunksAppendPayload:
			want := int64(0)
			for _, chunk := range payload.Chunks {
				want += int64(len(chunk.Text) + len(chunk.Raw))
			}
			if operation.PayloadBytes != want {
				t.Fatalf("chunk payload bytes = %d, want %d", operation.PayloadBytes, want)
			}
		}
		seen[operation.ID] = true
	}
}

func TestGenerateRejectsInvalidScale(t *testing.T) {
	for _, scale := range []Scale{
		{},
		{Projects: -1},
		{Projects: 1, GhostFiles: 1, GhostBodyBytes: -1},
		{Projects: 1, Documents: 1, DocumentBodyBytes: -1},
	} {
		if _, _, err := GenerateChecked(1, scale); err == nil {
			t.Fatalf("GenerateChecked accepted %+v", scale)
		}
	}
}
