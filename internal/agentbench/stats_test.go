package agentbench

import (
	"fmt"
	"math"
	"testing"
)

func pairedRecords(taskIDs []string, treatment, control float64) []RunRecord {
	var records []RunRecord
	for _, id := range taskIDs {
		for rep := 1; rep <= 3; rep++ {
			records = append(records,
				RunRecord{TaskID: id, Repo: "r", Arm: ArmGhosttree, Repetition: rep,
					Score: Score{FactRecall: treatment}},
				RunRecord{TaskID: id, Repo: "r", Arm: ArmClaudeNative, Repetition: rep,
					Score: Score{FactRecall: control}})
		}
	}
	return records
}

func TestPairedBootstrapClustersByTask(t *testing.T) {
	records := pairedRecords([]string{"t1", "t2", "t3", "t4"}, 0.8, 0.5)

	effect := PairedBootstrap(records, ArmGhosttree, ArmClaudeNative, 42, 2000)

	if effect.Tasks != 4 {
		t.Fatalf("want 4 clustered tasks, got %d", effect.Tasks)
	}
	if math.Abs(effect.Mean-0.3) > 1e-9 {
		t.Fatalf("want a mean effect of 0.3, got %v", effect.Mean)
	}
	if effect.LowerCI > effect.Mean || effect.UpperCI < effect.Mean {
		t.Fatalf("interval must contain the point estimate: %+v", effect)
	}
}

func TestPairedBootstrapIsDeterministicForASeed(t *testing.T) {
	records := pairedRecords([]string{"t1", "t2", "t3"}, 0.9, 0.4)
	first := PairedBootstrap(records, ArmGhosttree, ArmClaudeNative, 7, 500)
	again := PairedBootstrap(records, ArmGhosttree, ArmClaudeNative, 7, 500)
	if first != again {
		t.Fatalf("same seed must give the same interval: %+v vs %+v", first, again)
	}
}

func TestPairedBootstrapDropsTasksMissingAnArm(t *testing.T) {
	records := pairedRecords([]string{"t1", "t2"}, 0.8, 0.5)
	// t3 lief nur im Behandlungsarm — ohne Gegenstueck ist es kein Paar.
	records = append(records, RunRecord{TaskID: "t3", Arm: ArmGhosttree, Score: Score{FactRecall: 1}})

	effect := PairedBootstrap(records, ArmGhosttree, ArmClaudeNative, 1, 200)

	if effect.Tasks != 2 {
		t.Fatalf("an unpaired task must not enter the estimate, got %d tasks", effect.Tasks)
	}
}

func TestPairedBootstrapIgnoresFailedRuns(t *testing.T) {
	records := pairedRecords([]string{"t1", "t2"}, 0.8, 0.5)
	records = append(records, RunRecord{
		TaskID: "t1", Arm: ArmClaudeNative, Failure: FailureProduct,
		Score: Score{FactRecall: 0},
	})

	effect := PairedBootstrap(records, ArmGhosttree, ArmClaudeNative, 1, 200)

	if math.Abs(effect.Mean-0.3) > 1e-9 {
		t.Fatalf("a failed run must not be scored as a zero, got %v", effect.Mean)
	}
}

func TestPairedBootstrapHandlesNoPairs(t *testing.T) {
	effect := PairedBootstrap(nil, ArmGhosttree, ArmClaudeNative, 1, 100)
	if effect.Tasks != 0 || effect.Mean != 0 {
		t.Fatalf("an empty set must not produce an effect: %+v", effect)
	}
}

func TestPairedBootstrapWidensWithVariance(t *testing.T) {
	tight := PairedBootstrap(pairedRecords([]string{"t1", "t2", "t3", "t4"}, 0.8, 0.5),
		ArmGhosttree, ArmClaudeNative, 3, 2000)

	var noisy []RunRecord
	deltas := []float64{0.9, 0.1, 0.8, 0.0}
	for i, id := range []string{"t1", "t2", "t3", "t4"} {
		noisy = append(noisy,
			RunRecord{TaskID: id, Arm: ArmGhosttree, Score: Score{FactRecall: deltas[i]}},
			RunRecord{TaskID: id, Arm: ArmClaudeNative, Score: Score{FactRecall: 0}})
	}
	wide := PairedBootstrap(noisy, ArmGhosttree, ArmClaudeNative, 3, 2000)

	if (wide.UpperCI - wide.LowerCI) <= (tight.UpperCI - tight.LowerCI) {
		t.Fatalf("a noisier task set must widen the interval: tight=%+v wide=%+v", tight, wide)
	}
}

