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
}

var (
	MetricFactRecall = Metric{"fact_recall", func(r RunRecord) float64 { return r.Score.FactRecall }}
	MetricPrecision  = Metric{"claim_precision", func(r RunRecord) float64 { return r.Score.ClaimPrecision }}
	MetricCostUSD    = Metric{"cost_usd", func(r RunRecord) float64 { return r.Transcript.CostUSD }}
	MetricTurns      = Metric{"turns", func(r RunRecord) float64 { return float64(r.Transcript.Turns) }}
	MetricToolCalls  = Metric{"tool_calls", func(r RunRecord) float64 { return float64(r.Transcript.ToolCalls) }}
)

// DefaultMetrics are reported for every contrast. Cost and turns are signed the
// same way as recall — a negative value means the treatment needed less.
var DefaultMetrics = []Metric{MetricFactRecall, MetricPrecision, MetricTurns, MetricCostUSD}

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
