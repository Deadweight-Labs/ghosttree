package agentbench

import "sort"

// TaskSuspicion flags a task whose ground truth is more likely wrong than the
// agents are.
type TaskSuspicion struct {
	TaskID string  `json:"task_id"`
	Runs   int     `json:"runs"`
	Arms   int     `json:"arms"`
	Recall float64 `json:"recall"`
	Reason string  `json:"reason"`
}

// SuspectTasks finds tasks that every arm failed in the same way.
//
// The rule comes from a real case in the first pilot: asked how many DNS
// provider implementations the repository has, all three arms answered "one"
// against a ground truth of "two". The arms were right — the second directory
// is a dry-run wrapper around a provider, not a provider — and the task author
// had counted directories instead of reading them.
//
// Unanimity across arms that otherwise disagree is evidence about the task, not
// about the arms. A memory system cannot make three differently-equipped agents
// agree on the same wrong answer; a badly worded question can. Such a task is
// therefore reported and excluded from the headline numbers rather than counted
// as a shared failure — because counted, it silently pushes every arm down by
// the same amount and dilutes every contrast toward zero.
func SuspectTasks(records []RunRecord, arms []ArmName) []TaskSuspicion {
	type agg struct {
		runs   int
		recall float64
		arms   map[ArmName]bool
	}
	byTask := map[string]*agg{}
	for _, record := range records {
		if record.Failure != FailureNone {
			continue
		}
		a := byTask[record.TaskID]
		if a == nil {
			a = &agg{arms: map[ArmName]bool{}}
			byTask[record.TaskID] = a
		}
		a.runs++
		a.recall += record.Score.FactRecall
		a.arms[record.Arm] = true
	}

	var out []TaskSuspicion
	for id, a := range byTask {
		// Nur wenn wirklich jeder Arm angetreten ist: fehlt einer, ist die
		// Einstimmigkeit kein Argument.
		if len(a.arms) < len(arms) || len(arms) < 2 {
			continue
		}
		mean := a.recall / float64(a.runs)
		if mean > 0 {
			continue
		}
		out = append(out, TaskSuspicion{
			TaskID: id, Runs: a.runs, Arms: len(a.arms), Recall: mean,
			Reason: "every arm scored zero; check the ground truth before the arms",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	return out
}
