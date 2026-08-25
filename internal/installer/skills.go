package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/skills"
)

// manifestName holds what ctx last wrote, keyed by absolute path.
//
// The whole point of keeping it is one distinction that is otherwise
// impossible: a file that differs from the shipped version is either OUR older
// version, which should be updated, or the user's own edit, which must not be
// touched. Without the manifest an update has to choose between never
// refreshing anything and silently overwriting somebody's work.
const manifestName = "skills.json"

func manifestPath() string {
	return filepath.Join(filepath.Dir(config.Path()), manifestName)
}

func readManifest() map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(manifestPath())
	if err != nil {
		return m
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]string{}
	}
	return m
}

func writeManifest(m map[string]string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(manifestPath()), 0o755); err != nil {
		return err
	}
	return writeAtomic(manifestPath(), append(b, '\n'), 0o644)
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// installSkills writes every embedded skill into the harness's skill directory.
//
// Idempotent, and it never overwrites a file the user changed. The four cases,
// in order: absent -> write; identical to the shipped version -> leave alone;
// identical to what we last wrote -> update; anything else -> keep theirs and
// report it. Only the manifest can tell the last two apart.
func installSkills(h Harness, home string) ([]Change, error) {
	if h.SkillsRoot == nil {
		return nil, nil
	}
	root := h.SkillsRoot(home)
	manifest := readManifest()
	var changes []Change
	dirty := false

	for _, name := range skills.Names() {
		files, err := skills.Files(name)
		if err != nil {
			return changes, err
		}
		for _, rel := range sortedKeys(files) {
			content := files[rel]
			target := filepath.Join(root, name, filepath.FromSlash(rel))
			shipped := sum(content)

			old, err := os.ReadFile(target)
			switch {
			case os.IsNotExist(err):
				if err := writeSkillFile(target, content); err != nil {
					return changes, err
				}
				manifest[target], dirty = shipped, true
				changes = append(changes, Change{Path: target, Action: "created"})
			case err != nil:
				return changes, err
			case sum(old) == shipped:
				// Already ours and already current. Record the hash anyway:
				// otherwise a machine that was set up before the manifest
				// existed would look edited on the next update.
				if manifest[target] != shipped {
					manifest[target], dirty = shipped, true
				}
				changes = append(changes, Change{Path: target, Action: "unchanged"})
			case manifest[target] == sum(old):
				if err := writeSkillFile(target, content); err != nil {
					return changes, err
				}
				manifest[target], dirty = shipped, true
				changes = append(changes, Change{Path: target, Action: "updated"})
			default:
				changes = append(changes, Change{Path: target, Action: "kept (edited locally)"})
			}
		}
	}
	if dirty {
		if err := writeManifest(manifest); err != nil {
			return changes, err
		}
	}
	return changes, nil
}

func writeSkillFile(target string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return writeAtomic(target, content, 0o644)
}

// SkillDrift names the skill files that no longer match what ctx wrote.
//
// Running an edited skill is allowed. Running one without knowing is the
// failure: the user believes they are getting updates and is not.
func SkillDrift(h Harness, home string) []string {
	if h.SkillsRoot == nil {
		return nil
	}
	root := h.SkillsRoot(home)
	manifest := readManifest()
	var out []string
	for target, want := range manifest {
		if !strings.HasPrefix(target, root+string(filepath.Separator)) {
			continue
		}
		got, err := os.ReadFile(target)
		if err != nil {
			// Deleted rather than edited. Reinstalling brings it back, and
			// saying "differs" about a file that is gone would mislead.
			continue
		}
		if sum(got) != want {
			out = append(out, target)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
