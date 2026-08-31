package agentbench

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// armRecordingAgent notes which arm actually invoked it, so a runner that
// silently reuses one arm's agent for every arm cannot pass.
type armRecordingAgent struct {
	arm  ArmName
	seen *[]ArmName
}

func (a armRecordingAgent) Run(_ context.Context, inv Invocation) (Transcript, error) {
	*a.seen = append(*a.seen, a.arm)
	if inv.Arm != a.arm {
		return Transcript{}, fmt.Errorf("agent for %q was handed an invocation for %q", a.arm, inv.Arm)
	}
	return Transcript{Output: "```agentbench-form\n{\"slots\":{\"s\":{\"string\":\"x\"}}}\n```"}, nil
}

func TestRunUsesAnAgentPerArm(t *testing.T) {
	var seen []ArmName
	campaign := Campaign{
		RepoCommit: "c", KnowledgeCutoff: time.Unix(1, 0).UTC(),
		Arms: []ArmName{ArmClaudeNative, ArmGhosttree}, Repetitions: 1, Seed: 3,
	}

	records, err := Run(context.Background(), campaign, []Task{pilotTask("t1")},
		AgentForFunc(func(arm ArmName) (Agent, error) {
			return armRecordingAgent{arm: arm, seen: &seen}, nil
		}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(records) != 2 {
		t.Fatalf("want one record per arm, got %d", len(records))
	}
	for _, record := range records {
		if record.Failure != FailureNone {
			t.Fatalf("each arm must run under its own agent: %+v", record)
		}
	}
	if len(seen) != 2 || seen[0] == seen[1] {
		t.Fatalf("both arms must have been invoked distinctly, got %v", seen)
	}
}

func TestRunReportsAnArmThatCannotBePrepared(t *testing.T) {
	campaign := Campaign{
		RepoCommit: "c", KnowledgeCutoff: time.Unix(1, 0).UTC(),
		Arms: []ArmName{ArmBare}, Repetitions: 1, Seed: 1,
	}

	records, err := Run(context.Background(), campaign, []Task{pilotTask("t1")},
		AgentForFunc(func(ArmName) (Agent, error) {
			return nil, fmt.Errorf("leakage check failed")
		}))
	if err != nil {
		t.Fatalf("a preparation error is per-arm, not fatal for the campaign: %v", err)
	}
	if records[0].Failure != FailureInfrastructure {
		t.Fatalf("a workspace that cannot be built is infrastructure, got %q", records[0].Failure)
	}
}

func TestSameAgentAdaptsASingleAgentToEveryArm(t *testing.T) {
	fake := NewFakeAgent(map[string]string{"t1": "```agentbench-form\n{\"slots\":{}}\n```"})
	provider := SameAgent(fake)
	for _, arm := range []ArmName{ArmBare, ArmGhosttree} {
		got, err := provider.For(arm)
		if err != nil || got != Agent(fake) {
			t.Fatalf("SameAgent must hand back the same agent for %q: %v", arm, err)
		}
	}
}
