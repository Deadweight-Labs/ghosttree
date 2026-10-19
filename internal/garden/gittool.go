package garden

import (
	"context"
	"fmt"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// The fixed git tool of a session. A session has no Bash; what it may ask git
// is a log of a path or one commit, with arguments that are checked here and
// put into a command line of a fixed shape. Nothing the session sends is ever
// parsed as an option: revisions are hex, paths are plain relative names and
// always sit behind "--", text goes behind -S as one argument.

const (
	gitLogMax      = 100
	gitLogDefault  = 30
	gitOutMax      = 200 << 10
	gitToolTimeout = 30 * time.Second
	gitPickaxeMax  = 200
)

var (
	revHexRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	// Path characters: letters, digits and the usual file name punctuation.
	// No glob or pathspec magic (":", "*", "?", "[", "\\"), no space, no
	// leading "-".
	pathRe2 = regexp.MustCompile(`^[\p{L}\p{N}_./+@=,~-]+$`)
)

// GitLogParams are the arguments of the log tool.
type GitLogParams struct {
	// Path limits the log to one file or directory of the checkout.
	Path string `json:"path,omitempty"`
	// Max is the number of commits (1 to 100, default 30).
	Max int `json:"max,omitempty"`
	// Pickaxe lists commits that added or removed this exact text.
	Pickaxe string `json:"pickaxe,omitempty"`
}

// GitShowParams are the arguments of the show tool.
type GitShowParams struct {
	// Commit is a hash of 7 to 40 hex digits, or HEAD.
	Commit string `json:"commit"`
	// Path limits the diff to one file or directory of the checkout.
	Path string `json:"path,omitempty"`
}

// CleanGitPath accepts a plain path relative to the checkout and returns it in
// clean form, or says why not.
func CleanGitPath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if len(p) > 300 || !pathRe2.MatchString(p) {
		return "", fmt.Errorf("Pfad %q ist nicht erlaubt (nur einfache Zeichen, keine Muster)", p)
	}
	if strings.HasPrefix(p, "-") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
		return "", fmt.Errorf("Pfad %q muss relativ zum Checkout sein und darf nicht mit - oder ~ beginnen", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", fmt.Errorf("Pfad %q verlässt den Checkout", p)
		}
	}
	c := path.Clean(p)
	if c == ".git" || strings.HasPrefix(c, ".git/") || strings.Contains(c, "/.git/") || strings.HasSuffix(c, "/.git") {
		return "", fmt.Errorf("Pfad %q zeigt in .git", p)
	}
	return c, nil
}

// CheckRevision accepts a commit hash or HEAD. Anything else, an option, a
// range, a ref name or an expression, is refused: with hex digits only there is
// no way to smuggle an option in as a revision.
func CheckRevision(rev string) (string, error) {
	if rev == "HEAD" || revHexRe.MatchString(rev) {
		return rev, nil
	}
	return "", fmt.Errorf("Revision %q ist nicht erlaubt (Hash mit 7 bis 40 Hexziffern oder HEAD)", rev)
}

// LogArgs builds the git command line (after "git -C root") of the log tool.
func LogArgs(p GitLogParams) ([]string, error) {
	rel, err := CleanGitPath(p.Path)
	if err != nil {
		return nil, err
	}
	n := p.Max
	switch {
	case n == 0:
		n = gitLogDefault
	case n < 0 || n > gitLogMax:
		return nil, fmt.Errorf("max muss zwischen 1 und %d liegen", gitLogMax)
	}
	args := []string{"--literal-pathspecs", "log", "--no-color", "--no-ext-diff", "--no-textconv", "--no-notes",
		"--date=short", "--abbrev=12", "--format=%h %ad %an: %s", fmt.Sprintf("--max-count=%d", n)}
	if p.Pickaxe != "" {
		if len(p.Pickaxe) > gitPickaxeMax || !utf8.ValidString(p.Pickaxe) || strings.ContainsAny(p.Pickaxe, "\x00\n\r") {
			return nil, fmt.Errorf("pickaxe ist zu lang oder enthält Steuerzeichen")
		}
		args = append(args, "-S"+p.Pickaxe, "--pickaxe-all")
		// -S takes the text literally; --pickaxe-regex is never set.
	}
	args = append(args, "HEAD", "--")
	if rel != "" {
		args = append(args, rel)
	}
	return args, nil
}

// ShowArgs builds the git command line of the show tool.
func ShowArgs(p GitShowParams) ([]string, error) {
	rev, err := CheckRevision(p.Commit)
	if err != nil {
		return nil, err
	}
	rel, err := CleanGitPath(p.Path)
	if err != nil {
		return nil, err
	}
	args := []string{"--literal-pathspecs", "show", "--no-color", "--no-ext-diff", "--no-textconv", "--no-notes",
		"--date=short", "--format=commit %H%nAutor: %an%nDatum: %ad%n%n%s%n%n%b", "--stat", "--patch", rev, "--"}
	if rel != "" {
		args = append(args, rel)
	}
	return args, nil
}

func (r *Repo) runTool(ctx context.Context, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitToolTimeout)
	defer cancel()
	full := append(append([]string{}, safeGitConfig(r.Root)...), "-C", r.Root)
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = SafeGitEnv()
	out, err := cmd.Output()
	if ee, ok := err.(*exec.ExitError); ok {
		return "", fmt.Errorf("git: %s", strings.TrimSpace(string(ee.Stderr)))
	}
	if err != nil {
		return "", err
	}
	s := string(out)
	if len(s) > gitOutMax {
		s = strings.ToValidUTF8(s[:gitOutMax], "") + "\n[Ausgabe gekürzt]"
	}
	return s, nil
}

// GitLog runs the log tool.
func (r *Repo) GitLog(ctx context.Context, p GitLogParams) (string, error) {
	args, err := LogArgs(p)
	if err != nil {
		return "", err
	}
	return r.runTool(ctx, args)
}

// GitShow runs the show tool.
func (r *Repo) GitShow(ctx context.Context, p GitShowParams) (string, error) {
	args, err := ShowArgs(p)
	if err != nil {
		return "", err
	}
	return r.runTool(ctx, args)
}
