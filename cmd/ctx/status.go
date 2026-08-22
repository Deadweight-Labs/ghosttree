package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/installer"
)

func cmdStatus(args []string, stdout io.Writer) int {
	home, _ := os.UserHomeDir()
	ok := true

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "config      missing (%s) - run 'ctx setup'\n", config.Path())
		ok = false
	} else {
		fmt.Fprintf(stdout, "config      %s (machine %s, server %s)\n", config.Path(), cfg.Machine, cfg.ServerURL)
		if err := client.New(cfg).Health(); err != nil {
			fmt.Fprintf(stdout, "server      unreachable: %v\n", err)
			ok = false
		} else {
			fmt.Fprintln(stdout, "server      reachable")
		}
	}

	if pid, running := watchProcess(); running {
		fmt.Fprintf(stdout, "watch       running (pid %d)\n", pid)
	} else {
		fmt.Fprintln(stdout, "watch       not running")
		ok = false
	}

	st, err := collector.LoadState(collector.DefaultStatePath())
	if err == nil {
		fmt.Fprintf(stdout, "transcripts %d tracked\n", len(st.Files))
	}

	claudeCfg := installer.ClaudeUserConfigPath(home)
	claudeOK := fileContains(claudeCfg, `"ghosttree"`) &&
		fileContains(filepath.Join(home, ".claude", "settings.json"), "ctx hook session-start")
	fmt.Fprintf(stdout, "claude      %s\n", installedLabel(claudeOK))
	codexOK := fileContains(filepath.Join(home, ".codex", "config.toml"), "[mcp_servers.ghosttree]") &&
		installer.HasMarker(filepath.Join(home, ".codex", "AGENTS.md"))
	fmt.Fprintf(stdout, "codex       %s\n", installedLabel(codexOK))
	if !claudeOK || !codexOK {
		ok = false
	}

	if !ok {
		return 1
	}
	return 0
}

func installedLabel(ok bool) string {
	if ok {
		return "installed"
	}
	return "not installed"
}

func fileContains(path, needle string) bool {
	b, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(b), needle)
}

func watchProcess() (int, bool) {
	b, err := os.ReadFile(pidFilePath())
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return pid, false
	}
	// Signal 0 only checks that the process is still alive.
	return pid, p.Signal(syscall.Signal(0)) == nil
}
