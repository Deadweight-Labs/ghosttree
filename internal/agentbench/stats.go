package agentbench

import (
	"math"
	"math/rand/v2"
	"sort"
)

type PairedEffect struct {
	Treatment ArmName `json:"treatment"`
	Control   ArmName `json:"control"`
	Tasks     int     `json:"tasks"`
	Mean      float64 `json:"mean"`
	LowerCI   float64 `json:"lower_ci"`
	UpperCI   float64 `json:"upper_ci"`
	Draws     int     `json:"draws"`
}

func PairedBootstrap(records []RunRecord, treatment, control ArmName, seed uint64, draws int) PairedEffect {
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
		perTask[record.TaskID][record.Arm] = append(perTask[record.TaskID][record.Arm], record.Score.FactRecall)
	}

	var ids []string
	for id, arms := range perTask {
		if len(arms[treatment]) > 0 && len(arms[control]) > 0 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	effect := PairedEffect{Treatment: treatment, Control: control, Tasks: len(ids), Draws: draws}
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
