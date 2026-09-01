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
		{Slot: "func_name", Type: SlotString, Value: "GetConfigArtifactByTarget"},
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
			Rejected: []RejectedClaim{{Slot: "f", Type: SlotString, Value: "irgendwas"}}}},
	}
	if found := SuspectTasks(records, arms); len(found) != 0 {
		t.Fatalf("one arm being wrong is not a suspicious task: %+v", found)
	}
}

// TestSuspectTasksIgnoresTwoArmsAgreeingOnABoolean keeps the convergence rule
// from firing on a coin flip. Asked whether work on the webhook is still open,
// the two arms without a ledger both guessed "false" — with two possible values
// they agree half the time by chance, and the question was fine.
func TestSuspectTasksIgnoresTwoArmsAgreeingOnABoolean(t *testing.T) {
	arms := []ArmName{ArmBare, ArmClaudeMD, ArmGhosttree}
	guessed := Score{FactRecall: 0.4, Rejected: []RejectedClaim{
		{Slot: "still_open", Type: SlotBoolean, Value: "false"},
	}}
	records := []RunRecord{
		{TaskID: "np-05", Arm: ArmBare, Score: guessed},
		{TaskID: "np-05", Arm: ArmClaudeMD, Score: guessed},
		{TaskID: "np-05", Arm: ArmGhosttree, Score: Score{FactRecall: 1}},
	}
	if found := SuspectTasks(records, arms); len(found) != 0 {
		t.Fatalf("agreement on one of two possible values is not evidence: %+v", found)
	}
}

// TestSuspectTasksLeavesTheNegativeControlAlone: there the unanimous zero is the
// point. Every arm abstains because the question has no answer, and reporting
// that as suspicious would print the design working as a defect.
func TestSuspectTasksLeavesTheNegativeControlAlone(t *testing.T) {
	arms := []ArmName{ArmBare, ArmClaudeMD, ArmGhosttree}
	var records []RunRecord
	for _, arm := range arms {
		records = append(records, RunRecord{
			TaskID: "np-07", Arm: arm, Category: CategoryNegative,
			Score: Score{FactRecall: 0, AbstentionRate: 1},
		})
	}
	if found := SuspectTasks(records, arms); len(found) != 0 {
		t.Fatalf("the negative control scoring zero is the design, not a fault: %+v", found)
	}
}

// TestSuspectTasksFindsASlotNobodyGotRight is the z-03 case: asked through
// which route a listed invite is revoked, one arm gave the bare path and two
// the full proxy-prefixed one. All three are true, the key listed one, and no
// arm scored zero — so neither older rule saw anything.
func TestSuspectTasksFindsASlotNobodyGotRight(t *testing.T) {
	arms := []ArmName{ArmBare, ArmClaudeMD, ArmGhosttree}
	miss := func(value string) Score {
		return Score{FactRecall: 0.4, Rejected: []RejectedClaim{
			{Slot: "revoke_route", Type: SlotString, Value: value}}}
	}
	records := []RunRecord{
		{TaskID: "z-03", Arm: ArmBare, Score: miss("DELETE /api/invites/by-id/{id}")},
		{TaskID: "z-03", Arm: ArmClaudeMD, Score: miss("DELETE /api/workspaces/{w}/proxy/api/invites/by-id/{id}")},
		{TaskID: "z-03", Arm: ArmGhosttree, Score: miss("DELETE /api/workspaces/{w}/proxy/api/invites/by-id/{id}")},
	}
	found := SuspectTasks(records, arms)
	if len(found) != 1 {
		t.Fatalf("the task must be reported exactly once, not once per rule: %+v", found)
	}
	if !strings.Contains(found[0].Reason, "revoke_route") {
		t.Fatalf("the reason must name the slot: %q", found[0].Reason)
	}
}

// TestSuspectTasksIgnoresASlotOneArmGotRight keeps the rule from firing on a
// merely hard question. If one arm answered it as asked, the key is fine.
func TestSuspectTasksIgnoresASlotOneArmGotRight(t *testing.T) {
	arms := []ArmName{ArmBare, ArmClaudeMD, ArmGhosttree}
	records := []RunRecord{
		{TaskID: "z-09", Arm: ArmBare, Score: Score{FactRecall: 1}},
		{TaskID: "z-09", Arm: ArmClaudeMD, Score: Score{FactRecall: 0,
			Rejected: []RejectedClaim{{Slot: "s", Type: SlotString, Value: "falsch-a"}}}},
		{TaskID: "z-09", Arm: ArmGhosttree, Score: Score{FactRecall: 0,
			Rejected: []RejectedClaim{{Slot: "s", Type: SlotString, Value: "falsch-b"}}}},
	}
	if found := SuspectTasks(records, arms); len(found) != 0 {
		t.Fatalf("one arm answering as asked means the key is fine: %+v", found)
	}
}
