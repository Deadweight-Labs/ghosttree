package storebench

type RunConfig struct {
	Concurrency       int     `json:"concurrency"`
	ArrivalMultiplier float64 `json:"arrival_multiplier"`
	RunID             string  `json:"run_id"`
}

type DurationSummary struct {
	Count int   `json:"count"`
	P50NS int64 `json:"p50_ns"`
	P95NS int64 `json:"p95_ns"`
	P99NS int64 `json:"p99_ns"`
	MaxNS int64 `json:"max_ns"`
}

type KindReport struct {
	Operations int             `json:"operations"`
	Errors     int             `json:"errors"`
	Queue      DurationSummary `json:"queue"`
	Execution  DurationSummary `json:"execution"`
	Total      DurationSummary `json:"total"`
}

type Report struct {
	RunID             string              `json:"run_id"`
	Backend           string              `json:"backend"`
	Seed              uint64              `json:"seed"`
	Scale             Scale               `json:"scale"`
	Config            RunConfig           `json:"config"`
	StartedAt         string              `json:"started_at"`
	FinishedAt        string              `json:"finished_at"`
	DurationNS        int64               `json:"duration_ns"`
	Throughput        float64             `json:"operations_per_second"`
	Operations        int                 `json:"operations"`
	Errors            int                 `json:"errors"`
	Kinds             map[Kind]KindReport `json:"kinds"`
	BackendStats      BackendStats        `json:"backend_stats"`
	VerificationError string              `json:"verification_error,omitempty"`
}
