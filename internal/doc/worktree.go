package doc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/plainfs"
)

var Kinds = []string{"spec", "plan", "investigation", "report", "other"}

var datedSlugPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(-|$)`)

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
	// A slug that already starts with a date is dated by its author; adding
	// the creation date (UTC) in front would double the prefix.
	if datedSlugPattern.MatchString(slug) {
		return filepath.ToSlash(filepath.Join(dir, slug+".md")), nil
	}
	day := createdAt
	if len(day) >= 10 {
		day = day[:10]
	}
	return filepath.ToSlash(filepath.Join(dir, day+"-"+slug+".md")), nil
}

// MaxBodyBytes is the largest text a document revision may have. The server
// refuses more, and ctx doc says so before it sends anything.
const MaxBodyBytes = 5 << 20

// TooLarge is the one wording of a text over MaxBodyBytes, for the server's
// answer and the CLI alike.
func TooLarge(n int) error {
	return fmt.Errorf("the document has %.1f MB; a document may have at most %d MB", float64(n)/(1<<20), MaxBodyBytes>>20)
}

func ValidateSlug(slug string) error {
	if !slugPattern.MatchString(slug) || reservedWindowsNames[strings.Split(slug, ".")[0]] {
		return fmt.Errorf("invalid slug %q; use lowercase ASCII letters, digits, dots and hyphens", slug)
	}
	return nil
}

func LoadState(repoRoot string) (State, error) {
	if err := plainfs.CheckDirs(rootDirs(repoRoot)...); err != nil {
		return nil, err
	}
	path := filepath.Join(Dir(repoRoot), ".state.json")
	raw, err := plainfs.Read(path)
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
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return plainfs.Write(filepath.Join(Dir(repoRoot), ".state.json"), append(raw, '\n'), rootDirs(repoRoot)...)
}

func ReadFile(repoRoot, rel string) (string, error) {
	full, err := safePath(repoRoot, rel)
	if err != nil {
		return "", err
	}
	if err := plainfs.CheckDirs(dirChain(repoRoot, rel)...); err != nil {
		return "", err
	}
	raw, err := plainfs.Read(full)
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
	return plainfs.Write(full, []byte(body), dirChain(repoRoot, rel)...)
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
	if err := plainfs.CheckDirs(dirChain(repoRoot, oldRel)...); err != nil {
		return err
	}
	if fi, err := os.Lstat(oldPath); err == nil && !fi.Mode().IsRegular() {
		return plainfs.ErrNotPlain(oldPath)
	}
	if err := plainfs.CheckDirs(dirChain(repoRoot, newRel)...); err != nil {
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
	if err := plainfs.CheckDirs(dirChain(repoRoot, newRel)...); err != nil {
		return err
	}
	return os.Rename(oldPath, newPath)
}

// RemoveFile deletes the copy of a document in the worktree. A link is
// removed as the entry it is, never followed, but a link on the way to it is
// refused.
func RemoveFile(repoRoot, rel string) error {
	full, err := safePath(repoRoot, rel)
	if err != nil {
		return err
	}
	if err := plainfs.CheckDirs(dirChain(repoRoot, rel)...); err != nil {
		return err
	}
	return os.Remove(full)
}

// rootDirs are the directories above the state file, from the outside in.
func rootDirs(repoRoot string) []string {
	return []string{filepath.Join(repoRoot, ".ghosttree"), Dir(repoRoot)}
}

// dirChain lists the directories from .ghosttree down to the one that holds
// the file rel, so that a link anywhere on that way is found.
func dirChain(repoRoot, rel string) []string {
	chain := rootDirs(repoRoot)
	dir := Dir(repoRoot)
	parts := strings.Split(filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel))), "/")
	for _, part := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, part)
		chain = append(chain, dir)
	}
	return chain
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
