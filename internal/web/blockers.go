package web

import (
	"slices"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Blockers (REQ-599): an open human task is what an issue, a work package, a
// branch and a repository wait for. Every list that shows it draws the person
// the task waits for as the same stack of pictures with the red outline
// (waitStack); the issue and the pull request page spell the task out.

// taskWaiter is the person an open task waits for: the one it is assigned to,
// otherwise the one who asked.
func taskWaiter(t humantask.Task) string { return firstNonEmpty(t.Assignee, t.CreatedBy) }

// waitersOf are the people the tasks wait for, each once, in the order of the tasks.
func waitersOf(tasks []humantask.Task) []string {
	var out []string
	for _, t := range tasks {
		if who := taskWaiter(t); who != "" && !slices.Contains(out, who) {
			out = append(out, who)
		}
	}
	return out
}

// repoOpenTasks are the open human tasks of the project the viewer may list.
func (a *app) repoOpenTasks(pa *store.ProjectAccess, remote string) []humantask.Task {
	if pa == nil {
		return nil
	}
	list, err := a.store.ListHumanTasks(pa, store.TaskFilter{Project: remote, State: humantask.Open, Limit: repoCountAt})
	if err != nil {
		return nil
	}
	return list
}

// packageStanding is what a work package contributes to a row of a list: the
// criteria met and all of them, and the people who are waited for on one of its
// issues, as far as the viewer may read those issues. tasks is the first open
// task of each issue (openTasksByRequest).
func (a *app) packageStanding(pa *store.ProjectAccess, d requestdomain.PackageDetail, tasks map[int64]humantask.Task) (pullPackage, bool) {
	members := pa.PackageView(d, false).Members
	ids := make([]int64, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.RequestID)
	}
	prog, err := a.store.CriteriaProgress(ids)
	if err != nil {
		return pullPackage{}, false
	}
	var p pullPackage
	var open []humantask.Task
	for _, id := range ids {
		p.Done += prog[id].Done
		p.Total += prog[id].Total
		if t, ok := tasks[id]; ok {
			open = append(open, t)
		}
	}
	p.Waiting = waitersOf(open)
	return p, true
}
