package storebench

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReportJSONUsesStableFieldsAndIntegerDurations(t *testing.T) {
	report := Report{RunID: "one", Backend: "sqlite_current", DurationNS: 123,
		Config:      RunConfig{Preset: "medium", Repetition: 3},
		Environment: RunEnvironment{Host: "mainex", GoVersion: "go1.test", GitCommit: "abc123"},
		BackendStats: BackendStats{Engine: "sqlite", EngineVersion: "3.test",
			QueueConfig: &QueueConfig{MaxOperations: 12, MaxBytes: 34, MaxBatch: 5, GatherWindow: 6, ReadConnections: 7}},
		Kinds: map[Kind]KindReport{GhostPut: {Operations: 2, Errors: 0,
			Execution: DurationSummary{Count: 2, P50NS: 10, P95NS: 20, P99NS: 20}}}}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, field := range []string{
		`"run_id":"one"`, `"duration_ns":123`, `"p95_ns":20`, `"ghost_put"`,
		`"preset":"medium"`, `"repetition":3`, `"host":"mainex"`, `"go_version":"go1.test"`,
		`"git_commit":"abc123"`, `"engine":"sqlite"`, `"engine_version":"3.test"`,
		`"max_operations":12`, `"max_bytes":34`, `"max_batch":5`, `"gather_window_ns":6`, `"read_connections":7`,
	} {
		if !strings.Contains(text, field) {
			t.Fatalf("report JSON %s lacks %s", text, field)
		}
	}
	if strings.Contains(text, `"queue"`) || !strings.Contains(text, `"scheduler_wait"`) {
		t.Fatalf("report JSON must distinguish scheduler wait from writer queue wait: %s", text)
	}
}
