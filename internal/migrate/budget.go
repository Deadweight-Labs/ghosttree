package migrate

import (
	"sort"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
)

type InstructionCandidate struct {
	Title, Body string
	Activation  activation.Rule
}

type BudgetConflict struct {
	Context activation.Context
	Chars   int
	Titles  []string
}

func CheckInstructionBudget(existing, candidates []InstructionCandidate, limit int) (*BudgetConflict, error) {
	all := append(append([]InstructionCandidate(nil), existing...), candidates...)
	paths := map[string]bool{"": true}
	tasks := map[string]bool{"": true}
	var patterns []string
	for _, item := range all {
		if err := activation.ValidateRule(item.Activation); err != nil {
			return nil, err
		}
		for _, pattern := range item.Activation.Paths {
			paths[representativePath(pattern)] = true
			patterns = append(patterns, pattern)
		}
		for _, task := range item.Activation.Tasks {
			tasks[task] = true
		}
	}
	var conflicts []BudgetConflict
	for repoPath := range paths {
		for task := range tasks {
			ctx := activation.Context{RepoPath: repoPath, Task: task}
			conflict := BudgetConflict{Context: ctx}
			for _, item := range all {
				if activation.Matches(item.Activation, ctx) {
					conflict.Chars += len([]rune(item.Body))
					conflict.Titles = append(conflict.Titles, item.Title)
				}
			}
			if conflict.Chars > limit {
				sort.Strings(conflict.Titles)
				conflicts = append(conflicts, conflict)
			}
		}
	}
	// Complex globs may intersect at a string not present in the finite witness
	// set. When one participates, conservatively consider all path-gated rules
	// with the same task context together; this can reject an ambiguous large
	// rule set, but can never let an over-budget active combination through.
	if hasComplexPattern(patterns) {
		for task := range tasks {
			conflict := BudgetConflict{Context: activation.Context{Task: task}}
			for _, item := range all {
				if taskMatches(item.Activation, task) {
					conflict.Chars += len([]rune(item.Body))
					conflict.Titles = append(conflict.Titles, item.Title)
				}
			}
			if conflict.Chars > limit {
				sort.Strings(conflict.Titles)
				covered := false
				for _, exact := range conflicts {
					if exact.Context.Task == task && exact.Chars >= conflict.Chars {
						covered = true
						break
					}
				}
				if !covered {
					conflicts = append(conflicts, conflict)
				}
			}
		}
	}
	if len(conflicts) == 0 {
		return nil, nil
	}
	sort.Slice(conflicts, func(i, j int) bool {
		if conflicts[i].Chars != conflicts[j].Chars {
			return conflicts[i].Chars > conflicts[j].Chars
		}
		left := conflicts[i].Context.RepoPath + "\x00" + conflicts[i].Context.Task
		right := conflicts[j].Context.RepoPath + "\x00" + conflicts[j].Context.Task
		return left < right
	})
	return &conflicts[0], nil
}

func hasComplexPattern(patterns []string) bool {
	for _, pattern := range patterns {
		base := strings.TrimSuffix(pattern, "/**")
		if strings.ContainsAny(base, "*?[") {
			return true
		}
	}
	return false
}

func taskMatches(rule activation.Rule, task string) bool {
	if len(rule.Tasks) == 0 {
		return true
	}
	for _, allowed := range rule.Tasks {
		if allowed == task {
			return true
		}
	}
	return false
}

func representativePath(pattern string) string {
	if strings.HasSuffix(pattern, "/**") {
		return strings.TrimSuffix(pattern, "/**")
	}
	parts := strings.Split(pattern, "/")
	for i, part := range parts {
		part = strings.ReplaceAll(part, "*", "x")
		part = strings.ReplaceAll(part, "?", "x")
		if strings.Contains(part, "[") {
			part = "x"
		}
		parts[i] = part
	}
	return strings.Join(parts, "/")
}
