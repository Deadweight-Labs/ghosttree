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
	Engine                    string            `json:"engine"`
	EngineVersion             string            `json:"engine_version"`
	Settings                  map[string]string `json:"settings,omitempty"`
	QueueConfig               *QueueConfig      `json:"queue_config,omitempty"`
	MaxOpenConnections        int               `json:"max_open_connections"`
	WaitCount                 int64             `json:"wait_count"`
	WaitDurationNS            int64             `json:"wait_duration_ns"`
	DatabaseBytes             int64             `json:"database_bytes"`
	WALBytes                  int64             `json:"wal_bytes"`
	SHMBytes                  int64             `json:"shm_bytes"`
	Batches                   int64             `json:"batches"`
	BatchedOperations         int64             `json:"batched_operations"`
	WriterQueueWait           DurationSummary   `json:"writer_queue_wait"`
	WriterBatchSize           ValueSummary      `json:"writer_batch_size"`
	WriterQueueDepthMax       int               `json:"writer_queue_depth_max"`
	WriterQueueBytesMax       int64             `json:"writer_queue_bytes_max"`
	WriterOutstandingMax      int               `json:"writer_outstanding_max"`
	WriterOutstandingBytesMax int64             `json:"writer_outstanding_bytes_max"`
}
