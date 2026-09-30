package storebench

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestRuntimeSQLiteUsesProductionWriterAndVerifiesWorkload(t *testing.T) {
	backend, err := OpenRuntimeSQLite(filepath.Join(t.TempDir(), "runtime.db"), store.DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	workload, expected := Generate(23, Scale{Projects: 1, Sessions: 8, ChunksPerSession: 8, GhostFiles: 50, GhostBodyBytes: 512, Documents: 5, DocumentRevisions: 2, DocumentBodyBytes: 4096, MigrationArtifacts: 20, ReadEvery: 10})
	report, err := Run(context.Background(), backend, workload, expected, RunConfig{Concurrency: 8, ArrivalMultiplier: 10})
	if err != nil || report.Errors != 0 || report.VerificationError != "" {
		t.Fatalf("runtime workload: %+v %v", report, err)
	}
	stats := backend.(*runtimeSQLite).store.RuntimeStats()
	if backend.Name() != "sqlite_runtime" || !stats.Writer.Enabled || stats.Writer.Admitted == 0 || stats.Writer.Completed != stats.Writer.Admitted || stats.Writer.Failed != 0 || stats.Writer.Config != store.DefaultWriterConfig() || stats.Reader.MaxOpenConnections != 3 {
		t.Fatalf("adapter bypassed production writer: %+v", stats)
	}
	if backend.Stats().MaxOpenConnections != 4 {
		t.Fatal("runtime reader/writer connection accounting missing")
	}
}
