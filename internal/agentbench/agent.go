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
	// MaxTurnsExceeded says the agent spent its whole turn budget without
	// delivering an answer. That is an outcome, not a fault: it is what
	// "searched and did not find" looks like, and it happens to the arm that
	// does not have the answer. Counted as a product failure it would drop out
	// of the scoring — removing the hardest cases and flattering the arm that
	// gave up.
	MaxTurnsExceeded bool   `json:"max_turns_exceeded,omitempty"`
	RawPath          string `json:"raw_path"`
	// Turns and CostUSD answer the question a recall number alone cannot:
	// what the answer cost. A memory that raises recall by a little and the
	// bill by a lot is a different product from one that raises both.
	Turns   int     `json:"turns"`
	CostUSD float64 `json:"cost_usd"`
	// ToolCalls counts every tool_use in the stream, including the ones a
	// subagent made. Turns and tool calls therefore disagree whenever an agent
	// delegates, and only tool calls stay an honest measure of work done.
	ToolCalls int `json:"tool_calls"`
	// DelegatedToolCalls is the part of ToolCalls that ran inside a subagent —
	// every tool_use carrying a parent_tool_use_id.
	//
	// It is recorded because a subagent has its own turn budget, so work done
	// there is invisible to --max-turns. One `bare` run on Robcord-Zentrale
	// spent 2 counted turns and made 17 tool calls, 15 of them delegated; a
	// `ghosttree` run on the same task made 6 calls and was cut off at the
	// budget. Without this number the first run reads as the cheaper one.
	DelegatedToolCalls int `json:"delegated_tool_calls,omitempty"`
	FilesRead          int `json:"files_read"`
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
