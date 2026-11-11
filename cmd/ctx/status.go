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
	"github.com/Deadweight-Labs/ghosttree/internal/profile"
	ver "github.com/Deadweight-Labs/ghosttree/internal/version"
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
			if !printVersions(stdout, cfg, client.New(cfg)) {
				ok = false
			}
		}
	}

	fmt.Fprintf(stdout, "profile     %s\n", profileLabel(os.Getenv(profile.EnvVar)))

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
		fileContains(filepath.Join(home, ".claude", "settings.json"), "hook session-start")
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

// profileLabel says which profile the sessions started from this environment
// get, and where that comes from. It is the profile of the process that runs
// `ctx status`: a session that sets GHOSTTREE_PROFILE for itself is not seen
// from here.
func profileLabel(value string) string {
	p, ok := profile.Parse(value)
	switch {
	case !ok:
		return fmt.Sprintf("%s (%s=%q is no profile: lean or full; full applies)", p, profile.EnvVar, value)
	case strings.TrimSpace(value) == "":
		return fmt.Sprintf("%s (default; %s=lean for short helper runs)", p, profile.EnvVar)
	}
	return fmt.Sprintf("%s (%s)", p, profile.EnvVar)
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

// printVersions shows client and server version and the update channel side by
// side and warns when the client is older than the server or too old for it.
// It returns false only for a protocol the server no longer serves.
func printVersions(stdout io.Writer, cfg config.Config, c *client.Client) bool {
	cv := version
	info, err := c.Version()
	if err != nil {
		fmt.Fprintf(stdout, "version     client %s (%s) | server unknown (no /api/version: an older server) | channel %s\n",
			versionLabel(cv), ver.Channel(cv), cfg.UpdateChannel())
		return true
	}
	fmt.Fprintf(stdout, "version     client %s (%s, protocol %d) | server %s (%s, protocol %d) | channel %s\n",
		versionLabel(cv), ver.Channel(cv), ver.Protocol,
		versionLabel(info.Version), info.Channel, info.Protocol, cfg.UpdateChannel())
	if ver.Protocol < info.MinClient {
		fmt.Fprintf(stdout, "warning     this client speaks protocol %d, the server needs at least %d: run 'ctx update'\n", ver.Protocol, info.MinClient)
		return false
	}
	switch ver.Compare(cv, info.Version) {
	case ver.Newer:
		fmt.Fprintf(stdout, "warning     this client (%s) is older than the server (%s): run 'ctx update'\n", cv, info.Version)
	case ver.Different:
		fmt.Fprintf(stdout, "warning     client and server run different builds (%s, %s): run 'ctx update' if the server is newer\n", cv, info.Version)
	}
	return true
}

func versionLabel(v string) string { return ver.Label(v) }
