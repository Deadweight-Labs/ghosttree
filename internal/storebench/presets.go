package storebench

import "fmt"

func Preset(name string) (Scale, error) {
	switch name {
	case "production-sample":
		return Scale{Projects: 1, Sessions: 4, ChunksPerSession: 20, ChunkBodyBytes: 512,
			GhostFiles: 50, GhostBodyBytes: 2 << 10, Documents: 8, DocumentRevisions: 3,
			DocumentBodyBytes: 64 << 10, MigrationArtifacts: 50, ReadEvery: 10}, nil
	case "small":
		return Scale{Projects: 1, Sessions: 8, ChunksPerSession: 8, ChunkBodyBytes: 256,
			GhostFiles: 50, GhostBodyBytes: 512, Documents: 5, DocumentRevisions: 2,
			DocumentBodyBytes: 4 << 10, MigrationArtifacts: 20, ReadEvery: 10}, nil
	case "medium":
		return Scale{Projects: 2, Sessions: 100, ChunksPerSession: 50, ChunkBodyBytes: 512,
			GhostFiles: 2_000, GhostBodyBytes: 2 << 10, Documents: 200, DocumentRevisions: 3,
			DocumentBodyBytes: 64 << 10, MigrationArtifacts: 2_000, ReadEvery: 20}, nil
	case "monorepo":
		return Scale{Projects: 1, Sessions: 1_000, ChunksPerSession: 100, ChunkBodyBytes: 1 << 10,
			GhostFiles: 200_000, GhostBodyBytes: 1 << 10, Documents: 1_000, DocumentRevisions: 3,
			DocumentBodyBytes: 300_000, MigrationArtifacts: 100_000, ReadEvery: 100}, nil
	default:
		return Scale{}, fmt.Errorf("unknown preset %q", name)
	}
}

func LogicalPayloadBytes(scale Scale) int64 {
	return int64(scale.Projects) * (int64(scale.Sessions)*int64(scale.ChunksPerSession)*int64(scale.ChunkBodyBytes) +
		int64(scale.GhostFiles)*int64(scale.GhostBodyBytes) +
		int64(scale.Documents)*int64(scale.DocumentRevisions)*int64(scale.DocumentBodyBytes) +
		int64(scale.MigrationArtifacts)*64)
}
