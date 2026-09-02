package agentbench

import (
	"sort"
)

// DelegationSummary is how much of an arm's work ran inside a subagent.
type DelegationSummary struct {
	Arm ArmName `json:"arm"`
	// Runs that used a subagent at all, out of RunsTotal.
	Runs      int `json:"runs"`
	RunsTotal int `json:"runs_total"`
	// MeanTurns and MeanToolCalls are the two numbers that disagree.
	MeanTurns     float64 `json:"mean_turns"`
	MeanToolCalls float64 `json:"mean_tool_calls"`
	// MeanDelegated is the part of MeanToolCalls that no turn budget saw.
	MeanDelegated float64 `json:"mean_delegated"`
}

// SummariseDelegation reports, per arm, how often it delegated and how far its
// turn count drifted from the work it actually did.
//
// It exists because the drift is invisible in every other number the report
// prints. A subagent carries its own turn budget, so `--max-turns` does not
// constrain it: on Robcord-Zentrale at budget 6, one `bare` run spent 2 counted
// turns and made 17 tool calls, 15 of them delegated, while a `ghosttree` run
// on the same task was cut off after 6 calls. Compared on turns, the delegating
// arm looked cheaper and the other looked like it had wasted its budget; the
// contrast reversed sign when measured in tool calls (+1.08 turns against
// −4.42 calls).
//
// The arms do not delegate equally often — the choice is the model's, and an
// agent with less orientation reaches for it more readily — so this is not a
// constant that cancels out of a paired contrast. Any campaign that treats the
// turn budget as its independent variable has to either forbid the tool (see
// AgentConfig.DisallowedTools) or read this table before believing its own
// effort numbers.
func SummariseDelegation(records []RunRecord, arms []ArmName) []DelegationSummary {
	type agg struct {
		runs, total             int
		turns, calls, delegated int
	}
	byArm := map[ArmName]*agg{}
	for _, record := range records {
		if record.Failure != FailureNone {
			continue
		}
		a := byArm[record.Arm]
		if a == nil {
			a = &agg{}
			byArm[record.Arm] = a
		}
		a.total++
		a.turns += record.Transcript.Turns
		a.calls += record.Transcript.ToolCalls
		a.delegated += record.Transcript.DelegatedToolCalls
		if record.Transcript.DelegatedToolCalls > 0 {
			a.runs++
		}
	}

	var out []DelegationSummary
	for arm, a := range byArm {
		if a.total == 0 {
			continue
		}
		out = append(out, DelegationSummary{
			Arm: arm, Runs: a.runs, RunsTotal: a.total,
			MeanTurns:     float64(a.turns) / float64(a.total),
			MeanToolCalls: float64(a.calls) / float64(a.total),
			MeanDelegated: float64(a.delegated) / float64(a.total),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Arm < out[j].Arm })
	return out
}

// DelegationImbalanced reports whether the arms delegated differently often
// enough that turn counts should not be compared across them.
//
// The threshold is deliberately low. This is not a statistical test but a
// warning label: if one arm delegated in a quarter of its runs and another
// never did, their turn counts measure different things, and no interval width
// fixes that.
func DelegationImbalanced(summaries []DelegationSummary) bool {
	var low, high float64
	first := true
	for _, s := range summaries {
		if s.RunsTotal == 0 {
			continue
		}
		share := float64(s.Runs) / float64(s.RunsTotal)
		if first {
			low, high, first = share, share, false
			continue
		}
		low, high = min(low, share), max(high, share)
	}
	return !first && high-low >= 0.25
}
