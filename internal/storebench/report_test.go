package storebench

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReportJSONUsesStableFieldsAndIntegerDurations(t *testing.T) {
	report := Report{RunID: "one", Backend: "sqlite_current", DurationNS: 123,
		Kinds: map[Kind]KindReport{GhostPut: {Operations: 2, Errors: 0,
			Execution: DurationSummary{Count: 2, P50NS: 10, P95NS: 20, P99NS: 20}}}}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, field := range []string{`"run_id":"one"`, `"duration_ns":123`, `"p95_ns":20`, `"ghost_put"`} {
		if !strings.Contains(text, field) {
			t.Fatalf("report JSON %s lacks %s", text, field)
		}
	}
}
