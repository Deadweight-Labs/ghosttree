package storebench

import "context"

type Backend interface {
	Name() string
	Execute(context.Context, Operation) error
	Verify(context.Context, Expected) error
	Stats() BackendStats
	Close() error
}

type BackendStats struct {
	MaxOpenConnections int   `json:"max_open_connections"`
	WaitCount          int64 `json:"wait_count"`
	WaitDurationNS     int64 `json:"wait_duration_ns"`
	DatabaseBytes      int64 `json:"database_bytes"`
	WALBytes           int64 `json:"wal_bytes"`
	SHMBytes           int64 `json:"shm_bytes"`
	Batches            int64 `json:"batches"`
	BatchedOperations  int64 `json:"batched_operations"`
}
