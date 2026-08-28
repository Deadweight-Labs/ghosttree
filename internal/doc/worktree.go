package doc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

var Kinds = []string{"spec", "plan", "investigation", "report", "other"}

var slugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)

var reservedWindowsNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

var kindDirs = map[string]string{
	"spec": "specs", "plan": "plans", "investigation": "investigations",
	"report": "reports", "other": "other",
}

type Entry struct {
	DocumentID   int64  `json:"document_id"`
	BaseRevision int    `json:"base_revision"`
	BaseDigest   string `json:"base_digest"`
	Path         string `json:"path"`
}

type State map[string]Entry

func Dir(repoRoot string) string {
	return filepath.Join(repoRoot, ".ghosttree", "edit")
}

func KindDir(kind string) (string, error) {
	dir, ok := kindDirs[kind]
	if !ok {
		return "", fmt.Errorf("unknown kind %q; expected one of %s", kind, strings.Join(Kinds, ", "))
	}
	return dir, nil
}

func KindOfDir(dir string) (string, error) {
	for kind, candidate := range kindDirs {
		if candidate == dir {
			return kind, nil
		}
	}
	return "", fmt.Errorf("unknown document directory %q", dir)
}

func RelPath(kind, createdAt, slug string) (string, error) {
	dir, err := KindDir(kind)
	if err != nil {
		return "", err
	}
	if err := ValidateSlug(slug); err != nil {
		return "", err
	}
	day := createdAt
	if len(day) >= 10 {
		day = day[:10]
	}
	return filepath.ToSlash(filepath.Join(dir, day+"-"+slug+".md")), nil
}

func ValidateSlug(slug string) error {
	if !slugPattern.MatchString(slug) || reservedWindowsNames[strings.Split(slug, ".")[0]] {
		return fmt.Errorf("invalid slug %q; use lowercase ASCII letters, digits, dots and hyphens", slug)
	}
	return nil
}

func LoadState(repoRoot string) (State, error) {
	path := filepath.Join(Dir(repoRoot), ".state.json")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return State{}, nil
	}
	if err != nil {
		return nil, err
	}
	state := State{}
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return state, nil
}

func SaveState(repoRoot string, state State) error {
	dir := Dir(repoRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state.json.tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, ".state.json"))
}

func ReadFile(repoRoot, rel string) (string, error) {
	full, err := safePath(repoRoot, rel)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("%s is not valid UTF-8; ghosttree stores text byte-for-byte and will not rewrite it", rel)
	}
	return string(raw), nil
}

func WriteFile(repoRoot, rel, body string) error {
	full, err := safePath(repoRoot, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(body), 0o644)
}

func MoveFile(repoRoot, oldRel, newRel string) error {
	oldPath, err := safePath(repoRoot, oldRel)
	if err != nil {
		return err
	}
	newPath, err := safePath(repoRoot, newRel)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(newPath); err == nil {
		return fmt.Errorf("destination already exists: %s", newRel)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		return err
	}
	return os.Rename(oldPath, newPath)
}

func safePath(repoRoot, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, `\\`) {
		return "", fmt.Errorf("invalid document path %q", rel)
	}
	base := Dir(repoRoot)
	clean := filepath.Clean(filepath.FromSlash(rel))
	full := filepath.Join(base, clean)
	contained, err := filepath.Rel(base, full)
	if err != nil || contained == "." || contained == ".." || strings.HasPrefix(contained, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("document path %q leaves the worktree", rel)
	}
	return full, nil
}
