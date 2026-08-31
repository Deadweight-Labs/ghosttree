package storebench

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestGenerateIsDeterministicForSeedAndScale(t *testing.T) {
	scale := Scale{
		Projects: 1, Sessions: 3, ChunksPerSession: 4,
		GhostFiles: 8, GhostBodyBytes: 256,
		Documents: 2, DocumentRevisions: 2, DocumentBodyBytes: 1024,
		MigrationArtifacts: 6, ReadEvery: 3,
	}
	left, expectedLeft := Generate(42, scale)
	right, expectedRight := Generate(42, scale)
	leftJSON, err := json.Marshal(left)
	if err != nil {
		t.Fatal(err)
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		t.Fatal(err)
	}
	if string(leftJSON) != string(rightJSON) {
		t.Fatal("same seed and scale produced different workloads")
	}
	if !reflect.DeepEqual(expectedLeft, expectedRight) {
		t.Fatal("same seed and scale produced different expected states")
	}
}

func TestGenerateChangesBodiesButNotCountsForDifferentSeeds(t *testing.T) {
	scale := Scale{Projects: 2, Sessions: 2, ChunksPerSession: 2, GhostFiles: 3,
		GhostBodyBytes: 64, Documents: 2, DocumentRevisions: 1,
		DocumentBodyBytes: 128, MigrationArtifacts: 2, ReadEvery: 2}
	left, expectedLeft := Generate(1, scale)
	right, expectedRight := Generate(2, scale)
	if reflect.DeepEqual(left, right) {
		t.Fatal("different seeds produced identical workloads")
	}
	if len(left.Operations) != len(right.Operations) ||
		len(expectedLeft.Sessions) != len(expectedRight.Sessions) ||
		len(expectedLeft.Ghosts) != len(expectedRight.Ghosts) ||
		len(expectedLeft.Documents) != len(expectedRight.Documents) ||
		len(expectedLeft.Migrations) != len(expectedRight.Migrations) {
		t.Fatal("different seeds changed workload counts")
	}
}
