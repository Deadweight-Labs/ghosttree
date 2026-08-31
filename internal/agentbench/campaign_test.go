package agentbench

import (
	"context"
	"testing"
	"time"
)

func TestCampaignRejectsBuildAfterCutoff(t *testing.T) {
	cutoff := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	campaign := Campaign{
		RepoCommit:      "a1b2c3d",
		KnowledgeCutoff: cutoff,
		Builds: map[ArmName]MemoryBuild{
			ArmGhosttree: {SourceCutoff: cutoff.Add(time.Hour)},
		},
	}
	if err := campaign.Validate(nil); err == nil {
		t.Fatal("expected future leakage to be rejected")
	}
}

func TestCampaignAcceptsBuildAtTheCutoff(t *testing.T) {
	cutoff := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	campaign := Campaign{
		RepoCommit:      "a1b2c3d",
		KnowledgeCutoff: cutoff,
		Builds:          map[ArmName]MemoryBuild{ArmGhosttree: {SourceCutoff: cutoff}},
	}
	if err := campaign.Validate(nil); err != nil {
		t.Fatalf("a build exactly at the cutoff is legal: %v", err)
	}
}

func TestCampaignRejectsTaskOnAnotherCommit(t *testing.T) {
	campaign := Campaign{RepoCommit: "a1b2c3d", KnowledgeCutoff: time.Unix(1, 0).UTC()}
	tasks := []Task{{ID: "t1", Commit: "deadbeef"}}
	if err := campaign.Validate(tasks); err == nil {
		t.Fatal("expected a task on a different commit to be rejected")
	}
}

func TestCampaignRequiresACutoff(t *testing.T) {
	campaign := Campaign{RepoCommit: "a1b2c3d"}
	if err := campaign.Validate(nil); err == nil {
		t.Fatal("a campaign without a cutoff must not run")
	}
}

func TestFakeAgentReturnsScriptedTranscript(t *testing.T) {
	fake := NewFakeAgent(map[string]string{
		"t1": "arbeit\n```agentbench-form\n{\"slots\":{}}\n```",
	})
	tr, err := fake.Run(context.Background(), Invocation{Task: Task{ID: "t1"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tr.Output == "" {
		t.Fatalf("unexpected transcript: %+v", tr)
	}
}

func TestFakeAgentFailsForAnUnknownTask(t *testing.T) {
	fake := NewFakeAgent(nil)
	if _, err := fake.Run(context.Background(), Invocation{Task: Task{ID: "missing"}}); err == nil {
		t.Fatal("expected an error for a task the fake has no output for")
	}
}
