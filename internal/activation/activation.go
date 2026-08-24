// Package activation matches conditional instructions against deterministic
// repository path context.
//
// There was a second gate on a task label — code, review, test and so on — and
// it was removed. A path is objectively determinable: the server sees the
// working directory and the files. A task is the agent's guess about what it is
// currently doing, from a vocabulary it cannot see, and in a real session it is
// several of them at once. A gate whose key has to be guessed does not filter
// reliably, it filters at random. It also cost a session: a Codex run ended on
// `unknown activation task "code review"` from a strict enum, and across the
// whole archive not one of 25 instructions ever used the gate, while 17 use the
// path gate.
package activation

import (
	"fmt"
	"path"
	"strings"
)

type Rule struct {
	Paths []string `json:"paths,omitempty"`
}
type Context struct {
	RepoPath string   `json:"repo_path,omitempty"`
	Paths    []string `json:"paths,omitempty"`
}

func ValidateRule(r Rule) error {
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
