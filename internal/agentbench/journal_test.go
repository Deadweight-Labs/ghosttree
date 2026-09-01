package agentbench

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func journalCampaign(arms ...ArmName) Campaign {
	return Campaign{
		Name: "c", RepoCommit: "c", KnowledgeCutoff: time.Unix(1, 0).UTC(),
		Arms: arms, Repetitions: 1, Seed: 1,
	}
}

func answering(value string) string {
	return "fertig\n```agentbench-form\n{\"slots\":{\"s\":{\"string\":\"" + value + "\"}}}\n```"
}

// countingAgent records how often it was asked to run. A resumed campaign that
// silently re-ran finished work would still produce a plausible report — the
// only way to see it is to count the invocations.
type countingAgent struct {
	runs   int
	output string
}

func (a *countingAgent) Run(context.Context, Invocation) (Transcript, error) {
	a.runs++
	return Transcript{Output: a.output}, nil
}

func TestJournalRecordsEachRunBeforeTheCampaignEnds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	journal, err := OpenRunJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	campaign := journalCampaign(ArmBare, ArmGhosttree)
	agent := &countingAgent{output: answering("x")}

	if _, err := Run(context.Background(), campaign,
		[]Task{pilotTask("t1"), pilotTask("t2")}, SameAgent(agent), journal); err != nil {
		t.Fatal(err)
	}
	// Bewusst nicht geschlossen: nach einem Absturz wird auch nicht
	// geschlossen, und genau dann muss die Datei vollstaendig sein.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if lines := countLines(string(raw)); lines != 4 {
		t.Fatalf("the journal must hold all 4 runs before the campaign returns, holds %d", lines)
	}
}

func countLines(s string) int {
	var n int
	for _, r := range s {
		if r == '\n' {
			n++
		}
	}
	return n
}

func TestResumedCampaignDoesNotPayForFinishedRunsAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	campaign := journalCampaign(ArmBare, ArmGhosttree)
	tasks := []Task{pilotTask("t1"), pilotTask("t2")}

	first, err := OpenRunJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	// Erster Abschnitt: ein Datensatz, dann bricht die Kampagne ab.
	if err := first.Emit(RunRecord{TaskID: "t1", Arm: ArmBare, Repetition: 1}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	resumed, err := OpenRunJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	agent := &countingAgent{output: answering("x")}
	if _, err := Run(context.Background(), campaign, tasks, SameAgent(agent), resumed); err != nil {
		t.Fatal(err)
	}
	if agent.runs != 3 {
		t.Fatalf("the resumed campaign must run the 3 missing runs, ran %d", agent.runs)
	}
	if got := len(resumed.Records()); got != 4 {
		t.Fatalf("the journal must hold the whole campaign, holds %d", got)
	}
}

// TestJournalKeepsAFailedRunFailed guards the tempting shortcut. Re-rolling a
// failed run until it works selects for the runs that happened to succeed, and
// how often an arm fails is itself a result.
func TestJournalKeepsAFailedRunFailed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	journal, err := OpenRunJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Emit(RunRecord{
		TaskID: "t1", Arm: ArmBare, Repetition: 1,
		Failure: FailureProduct, FailureMsg: "timeout",
	}); err != nil {
		t.Fatal(err)
	}
	if !journal.Done("t1", ArmBare, 1) {
		t.Fatal("a recorded failure counts as done; otherwise the campaign rolls until it likes the result")
	}
}

// TestRecoverFromTranscriptsRebuildsWhatACrashLost is the case the pilot ran
// into: 33 transcripts on disk, no records at all.
func TestRecoverFromTranscriptsRebuildsWhatACrashLost(t *testing.T) {
	rawDir := filepath.Join(t.TempDir(), "raw")
	armDir := filepath.Join(rawDir, string(ArmGhosttree))
	if err := os.MkdirAll(armDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, armDir, "np-01-wire--ghosttree--r2.jsonl", answering("x"))
	// Ein Transkript einer Aufgabe, die es im Satz nicht mehr gibt: es darf
	// die Rettung der uebrigen nicht aufhalten.
	writeTranscript(t, armDir, "np-03-broken--ghosttree--r1.jsonl", answering("x"))

	task := pilotTask("np-01-wire")
	records, err := RecoverFromTranscripts(journalCampaign(ArmGhosttree), []Task{task}, rawDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("expected the one known task to be recovered, got %d", len(records))
	}
	got := records[0]
	if got.TaskID != "np-01-wire" || got.Arm != ArmGhosttree || got.Repetition != 2 {
		t.Fatalf("task, arm and repetition must come back from the file name: %+v", got)
	}
	if got.Failure != FailureNone || got.Score.FactRecall != 1 {
		t.Fatalf("a recovered record must carry its score: %+v", got)
	}
	if got.Category != task.Category {
		t.Fatalf("the task definition must fill in what the transcript cannot: %+v", got)
	}
}
