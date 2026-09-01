package agentbench

import (
	"strings"
	"testing"
)

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

// TestSuspectTasksFindsArmsThatAgreeOnARejectedAnswer is the np-08 case: the
// question asked which function keeps two hosts' adopted configuration from
// colliding, and the repository has two true answers. One arm gave the one the
// key listed, two gave the other. Scored as written that reads as "memory made
// the agent worse".
func TestSuspectTasksFindsArmsThatAgreeOnARejectedAnswer(t *testing.T) {
	arms := []ArmName{ArmBare, ArmClaudeMD, ArmGhosttree}
	rejected := Score{FactRecall: 0, Rejected: []RejectedClaim{
		{Slot: "func_name", Value: "GetConfigArtifactByTarget"},
	}}
	records := []RunRecord{
		{TaskID: "np-08", Arm: ArmBare, Score: Score{FactRecall: 1}},
		{TaskID: "np-08", Arm: ArmClaudeMD, Score: rejected},
		{TaskID: "np-08", Arm: ArmGhosttree, Score: rejected},
	}
	found := SuspectTasks(records, arms)
	if len(found) != 1 || found[0].TaskID != "np-08" {
		t.Fatalf("a task two arms answered the same rejected way must be flagged: %+v", found)
	}
	if !strings.Contains(found[0].Reason, "GetConfigArtifactByTarget") {
		t.Fatalf("the reason must name what they said: %q", found[0].Reason)
	}
}

// TestSuspectTasksIgnoresASingleArmsRejectedAnswer keeps the rule from firing
// on ordinary wrong answers. One arm being wrong is the normal outcome of a
// benchmark and says nothing about the question.
func TestSuspectTasksIgnoresASingleArmsRejectedAnswer(t *testing.T) {
	arms := []ArmName{ArmBare, ArmClaudeMD, ArmGhosttree}
	records := []RunRecord{
		{TaskID: "np-09", Arm: ArmBare, Score: Score{FactRecall: 1}},
		{TaskID: "np-09", Arm: ArmClaudeMD, Score: Score{FactRecall: 1}},
		{TaskID: "np-09", Arm: ArmGhosttree, Score: Score{FactRecall: 0,
			Rejected: []RejectedClaim{{Slot: "f", Value: "irgendwas"}}}},
	}
	if found := SuspectTasks(records, arms); len(found) != 0 {
		t.Fatalf("one arm being wrong is not a suspicious task: %+v", found)
	}
}
