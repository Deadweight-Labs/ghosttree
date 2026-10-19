// Package projectlink is the per-machine registry that ties a local folder to a
// named project (one without git). It lives in the user's config directory and
// nothing inside a folder or repository decides the assignment: a committed file
// in someone else's repository must never be able to move a session into a
// project the user can read.
package projectlink

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/privatefile"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Path is the registry file, next to the client config.
func Path() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "ghosttree", "links.json")
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "ghosttree", "links.json")
}

type file struct {
	Links map[string]string `json:"links"`
}

// Entry is one link: a canonical absolute path and the project it belongs to.
type Entry struct {
	Path    string
	Project string
}

// Canonical makes a path comparable: absolute, symlinks resolved. A path that no
// longer exists is resolved through its nearest existing ancestor, so a deleted
// subfolder still finds the link above it.
func Canonical(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	rest := ""
	for d := abs; ; d = filepath.Dir(d) {
		if real, err := filepath.EvalSymlinks(d); err == nil {
			return filepath.Join(real, rest), nil
		}
		if parent := filepath.Dir(d); parent == d {
			return abs, nil
		}
		rest = filepath.Join(filepath.Base(d), rest)
	}
}

func load() map[string]string {
	b, err := os.ReadFile(Path())
	if err != nil {
		return map[string]string{}
	}
	var f file
	if json.Unmarshal(b, &f) != nil || f.Links == nil {
		return map[string]string{}
	}
	return f.Links
}

// Refusal says why a folder may never be linked: the home directory and "/" would
// put every session on the machine into one project. Empty means allowed.
func Refusal(dir string) string {
	real, err := Canonical(dir)
	if err != nil {
		real = dir
	}
	if real == string(filepath.Separator) {
		return "refusing to link /: every session on this machine would belong to the project"
	}
	if isHome(real) {
		return "refusing to link your home directory: every session on this machine would belong to the project; link a folder below it"
	}
	return ""
}

func isHome(real string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	if c, err := Canonical(home); err == nil {
		home = c
	}
	return filepath.Clean(home) == real
}

// Set links dir (canonicalized) to project. Replacing an entry is the caller's
// decision; Set only refuses what can never be linked.
func Set(dir, project string) (string, error) {
	if !scope.IsNamedProject(project) {
		return "", errors.New("not a named project: " + project)
	}
	real, err := Canonical(dir)
	if err != nil {
		return "", err
	}
	if why := Refusal(real); why != "" {
		return "", errors.New(why)
	}
	unlock, err := lock()
	if err != nil {
		return "", err
	}
	defer unlock()
	links := load()
	links[real] = project
	return real, save(links)
}

// Remove deletes the link of exactly this folder and reports whether there was one.
func Remove(dir string) (bool, error) {
	real, err := Canonical(dir)
	if err != nil {
		return false, err
	}
	unlock, err := lock()
	if err != nil {
		return false, err
	}
	defer unlock()
	links := load()
	if _, ok := links[real]; !ok {
		return false, nil
	}
	delete(links, real)
	return true, save(links)
}

// Prune removes the links whose folder no longer exists and returns how many.
func Prune() (int, error) {
	unlock, err := lock()
	if err != nil {
		return 0, err
	}
	defer unlock()
	links := load()
	n := 0
	for p := range links {
		if _, err := os.Stat(p); err != nil {
			delete(links, p)
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	return n, save(links)
}

func save(links map[string]string) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(file{Links: links}, "", "  ")
	if err != nil {
		return err
	}
	return privatefile.Write(p, append(b, '\n'))
}

// List returns all valid entries, sorted by path.
func List() []Entry {
	var out []Entry
	for p, id := range load() {
		if filepath.IsAbs(p) && scope.IsNamedProject(id) {
			out = append(out, Entry{Path: p, Project: id})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Find walks up from cwd and returns the nearest linked folder and its project.
// Entries for the home directory or "/" never apply, and neither do broken ones.
func Find(cwd string) (dir, project string) {
	if cwd == "" {
		return "", ""
	}
	links := load()
	if len(links) == 0 {
		return "", ""
	}
	real, err := Canonical(cwd)
	if err != nil {
		return "", ""
	}
	for d := real; ; d = filepath.Dir(d) {
		if parent := filepath.Dir(d); parent == d {
			return "", ""
		}
		if id, ok := links[d]; ok && scope.IsNamedProject(id) && !isHome(d) {
			return d, id
		}
	}
}

// Inside reports whether path lies strictly below dir.
func Inside(path, dir string) bool {
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}
