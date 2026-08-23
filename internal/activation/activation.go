// Package activation matches conditional instructions against deterministic
// repository path and task context.
package activation

import (
	"fmt"
	"path"
	"strings"
)

type Rule struct {
	Paths []string `json:"paths,omitempty"`
	Tasks []string `json:"tasks,omitempty"`
}
type Context struct {
	RepoPath string   `json:"repo_path,omitempty"`
	Paths    []string `json:"paths,omitempty"`
	Task     string   `json:"task,omitempty"`
}

var validTasks = map[string]bool{"code": true, "review": true, "test": true, "deploy": true, "security": true, "docs": true}

func ValidateRule(r Rule) error {
	for _, task := range r.Tasks {
		if !validTasks[task] {
			return fmt.Errorf("unknown activation task %q", task)
		}
	}
	for _, pattern := range r.Paths {
		if err := validateRel(pattern); err != nil {
			return fmt.Errorf("activation path %q: %w", pattern, err)
		}
		if _, err := path.Match(strings.ReplaceAll(pattern, "**", "*"), "probe"); err != nil {
			return fmt.Errorf("activation path %q: %w", pattern, err)
		}
	}
	return nil
}

func NormalizeContext(c Context) (Context, error) {
	if c.Task != "" && !validTasks[c.Task] {
		return Context{}, fmt.Errorf("unknown activation task %q", c.Task)
	}
	var err error
	if c.RepoPath, err = normalizeRel(c.RepoPath); err != nil {
		return Context{}, err
	}
	for i, p := range c.Paths {
		c.Paths[i], err = normalizeRel(p)
		if err != nil {
			return Context{}, err
		}
	}
	return c, nil
}

func Matches(r Rule, c Context) bool {
	if len(r.Paths) > 0 {
		matched := false
		for _, candidate := range append([]string{c.RepoPath}, c.Paths...) {
			for _, pattern := range r.Paths {
				if matchPath(pattern, candidate) {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(r.Tasks) > 0 {
		matched := false
		for _, task := range r.Tasks {
			if c.Task == task {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func matchPath(pattern, candidate string) bool {
	if candidate == "" {
		return false
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return candidate == prefix || strings.HasPrefix(candidate, prefix+"/")
	}
	ok, _ := path.Match(pattern, candidate)
	return ok
}

func validateRel(v string) error {
	if v == "" {
		return fmt.Errorf("path is empty")
	}
	if strings.HasPrefix(v, "/") || strings.Contains(v, "\\") {
		return fmt.Errorf("path must be repository-relative and slash-normalized")
	}
	for _, part := range strings.Split(v, "/") {
		if part == ".." {
			return fmt.Errorf("path escapes repository")
		}
	}
	return nil
}

func normalizeRel(v string) (string, error) {
	if v == "" || v == "." {
		return "", nil
	}
	if err := validateRel(v); err != nil {
		return "", err
	}
	clean := path.Clean(v)
	if clean == "." {
		return "", nil
	}
	return clean, nil
}
