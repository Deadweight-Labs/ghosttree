package agentbench

import (
	"context"
	"fmt"
	"time"
)

type Invocation struct {
	Task      Task
	Arm       ArmName
	Workspace string
	Config    AgentConfig
	// Repetition numbers the repeat of this task/arm cell. It is part of the
	// raw transcript's file name: without it the second repetition overwrites
	// the first, and the variance between repeats — the whole reason for
	// repeating — could no longer be read back from the transcripts.
	Repetition int
}

type Transcript struct {
	Output string `json:"-"`
	// AgentError carries the agent's own error result. A run that failed for
	// the agent's own reasons must not be scored as an abstention.
	AgentError string `json:"agent_error,omitempty"`
	RawPath    string `json:"raw_path"`
	// Turns and CostUSD answer the question a recall number alone cannot:
	// what the answer cost. A memory that raises recall by a little and the
	// bill by a lot is a different product from one that raises both.
	Turns        int           `json:"turns"`
	CostUSD      float64       `json:"cost_usd"`
	ToolCalls    int           `json:"tool_calls"`
	FilesRead    int           `json:"files_read"`
	InputTokens  int           `json:"input_tokens"`
	OutputTokens int           `json:"output_tokens"`
	Duration     time.Duration `json:"duration_ns"`
}

type Agent interface {
	Run(ctx context.Context, inv Invocation) (Transcript, error)
}

type FakeAgent struct{ outputs map[string]string }

func NewFakeAgent(outputs map[string]string) *FakeAgent {
	return &FakeAgent{outputs: outputs}
}

func (f *FakeAgent) Run(_ context.Context, inv Invocation) (Transcript, error) {
	out, ok := f.outputs[inv.Task.ID]
	if !ok {
		return Transcript{}, fmt.Errorf("fake agent has no output for task %q", inv.Task.ID)
	}
	return Transcript{Output: out, ToolCalls: 1, FilesRead: 1}, nil
}