func TestPairedBootstrapMetricMeasuresEffortToo(t *testing.T) {
	// Beide Arme antworten gleich gut; der Unterschied liegt allein im
	// Aufwand. Eine Auswertung, die nur die Trefferquote kennt, sieht hier
	// nichts — und uebersieht genau das Ergebnis.
	records := []RunRecord{
		{TaskID: "t1", Arm: ArmGhosttree, Score: Score{FactRecall: 1},
			Transcript: Transcript{Turns: 3, CostUSD: 0.05}},
		{TaskID: "t1", Arm: ArmBare, Score: Score{FactRecall: 1},
			Transcript: Transcript{Turns: 11, CostUSD: 0.17}},
		{TaskID: "t2", Arm: ArmGhosttree, Score: Score{FactRecall: 1},
			Transcript: Transcript{Turns: 4, CostUSD: 0.06}},
		{TaskID: "t2", Arm: ArmBare, Score: Score{FactRecall: 1},
			Transcript: Transcript{Turns: 10, CostUSD: 0.15}},
	}

	recall := PairedBootstrapMetric(records, ArmGhosttree, ArmBare, MetricFactRecall, 1, 500)
	if recall.Mean != 0 {
		t.Fatalf("recall is identical here, want 0, got %.3f", recall.Mean)
	}

	turns := PairedBootstrapMetric(records, ArmGhosttree, ArmBare, MetricTurns, 1, 500)
	if turns.Mean >= 0 {
		t.Fatalf("ghosttree needed fewer turns, so the effect must be negative: %.3f", turns.Mean)
	}
	if turns.Metric != "turns" {
		t.Fatalf("the effect must name its metric, got %q", turns.Metric)
	}
	cost := PairedBootstrapMetric(records, ArmGhosttree, ArmBare, MetricCostUSD, 1, 500)
	if cost.Mean >= 0 {
		t.Fatalf("ghosttree was cheaper, so the effect must be negative: %.3f", cost.Mean)
	}
}

// Die Nullprobe des Verfahrens: Ein Arm gegen sich selbst muss exakt null
// ergeben, mit einem Intervall der Breite null. Faellt das anders aus, ist
// jedes andere Intervall in diesem Bericht ebenfalls falsch, und keine noch so
// sorgfaeltige Aufgabe rettet es.
func TestPairedBootstrapOfAnArmAgainstItselfIsExactlyZero(t *testing.T) {
	var records []RunRecord
	for i, recall := range []float64{0.1, 0.9, 0.4, 1.0, 0.0, 0.55} {
		records = append(records, RunRecord{
			TaskID: fmt.Sprintf("t%d", i), Arm: ArmGhosttree, Repetition: 1,
			Score: Score{FactRecall: recall}, Transcript: Transcript{Turns: i + 1},
		})
	}

	for _, metric := range []Metric{MetricFactRecall, MetricTurns} {
		effect := PairedBootstrapMetric(records, ArmGhosttree, ArmGhosttree, metric, 7, 2000)
		if effect.Mean != 0 || effect.LowerCI != 0 || effect.UpperCI != 0 {
			t.Fatalf("%s gegen sich selbst: want 0 [0,0], got %.6f [%.6f, %.6f]",
				metric.Name, effect.Mean, effect.LowerCI, effect.UpperCI)
		}
	}
}

// Ein konstanter Abstand auf jeder Aufgabe hat keine Streuung: Der Bootstrap
// muss ihn exakt treffen und darf kein Intervall darum erfinden.
func TestPairedBootstrapOfAConstantShiftHasNoInterval(t *testing.T) {
	var records []RunRecord
	for i, recall := range []float64{0.1, 0.9, 0.4, 0.0} {
		id := fmt.Sprintf("t%d", i)
		records = append(records,
			RunRecord{TaskID: id, Arm: ArmBare, Repetition: 1, Score: Score{FactRecall: recall}},
			RunRecord{TaskID: id, Arm: ArmGhosttree, Repetition: 1, Score: Score{FactRecall: recall + 0.25}})
	}

	effect := PairedBootstrapMetric(records, ArmGhosttree, ArmBare, MetricFactRecall, 7, 2000)

	if math.Abs(effect.Mean-0.25) > 1e-9 || math.Abs(effect.UpperCI-effect.LowerCI) > 1e-9 {
		t.Fatalf("konstanter Abstand: want 0.25 ohne Intervall, got %.9f [%.9f, %.9f]",
			effect.Mean, effect.LowerCI, effect.UpperCI)
	}
}
