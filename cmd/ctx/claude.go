package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// agentIDPrefix ist das Provider-Präfix jeder Launcher-Identität; ctx mcp
// übernimmt GHOSTTREE_AGENT_ID nur mit dem Präfix der erkannten Harness.
const agentIDPrefix = "claude:"

const (
	// agentIDEnv trägt die Koordinationsidentität eines per `ctx claude`
	// gestarteten Claude. ctx mcp und ctx channel lesen sie beide.
	agentIDEnv = "GHOSTTREE_AGENT_ID"
	// agentRoleEnv trägt die mit --role angeforderte Rolle (lead, member,
	// guest) zu ctx channel und ctx mcp. Der Server kappt sie live am Rang des
	// Kontos im Projekt, höchstens lead.
	agentRoleEnv = "GHOSTTREE_AGENT_ROLE"
	// sessionIDEnv trägt die Session-UUID, die der Launcher Claude Code per
	// --session-id vorgibt. ctx mcp und ctx channel melden sie bei der
	// Anmeldung; nur über sie lässt sich beobachtete Aktivität einem Agenten
	// zuordnen (Presence, REQ-360).
	sessionIDEnv = "GHOSTTREE_SESSION_ID"
	// claudeDryRunEnv entspricht --dry-run.
	claudeDryRunEnv = "GHOSTTREE_CLAUDE_DRY_RUN"
	// claudeBinEnv überschreibt das aufgerufene claude-Programm.
	claudeBinEnv = "GHOSTTREE_CLAUDE_BIN"
)

const claudeUsage = `usage: ctx claude [--dry-run] [--agent <identity>] [--role lead|member|guest] [claude args...]

Starts Claude Code with the ghosttree channel loaded, so coordination messages
reach the session (a waiting session wakes up, a working one gets the message
at its next tool result). The identity is generated per launch
(claude:<host>:<uuid>) unless --agent names one; that
identity must start with "claude:" or the launcher refuses it. --role asks for
the agent's role in the project (default member). The server applies
min(requested, your own rank in the project), never above lead, and recomputes
it live: demoting your account demotes your agents. Everything after the
launcher's own flags goes to
claude unchanged. Set GHOSTTREE_CLAUDE_DRY_RUN=1 to print instead of start.

This is the only way in: the channel cannot be attached to a running session,
and 'ctx install' does not register it. Claude Code will ask you to confirm
--dangerously-load-development-channels at every start.`

// newAgentID erzeugt die eine Identität dieses Launches, im Stil der übrigen
// Kennungen (Anbieter:Maschine:Kennung).
func newAgentID(machine string) (string, error) {
	u, err := newUUID()
	if err != nil {
		return "", err
	}
	if machine == "" {
		machine = "unknown"
	}
	return "claude:" + machine + ":" + u, nil
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40 // UUID v4
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:]), nil
}

// sessionFor entscheidet die Session-UUID dieses Starts. Gibt der Aufrufer
// selbst eine Session vor oder setzt eine fort, ist die UUID nicht unsere
// Wahl: eine eigene Vorgabe wird übernommen, bei --resume und Verwandten ist
// sie unbekannt (leer) und der Agent meldet keine. Dann bleibt "arbeitet"
// unbekannt, statt zu raten.
func sessionFor(extra []string) (session string, pass bool, err error) {
	for i, a := range extra {
		switch a {
		case "--session-id":
			if i+1 < len(extra) {
				return extra[i+1], false, nil
			}
			return "", false, nil
		case "--resume", "-r", "--continue", "-c", "--from-pr":
			return "", false, nil
		}
		if v, ok := strings.CutPrefix(a, "--session-id="); ok {
			return v, false, nil
		}
		if strings.HasPrefix(a, "--resume=") {
			return "", false, nil
		}
	}
	session, err = newUUID()
	return session, err == nil, err
}

// claudeMCPConfig ist der Inhalt der temporären --mcp-config-Datei. Nur der
// Channel steht darin: das ohnehin registrierte ctx mcp erbt die Identität über
// die Umgebung des Claude-Prozesses.
func claudeMCPConfig(exe, agent, role string) ([]byte, error) {
	env := map[string]string{agentIDEnv: agent}
	if role != "" {
		env[agentRoleEnv] = role
	}
	cfg := map[string]any{"mcpServers": map[string]any{
		channelServerName: map[string]any{
			"command": exe,
			"args":    []string{"channel", "--agent", agent},
			"env":     env,
		},
	}}
	return json.MarshalIndent(cfg, "", "  ")
}

// claudeArgs ist die Kommandozeile hinter dem Programmnamen.
func claudeArgs(cfgPath string, extra []string) []string {
	args := []string{"--mcp-config", cfgPath, "--dangerously-load-development-channels", "server:" + channelServerName}
	return append(args, extra...)
}

// claudeLaunchArgs ergänzt --session-id, wenn der Launcher die Session
// vorgibt.
func claudeLaunchArgs(cfgPath, session string, pass bool, extra []string) []string {
	if !pass || session == "" {
		return claudeArgs(cfgPath, extra)
	}
	return claudeArgs(cfgPath, append([]string{"--session-id", session}, extra...))
}

