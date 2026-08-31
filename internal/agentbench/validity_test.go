package agentbench

import "testing"

func TestSuspectTasksFlagsUnanimousZero(t *testing.T) {
	arms := []ArmName{ArmBare, ArmClaudeMD, ArmGhosttree}
	records := []RunRecord{
		{TaskID: "np-03", Arm: ArmBare, Score: Score{FactRecall: 0}},
		{TaskID: "np-03", Arm: ArmClaudeMD, Score: Score{FactRecall: 0}},
		{TaskID: "np-03", Arm: ArmGhosttree, Score: Score{FactRecall: 0}},
		{TaskID: "np-01", Arm: ArmBare, Score: Score{FactRecall: 1}},
		{TaskID: "np-01", Arm: ArmClaudeMD, Score: Score{FactRecall: 1}},
		{TaskID: "np-01", Arm: ArmGhosttree, Score: Score{FactRecall: 1}},
	}

	suspect := SuspectTasks(records, arms)

	if len(suspect) != 1 || suspect[0].TaskID != "np-03" {
		t.Fatalf("a task every arm scored zero on must be flagged: %+v", suspect)
	}
}

func TestSuspectTasksIgnoresATaskOneArmSolved(t *testing.T) {
	arms := []ArmName{ArmBare, ArmGhosttree}
	records := []RunRecord{
		{TaskID: "np-05", Arm: ArmBare, Score: Score{FactRecall: 0}},
		{TaskID: "np-05", Arm: ArmGhosttree, Score: Score{FactRecall: 0.6}},
	}

	if suspect := SuspectTasks(records, arms); len(suspect) != 0 {
		// Genau das ist ein echter Effekt und darf nicht als kaputte Aufgabe
		// aussortiert werden.
		t.Fatalf("a task one arm solved is a result, not a defect: %+v", suspect)
	}
}

func TestSuspectTasksNeedsEveryArmToHaveRun(t *testing.T) {
	arms := []ArmName{ArmBare, ArmClaudeMD, ArmGhosttree}
	records := []RunRecord{
		{TaskID: "np-09", Arm: ArmBare, Score: Score{FactRecall: 0}},
		{TaskID: "np-09", Arm: ArmGhosttree, Score: Score{FactRecall: 0}},
	}

	if suspect := SuspectTasks(records, arms); len(suspect) != 0 {
		t.Fatalf("unanimity is no argument when an arm did not run: %+v", suspect)
	}
}

func TestSuspectTasksIgnoresFailedRuns(t *testing.T) {
	arms := []ArmName{ArmBare, ArmGhosttree}
	records := []RunRecord{
		{TaskID: "np-09", Arm: ArmBare, Failure: FailureProduct},
		{TaskID: "np-09", Arm: ArmGhosttree, Score: Score{FactRecall: 0}},
	}

	if suspect := SuspectTasks(records, arms); len(suspect) != 0 {
		t.Fatalf("a crashed run is not evidence about the ground truth: %+v", suspect)
	}
}
