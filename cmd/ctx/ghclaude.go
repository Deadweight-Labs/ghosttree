package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/installer"
)

// ghclaudeName ist der Aufrufname, unter dem ctx claude mit Channel startet.
const ghclaudeName = "ghclaude"

// Austauschbar für Tests.
var (
	ghclaudeExec = syscall.Exec
	installTTY   = openDevTTY
)

// invokedAsGhclaude: ctx erkennt am Basisnamen von argv[0], dass es als
// ghclaude gestartet wurde (Symlink auf ctx).
func invokedAsGhclaude(argv0 string) bool {
	return strings.TrimSuffix(filepath.Base(argv0), ".exe") == ghclaudeName
}

func sameFile(a, b string) bool {
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}

// findClaude sucht das claude-Programm wie ctx claude (GHOSTTREE_CLAUDE_BIN,
// sonst PATH). Ein T3-Prozess hat oft ein PATH ohne ~/.local/bin, in dem
// ghclaude und claude liegen; deshalb wird zusätzlich neben ghclaude gesucht.
// Der eigene Binärpfad zählt nie: ghclaude darf sich nicht selbst starten.
func findClaude(argv0 string) (string, error) {
	self, _ := os.Executable()
	ok := func(p string) bool {
		if p == "" || sameFile(p, self) {
			return false
		}
		st, err := os.Stat(p)
		return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
	}
	name := claudeProgram()
	if strings.Contains(name, "/") {
		if ok(name) {
			return name, nil
		}
		return "", fmt.Errorf("%s is not an executable file", name)
	}
	if p, err := exec.LookPath(name); err == nil && ok(p) {
		return p, nil
	}
	var dirs []string
	if strings.Contains(argv0, "/") {
		dirs = append(dirs, filepath.Dir(argv0))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}
	for _, d := range dirs {
		if p := filepath.Join(d, name); ok(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found in PATH (set %s to its path)", name, claudeBinEnv)
}

// runGhclaude startet claude mit dem Channel und reicht alle Argumente
// unverändert durch. Es ersetzt den Prozess (exec): Exit-Code, Signale und
// stdin/stdout für stream-json gehören damit claude selbst. Die MCP-Konfiguration
// geht deshalb inline als JSON mit, nicht als Datei, die jemand wegräumen müsste.
// Es kehrt nur bei einem Fehler zurück.
func runGhclaude(argv0 string, args []string, stdout io.Writer) int {
	machine := ""
	if cfg, err := config.Load(); err == nil {
		machine = cfg.Machine
	}
	agent, err := newAgentID(machine)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ghclaude: %v\n", err)
		return 1
	}
	session, pass, err := sessionFor(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ghclaude: %v\n", err)
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ghclaude: locate ctx binary: %v\n", err)
		return 1
	}
	conf, err := claudeMCPConfig(exe, agent, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ghclaude: %v\n", err)
		return 1
	}
	full := claudeLaunchArgs(string(conf), session, pass, args)
	if v := os.Getenv(claudeDryRunEnv); v != "" && v != "0" {
		fmt.Fprintf(stdout, "agent: %s\ncommand: %s\n", agent, quoteArgs(claudeProgram(), full))
		return 0
	}
	path, err := findClaude(argv0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ghclaude: %v\n", err)
		return 127
	}
	os.Setenv(agentIDEnv, agent)
	os.Setenv(sessionIDEnv, session)
	// Eine geerbte Rolle eines anderen Agenten gilt hier nicht.
	os.Setenv(agentRoleEnv, "")
	err = ghclaudeExec(path, append([]string{path}, full...), os.Environ())
	fmt.Fprintf(os.Stderr, "ghclaude: exec %s: %v\n", path, err)
	return 126
}

// ---- Symlink anlegen, prüfen, entfernen ----

// ghclaudeLink liefert den Pfad des Symlinks (im bin-Verzeichnis des stabilen
// ctx-Pfads) und sein Ziel, den Namen von ctx daneben. Das Ziel ist relativ,
// damit der Link ein Update von ctx am selben Pfad und ein Verschieben des
// Verzeichnisses übersteht.
func ghclaudeLink(home string) (link, target, ctxPath string, err error) {
	ctxPath, err = installCtxExecutable(home)
	if err != nil {
		return "", "", "", err
	}
	if !filepath.IsAbs(ctxPath) {
		return "", "", "", fmt.Errorf("ctx path %q is not absolute", ctxPath)
	}
	return filepath.Join(filepath.Dir(ctxPath), ghclaudeName), filepath.Base(ctxPath), ctxPath, nil
}

// ensureGhclaude legt den Symlink an; ein vorhandener, richtiger bleibt, ein
// falscher Symlink wird ersetzt, eine fremde Datei nie angefasst.
func ensureGhclaude(home string) (action, link string, err error) {
	link, target, ctxPath, err := ghclaudeLink(home)
	if err != nil {
		return "", "", err
	}
	st, lerr := os.Lstat(link)
	switch {
	case lerr == nil && st.Mode()&os.ModeSymlink == 0:
		return "", link, fmt.Errorf("%s exists and is not a link; left alone", link)
	case lerr == nil:
		if sameFile(link, ctxPath) {
			return "unchanged", link, nil
		}
		if err := os.Remove(link); err != nil {
			return "", link, err
		}
		action = "updated"
	case errors.Is(lerr, os.ErrNotExist):
		action = "created"
	default:
		return "", link, lerr
	}
	if err := os.Symlink(target, link); err != nil {
		return "", link, err
	}
	return action, link, nil
}

// removeGhclaude löscht den Symlink, aber nur einen Link; eine fremde Datei
// gleichen Namens bleibt.
func removeGhclaude(home string) (removed bool, link string, err error) {
	link, _, _, err = ghclaudeLink(home)
	if err != nil {
		return false, "", err
	}
	st, lerr := os.Lstat(link)
	if errors.Is(lerr, os.ErrNotExist) {
		return false, link, nil
	}
	if lerr != nil {
		return false, link, lerr
	}
	if st.Mode()&os.ModeSymlink == 0 {
		return false, link, fmt.Errorf("%s is not a link; left alone", link)
	}
	return true, link, os.Remove(link)
}

// ghclaudeCheck prüft für ctx doctor, ob der Symlink auf dieses ctx zeigt.
// Ohne Symlink ist das kein Fehler: ghclaude ist freiwillig.
func ghclaudeCheck(home string) installer.Check {
	c := installer.Check{Name: "ghclaude"}
	link, _, ctxPath, err := ghclaudeLink(home)
	if err != nil {
		c.Unverified, c.Detail = true, "not checked: "+err.Error()
		return c
	}
	st, lerr := os.Lstat(link)
	switch {
	case lerr != nil && errors.Is(lerr, os.ErrNotExist):
		c.Unverified, c.Detail = true, "not installed (optional): "+link
		c.Fix = "run '" + shellWord(ctxPath) + " install ghclaude'"
	case lerr != nil:
		c.Detail = lerr.Error()
	case st.Mode()&os.ModeSymlink == 0:
		c.Detail = link + " is not a link to ctx"
	case !sameFile(link, ctxPath):
		c.Detail = link + " does not lead to " + ctxPath
		c.Fix = "run '" + shellWord(ctxPath) + " install ghclaude'"
	default:
		c.OK, c.Detail = true, link
	}
	return c
}

// ---- Rückfrage und Meldungen ----

const ghclaudeQuestion = "Agents can be woken by messages while idle. Install ghclaude? [Y/n] "

// ghclaudeNote sagt ehrlich, wo ghclaude weckt: gemessen mit Claude Code
// 2.1.296 (2026-10-16), siehe README.
const ghclaudeNote = "Wakes an idle agent in an interactive terminal session. A non-interactive claude (-p, the Agent SDK as used by T3) ignores the channel, so messages do not wake it there."

func printGhclaude(stdout io.Writer, action, link string) {
	fmt.Fprintf(stdout, "%-12s %s\n", action, link)
	fmt.Fprintf(stdout, "ghclaude: start Claude Code with the channel via %s (in T3, enter it as the Claude executable). %s\n", shellWord(link), ghclaudeNote)
}

// offerGhclaude fragt einmal, ob ghclaude angelegt werden soll. Ist der Link
// schon richtig, entfällt die Frage. Ohne Terminal wird nichts angelegt und nur
// auf den Befehl hingewiesen. tty darf nil sein.
func offerGhclaude(ctx context.Context, stdout io.Writer, tty terminal, home string) {
	link, _, ctxPath, err := ghclaudeLink(home)
	if err != nil {
		fmt.Fprintf(stdout, "ghclaude    not offered: %v\n", err)
		return
	}
	if st, err := os.Lstat(link); err == nil && st.Mode()&os.ModeSymlink != 0 && sameFile(link, ctxPath) {
		printGhclaude(stdout, "unchanged", link)
		return
	}
	if tty == nil {
		fmt.Fprintf(stdout, "ghclaude    not installed (no terminal to ask); install it with: %s install ghclaude\n", ctxHint())
		return
	}
	if !confirm(ctx, tty, ghclaudeQuestion, true) {
		return
	}
	action, link, err := ensureGhclaude(home)
	if err != nil {
		fmt.Fprintf(stdout, "ghclaude    %v\n", err)
		return
	}
	printGhclaude(stdout, action, link)
}

// installGhclaudeOffer öffnet /dev/tty für die Frage von ctx install claude.
func installGhclaudeOffer(stdout io.Writer, home string) {
	var tty terminal
	if t, err := installTTY(); err == nil {
		defer t.Close()
		tty = t
	}
	offerGhclaude(context.Background(), stdout, tty, home)
}

// cmdInstallGhclaude ist `ctx install ghclaude`: legt den Link ohne Frage an.
func cmdInstallGhclaude(stdout io.Writer, home string) int {
	action, link, err := ensureGhclaude(home)
	if err != nil {
		fmt.Fprintf(stdout, "ghclaude: %v\n", err)
		return 1
	}
	printGhclaude(stdout, action, link)
	return 0
}

const uninstallUsage = `usage: ctx uninstall ghclaude

  ghclaude  remove the ghclaude link next to ctx (only a link; a regular file
            of that name is left alone)
`

func cmdUninstall(args []string, stdout io.Writer) int {
	if len(args) != 1 || args[0] != "ghclaude" {
		fmt.Fprint(stdout, uninstallUsage)
		if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
			return 0
		}
		return 2
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stdout, "home dir: %v\n", err)
		return 1
	}
	removed, link, err := removeGhclaude(home)
	switch {
	case err != nil:
		fmt.Fprintf(stdout, "ghclaude: %v\n", err)
		return 1
	case removed:
		fmt.Fprintf(stdout, "%-12s %s\n", "removed", link)
	default:
		fmt.Fprintf(stdout, "ghclaude is not installed (%s)\n", link)
	}
	return 0
}