func claudeProgram() string {
	if bin := os.Getenv(claudeBinEnv); bin != "" {
		return bin
	}
	return "claude"
}

func quoteArgs(prog string, args []string) string {
	parts := []string{prog}
	for _, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\n'\"\\$`") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

func cmdClaude(args []string, stdout io.Writer) int {
	dry := os.Getenv(claudeDryRunEnv) != "" && os.Getenv(claudeDryRunEnv) != "0"
	// Nur führende Launcher-Flags; alles ab dem ersten anderen Argument gehört
	// claude.
	presetAgent := ""
	role := ""
	for len(args) > 0 {
		switch {
		case args[0] == "--dry-run":
			dry = true
			args = args[1:]
			continue
		case args[0] == "--":
			args = args[1:] // beendet die Launcher-Flags und wird verbraucht
		case args[0] == "--agent":
			if len(args) < 2 || strings.TrimSpace(args[1]) == "" || strings.HasPrefix(args[1], "-") {
				fmt.Fprintln(stdout, "--agent needs an identity")
				return 2
			}
			if !strings.HasPrefix(args[1], agentIDPrefix) {
				fmt.Fprintf(stdout, "--agent %q must start with %q: ctx mcp ignores other identities, and the channel and coord tools would split\n", args[1], agentIDPrefix)
				return 2
			}
			presetAgent = args[1]
			args = args[2:]
			continue
		case args[0] == "--role":
			if len(args) < 2 || !store.ValidAgentRole(args[1]) || args[1] == "" {
				fmt.Fprintln(stdout, "--role needs lead, member or guest (agents are never owner)")
				return 2
			}
			role = args[1]
			args = args[2:]
			continue
		case args[0] == "-h" || args[0] == "--help":
			fmt.Fprintln(stdout, claudeUsage)
			return 0
		}
		break
	}
	machine := ""
	if cfg, err := config.Load(); err == nil {
		machine = cfg.Machine
	}
	agent := presetAgent
	if agent == "" {
		var err error
		if agent, err = newAgentID(machine); err != nil {
			fmt.Fprintf(os.Stderr, "claude: %v\n", err)
			return 1
		}
	}
	session, passSession, err := sessionFor(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "claude: %v\n", err)
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "claude: locate ctx binary: %v\n", err)
		return 1
	}
	conf, err := claudeMCPConfig(exe, agent, role)
	if err != nil {
		fmt.Fprintf(os.Stderr, "claude: %v\n", err)
		return 1
	}
	if dry {
		fmt.Fprintf(stdout, "agent: %s\ncommand: %s\nenv: %s=%s\n",
			agent, quoteArgs(claudeProgram(), claudeLaunchArgs("<tmp>", session, passSession, args)), agentIDEnv, agent)
		fmt.Fprintf(stdout, "env: %s=%s\n", sessionIDEnv, session)
		if role != "" {
			fmt.Fprintf(stdout, "env: %s=%s\n", agentRoleEnv, role)
		}
		fmt.Fprintf(stdout, "mcp-config:\n%s\n", conf)
		return 0
	}
	return runClaude(conf, agent, role, session, passSession, args)
}

// runClaude startet claude als Kindprozess statt per exec, weil die temporäre
// Konfiguration danach weg muss.
func runClaude(conf []byte, agent, role, session string, passSession bool, extra []string) int {
	f, err := os.CreateTemp("", "ghosttree-claude-*.json") // 0600
	if err != nil {
		fmt.Fprintf(os.Stderr, "claude: %v\n", err)
		return 1
	}
	defer os.Remove(f.Name())
	_, werr := f.Write(conf)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		fmt.Fprintf(os.Stderr, "claude: write config: %v\n", werr)
		return 1
	}

	cmd := exec.Command(claudeProgram(), claudeLaunchArgs(f.Name(), session, passSession, extra)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// Die Session-UUID geht nur mit, wenn sie sicher ist; eine geerbte aus einem
	// früheren Launch wird überschrieben, nicht weitergereicht.
	cmd.Env = append(os.Environ(), agentIDEnv+"="+agent, sessionIDEnv+"="+session)
	if role != "" {
		cmd.Env = append(cmd.Env, agentRoleEnv+"="+role)
	} else {
		// Eine geerbte Rolle eines anderen Agenten gilt hier nicht.
		cmd.Env = append(cmd.Env, agentRoleEnv+"=")
	}

	// Der Launcher fängt die Signale ab, damit er überlebt und aufräumt, und
	// reicht sie an claude weiter. Vom Terminal kann claude dasselbe Signal
	// zusätzlich selbst bekommen; im Raw-Modus von claude entsteht keins.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "claude: %v\n", err)
		return 127
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				_ = cmd.Process.Signal(s)
			case <-done:
				return
			}
		}
	}()
	err = cmd.Wait()
	close(done)
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	fmt.Fprintf(os.Stderr, "claude: %v\n", err)
	return 1
}
