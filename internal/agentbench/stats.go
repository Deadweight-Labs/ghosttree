package agentbench

import (
	"math"
	"math/rand/v2"
	"sort"
)

type PairedEffect struct {
	Metric    string  `json:"metric"`
	Treatment ArmName `json:"treatment"`
	Control   ArmName `json:"control"`
	Tasks     int     `json:"tasks"`
	Mean      float64 `json:"mean"`
	LowerCI   float64 `json:"lower_ci"`
	UpperCI   float64 `json:"upper_ci"`
	Draws     int     `json:"draws"`
}

// Metric is what a contrast is measured in. Recall alone cannot answer whether
// a memory is worth having: two arms can reach the same answer, and the
// difference lies entirely in how many turns and how much money it took.
type Metric struct {
	Name string
	Of   func(RunRecord) float64
	// Applies excludes runs the metric says nothing about. A task with no
	// memory-only fact has no memory recall — counting it as zero would not be
	// a bad score but a made-up one, and it would drag every contrast on that
	// metric toward the middle. A nil Applies means every run counts.
	Applies func(RunRecord) bool
}

func (m Metric) applies(r RunRecord) bool { return m.Applies == nil || m.Applies(r) }

var (
	MetricFactRecall = Metric{Name: "fact_recall", Of: func(r RunRecord) float64 { return r.Score.FactRecall }}
	MetricPrecision  = Metric{Name: "claim_precision", Of: func(r RunRecord) float64 { return r.Score.ClaimPrecision }}
	MetricCostUSD    = Metric{Name: "cost_usd", Of: func(r RunRecord) float64 { return r.Transcript.CostUSD }}
	MetricTurns      = Metric{Name: "turns", Of: func(r RunRecord) float64 { return float64(r.Transcript.Turns) }}
	MetricToolCalls  = Metric{Name: "tool_calls", Of: func(r RunRecord) float64 { return float64(r.Transcript.ToolCalls) }}

	// MetricRepoRecall ist der ehrliche Wettstreit: die Antwort steht im
	// Repository, jeder Arm koennte sie finden, gemessen wird, ob ein
	// Gedaechtnis die Suche besser fuehrt.
	MetricRepoRecall = Metric{
		Name:    "repo_recall",
		Of:      func(r RunRecord) float64 { return r.Score.RepoRecall },
		Applies: func(r RunRecord) bool { return r.Score.RepoWeight > 0 },
	}
	// MetricMemoryRecall misst die andere Frage: was es wert war, die Sache
	// aufzuschreiben. Ein Arm ohne Gedaechtnis erreicht hier null, und das ist
	// kein Versagen, sondern die Bauart.
	MetricMemoryRecall = Metric{
		Name:    "memory_recall",
		Of:      func(r RunRecord) float64 { return r.Score.MemoryRecall },
		Applies: func(r RunRecord) bool { return r.Score.MemoryWeight > 0 },
	}
)

// DefaultMetrics are reported for every contrast. Cost and turns are signed the
// same way as recall — a negative value means the treatment needed less.
//
// Turns and tool calls are both reported because they disagree, and the
// disagreement is the point. A turn is what Claude Code counts against
// --max-turns; a subagent runs on its own budget, so an agent that delegates
// does an unbounded amount of work inside a single turn. On Robcord-Zentrale at
// budget 6, `bare` delegated in 5 of 12 runs and `ghosttree` in none: measured
// in turns `bare` looked cheaper, measured in tool calls it did twice the work
// (8.58 against 4.17). Turns measure what the budget constrains, tool calls
// measure what the agent actually did — reporting only the first would have
// published a comparison an arm could win by delegating.
var DefaultMetrics = []Metric{
	MetricFactRecall, MetricRepoRecall, MetricMemoryRecall,
	MetricPrecision, MetricTurns, MetricToolCalls, MetricCostUSD,
}

func PairedBootstrap(records []RunRecord, treatment, control ArmName, seed uint64, draws int) PairedEffect {
	return PairedBootstrapMetric(records, treatment, control, MetricFactRecall, seed, draws)
}

func PairedBootstrapMetric(records []RunRecord, treatment, control ArmName, metric Metric, seed uint64, draws int) PairedEffect {
	perTask := map[string]map[ArmName][]float64{}
	for _, record := range records {
		if record.Failure != FailureNone {
			continue
		}
		if record.Arm != treatment && record.Arm != control {
			continue
		}
		if !metric.applies(record) {
			continue
		}
		if perTask[record.TaskID] == nil {
			perTask[record.TaskID] = map[ArmName][]float64{}
		}
		perTask[record.TaskID][record.Arm] = append(perTask[record.TaskID][record.Arm], metric.Of(record))
	}

	var ids []string
	for id, arms := range perTask {
		if len(arms[treatment]) > 0 && len(arms[control]) > 0 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	effect := PairedEffect{Metric: metric.Name, Treatment: treatment, Control: control, Tasks: len(ids), Draws: draws}
	if len(ids) == 0 {
		return effect
	}

	deltas := make([]float64, 0, len(ids))
	for _, id := range ids {
		deltas = append(deltas, mean(perTask[id][treatment])-mean(perTask[id][control]))
	}
	effect.Mean = mean(deltas)

	rng := rand.New(rand.NewPCG(seed, uint64(len(deltas))))
	samples := make([]float64, 0, draws)
	for i := 0; i < draws; i++ {
		total := 0.0
		for range deltas {
			total += deltas[rng.IntN(len(deltas))]
		}
		samples = append(samples, total/float64(len(deltas)))
	}
	sort.Float64s(samples)
	effect.LowerCI = percentile(samples, 0.025)
	effect.UpperCI = percentile(samples, 0.975)
	return effect
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return math.NaN()
	}
	total := 0.0
	for _, v := range values {
		total += v
	}
	return total / float64(len(values))
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}
