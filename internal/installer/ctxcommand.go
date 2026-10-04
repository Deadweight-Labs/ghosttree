package installer

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ctxCommand is how harness configs and services name this binary. The default
// is the bare command so library callers and tests keep their behaviour; the
// ctx command sets the absolute path of the running binary before installing,
// because a bare name only works while ~/.local/bin is on the PATH of every
// process the harness starts, and on macOS it usually is not.
var ctxCommand = "ctx"

// SetCtxCommand sets the command written into hooks, MCP entries and services.
// An empty value restores the bare default.
func SetCtxCommand(path string) {
	if path == "" {
		path = "ctx"
	}
	ctxCommand = path
}

// CtxCommand returns the command currently written into configurations.
func CtxCommand() string { return ctxCommand }

// ErrTransientCtx is returned when the running binary lives in a temporary
// directory or a Go build cache: a hook or unit that points there breaks as
// soon as the directory is cleaned.
var ErrTransientCtx = errors.New("this ctx binary runs from a temporary location (a temp directory or the Go build cache), so no hook or service may point to it. Install ctx to a permanent place (for example ~/.local/bin/ctx) and run the command again from there")

// transientRoots lists the directories whose content is not meant to last. It
// is a seam: tests create their binaries below the temp directory.
var transientRoots = func() []string { return []string{os.TempDir(), "/tmp", "/var/tmp"} }

// isTransientPath reports whether p lies below a temporary directory (also
// after resolving symlinks, as /tmp is /private/tmp on macOS) or inside a Go
// build cache.
func isTransientPath(p string) bool {
	variants := []string{filepath.Clean(p)}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		variants = append(variants, r)
	}
	for _, v := range variants {
		for _, part := range strings.Split(filepath.ToSlash(v), "/") {
			if strings.HasPrefix(part, "go-build") {
				return true
			}
		}
		for _, root := range transientRoots() {
			if root == "" {
				continue
			}
			roots := []string{filepath.Clean(root)}
			if r, err := filepath.EvalSymlinks(root); err == nil {
				roots = append(roots, r)
			}
			for _, r := range roots {
				if v == r || strings.HasPrefix(v, strings.TrimRight(r, string(filepath.Separator))+string(filepath.Separator)) {
					return true
				}
			}
		}
	}
	return false
}

// ResolveCtxExecutable returns the path under which the running binary should
// be written into configuration. The real file wins only if no stable name
// points at it: ~/.local/bin/ctx survives an upgrade by atomic rename while a
// resolved target such as a versioned Homebrew cellar path does not. A binary
// in a temporary location is never returned; ErrTransientCtx says so, unless
// a permanent ctx exists at a known place.
func ResolveCtxExecutable(home string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return stableCtxPath(exe, home, os.Getenv("XDG_BIN_HOME"))
}

func stableCtxPath(exe, home, xdgBin string) (string, error) {
	real := exe
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		real = r
	}
	realInfo, statErr := os.Stat(real)
	var candidates []string
	if xdgBin != "" && filepath.IsAbs(xdgBin) {
		candidates = append(candidates, filepath.Join(xdgBin, "ctx"))
	}
	if home != "" {
		candidates = append(candidates, filepath.Join(home, ".local", "bin", "ctx"))
	}
	if p, err := exec.LookPath("ctx"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			candidates = append(candidates, abs)
		}
	}
	if isTransientPath(exe) || isTransientPath(real) {
		// Fall back to a permanent ctx that is really there.
		for _, c := range candidates {
			if info, err := os.Stat(c); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 && !isTransientPath(c) {
				return c, nil
			}
		}
		return "", ErrTransientCtx
	}
	if statErr != nil {
		return real, nil
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && os.SameFile(info, realInfo) && !isTransientPath(c) {
			return c, nil
		}
	}
	return real, nil
}

// quoteCommandWord leaves ordinary paths bare and wraps the rest in double
// quotes, the form both JSON hook commands (run through a shell) and the
// installer's own splitter understand.
func quoteCommandWord(w string) string {
	if w != "" && !strings.ContainsAny(w, " \t\"'\\$`&|;<>()*?[]{}#~!") {
		return w
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`")
	return `"` + r.Replace(w) + `"`
}

// ctxRun is the command a person should type to run this binary again: the
// absolute path when one is configured, because the bare name is exactly what
// fails when the PATH lacks its directory.
// CtxCommandForHumans is ctxRun for callers outside the package.
func CtxCommandForHumans() string { return ctxRun() }

func ctxRun() string { return quoteCommandWord(ctxCommand) }

// withCtx replaces the leading "ctx" word of a bare command with the
// configured one.
func withCtx(bare string) string {
	rest, ok := strings.CutPrefix(bare, "ctx ")
	if !ok || ctxCommand == "ctx" {
		return bare
	}
	return quoteCommandWord(ctxCommand) + " " + rest
}

// splitCommand splits a command line on spaces, honouring double quotes and
// backslash escapes inside them.
func splitCommand(s string) []string {
	var out []string
	var b strings.Builder
	inWord, inQuote := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote && c == '\\' && i+1 < len(s):
			i++
			b.WriteByte(s[i])
		case c == '"':
			inQuote, inWord = !inQuote, true
		case !inQuote && (c == ' ' || c == '\t'):
			if inWord {
				out = append(out, b.String())
				b.Reset()
				inWord = false
			}
		default:
			b.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		out = append(out, b.String())
	}
	return out
}

// ctxSubcommand returns the arguments after the ctx word when the command runs
// a binary named ctx (bare or by any path), so that an entry written earlier
// with another path is recognised as ours without touching foreign tools.
func ctxSubcommand(command string) (string, bool) {
	words := splitCommand(command)
	if len(words) == 0 || filepath.Base(words[0]) != "ctx" {
		return "", false
	}
	return strings.Join(words[1:], " "), true
}

// isCtxHook reports whether the command is one of our lifecycle hooks.
func isCtxHook(command string) bool {
	rest, ok := ctxSubcommand(command)
	return ok && strings.HasPrefix(rest, "hook ")
}
