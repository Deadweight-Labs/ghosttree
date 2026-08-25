// Package skills carries the onboarding skills that ctx installs into each
// harness.
//
// They live here as plain files rather than as Go string constants for one
// reason: a change to a command and a change to the text that describes it
// should be able to travel in the same commit. A skill that documents a flag
// which was renamed three versions ago leads people wrong with confidence.
package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed ghosttree-setup ghosttree-onboard-repo
var FS embed.FS

func Names() []string {
	entries, err := FS.ReadDir(".")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// Files returns every file of one skill, keyed by its path relative to the
// skill directory. The caller writes them under a harness-specific root; the
// layout inside is identical everywhere, because both supported harnesses read
// the same SKILL.md plus references/ shape.
func Files(name string) (map[string][]byte, error) {
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") {
		return nil, fmt.Errorf("bad skill name %q", name)
	}
	out := map[string][]byte{}
	err := fs.WalkDir(FS, name, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := FS.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := relTo(name, p)
		if err != nil {
			return err
		}
		out[rel] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("unknown skill %q", name)
	}
	return out, nil
}

// relTo works on embed paths, which are slash-separated on every platform —
// path, not filepath.
func relTo(base, p string) (string, error) {
	rel := strings.TrimPrefix(p, base+"/")
	if rel == p {
		return "", fmt.Errorf("%q is not under %q", p, base)
	}
	return path.Clean(rel), nil
}
