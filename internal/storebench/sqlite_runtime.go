package storebench

import "github.com/Deadweight-Labs/ghosttree/internal/store"

type runtimeSQLite struct {
	*currentSQLite
}

func OpenRuntimeSQLite(path string, config store.WriterConfig) (Backend, error) {
	st, err := store.OpenRuntime(path, config)
	if err != nil {
		return nil, err
	}
	return &runtimeSQLite{&currentSQLite{store: st, sessions: map[string]int64{}, documents: map[string]int64{}, migrations: map[string]int64{}}}, nil
}

func (b *runtimeSQLite) Name() string { return "sqlite_runtime" }

func (b *runtimeSQLite) Stats() BackendStats {
	stats := b.currentSQLite.Stats()
	runtime := b.store.RuntimeStats()
	w := runtime.Writer
	stats.QueueConfig = &QueueConfig{MaxOperations: w.Config.MaxOperations, MaxBytes: w.Config.MaxBytes, MaxBatch: w.Config.MaxBatch, ReadConnections: w.Config.ReadConnections}
	stats.MaxOpenConnections += runtime.Reader.MaxOpenConnections
	stats.WaitCount += runtime.Reader.WaitCount
	stats.WaitDurationNS += runtime.Reader.WaitDuration.Nanoseconds()
	stats.Batches = int64(w.Batches)
	stats.BatchedOperations = int64(w.BatchedOperations)
	stats.WriterOutstandingMax = w.OperationsHighWater
	stats.WriterOutstandingBytesMax = w.BytesHighWater
	stats.RuntimeWriter = &w
	return stats
}
