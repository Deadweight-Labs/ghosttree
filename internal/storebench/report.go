package storebench

type RunConfig struct {
	Concurrency       int     `json:"concurrency"`
	ArrivalMultiplier float64 `json:"arrival_multiplier"`
	RunID             string  `json:"run_id"`
	Preset            string  `json:"preset"`
	Repetition        int     `json:"repetition"`
}

type RunEnvironment struct {
	Host        string `json:"host"`
	GoVersion   string `json:"go_version"`
	GitCommit   string `json:"git_commit"`
	GitModified bool   `json:"git_modified"`
}

type DurationSummary struct {
	Count int   `json:"count"`
	P50NS int64 `json:"p50_ns"`
	P95NS int64 `json:"p95_ns"`
	P99NS int64 `json:"p99_ns"`
	MaxNS int64 `json:"max_ns"`
}

type ValueSummary struct {
	Count int   `json:"count"`
	P50   int64 `json:"p50"`
	P95   int64 `json:"p95"`
	P99   int64 `json:"p99"`
	Max   int64 `json:"max"`
}

type KindReport struct {
	Operations    int             `json:"operations"`
	Errors        int             `json:"errors"`
	SchedulerWait DurationSummary `json:"scheduler_wait"`
	Execution     DurationSummary `json:"execution"`
	Total         DurationSummary `json:"total"`
}

type Report struct {
	RunID             string              `json:"run_id"`
	Backend           string              `json:"backend"`
	Seed              uint64              `json:"seed"`
	Scale             Scale               `json:"scale"`
	Config            RunConfig           `json:"config"`
	Environment       RunEnvironment      `json:"environment"`
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
