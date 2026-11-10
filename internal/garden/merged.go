package garden

import (
	"fmt"
	"regexp"
	"strings"
)

var hexCommit = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// Merge is what a merge into the default branch says about one path: the newest
// commit on the first-parent line of HEAD that touched it, the message of that
// commit (for a squash merge it is the pull request's title and text), and
// whether the path came new into the branch in the range, added or renamed.
type Merge struct {
	Commit string
	Text   string
	New    bool
}

// DefaultGhostModel is the model of the description sessions: describing a file
// from its code and the text of its merge needs no large model.
const DefaultGhostModel = "claude-haiku-5-5"

const (
	// mergeLookback is how far back a run without memory looks: the newest
	// commits of the first-parent line.
	mergeLookback = 20
	// maxMergeText bounds the commit message given to a session.
	maxMergeText = 1500
	// MaxMergedGhosts bounds the paths of merges taken up in one run on top of
	// --ghosts; a merge larger than that is finished by the next runs.
	MaxMergedGhosts = 60
)

// GhostMergeState is the name under which the check memory keeps the commit up
// to which merges were taken up (key 1).
func GhostMergeState(project string) string { return "ghost-merges|" + project }

// Merged lists the paths the merges since commit since brought into HEAD, on the
// first-parent line (each commit there is one merge or squashed pull request).
// Without since, or when since is not an ancestor of HEAD any more (a forced
// push), it looks at the last mergeLookback commits. A path deleted again in the
// range is left out.
func (r *Repo) Merged(since string) (map[string]Merge, error) {
	args := []string{"rev-list", "--first-parent", "HEAD"}
	if since != "" && r.isAncestor(since) {
		args = []string{"rev-list", "--first-parent", since + "..HEAD"}
	} else {
		args = append(args, fmt.Sprintf("--max-count=%d", mergeLookback))
	}
	out, err := r.git(args...)
	if err != nil {
		return nil, err
	}
	commits := strings.Fields(out)
	paths := map[string]Merge{}
	// Oldest first, so the newest commit that touched a path wins.
	for i := len(commits) - 1; i >= 0; i-- {
		c := commits[i]
		changes, err := r.mergeChanges(c)
		if err != nil {
			return nil, err
		}
		if len(changes) == 0 {
			continue
		}
		msg, _ := r.git("show", "-s", "--format=%B", c)
		msg = cutRunes(strings.TrimSpace(msg), maxMergeText)
		for _, ch := range changes {
			if ch.deleted {
				delete(paths, ch.path)
				continue
			}
			m := Merge{Commit: c, Text: msg, New: ch.added || paths[ch.path].New}
			paths[ch.path] = m
		}
	}
	return paths, nil
}

func (r *Repo) isAncestor(commit string) bool {
	if !hexCommit.MatchString(commit) {
		return false
	}
	_, err := r.git("merge-base", "--is-ancestor", commit, "HEAD")
	return err == nil
}

type mergeChange struct {
	path           string
	added, deleted bool
}

// mergeChanges is what one commit changed against its first parent (against
// nothing for the root commit), with renames found. The list is read with -z, so
// a path is never quoted.
func (r *Repo) mergeChanges(c string) ([]mergeChange, error) {
	var out string
	var err error
	if _, perr := r.git("rev-parse", "--verify", "--quiet", c+"^1"); perr == nil {
		out, err = r.git("diff", "-z", "--name-status", "-M", "--no-color", c+"^1", c)
	} else {
		out, err = r.git("diff-tree", "-z", "-r", "--root", "--no-commit-id", "--name-status", "-M", c)
	}
	if err != nil {
		return nil, err
	}
	f := strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	var changes []mergeChange
	for i := 0; i < len(f); i++ {
		status := f[i]
		if status == "" {
			continue
		}
		switch status[0] {
		case 'R', 'C':
			if i+2 >= len(f) {
				return changes, nil
			}
			if status[0] == 'R' {
				changes = append(changes, mergeChange{path: f[i+1], deleted: true})
			}
			changes = append(changes, mergeChange{path: f[i+2], added: true})
			i += 2
		default:
			if i+1 >= len(f) {
				return changes, nil
			}
			changes = append(changes, mergeChange{path: f[i+1], added: status[0] == 'A', deleted: status[0] == 'D'})
			i++
		}
	}
	return changes, nil
}

// Branch is the branch the checkout has out, empty for a detached HEAD.
func (r *Repo) Branch() string {
	out, err := r.git("symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}
