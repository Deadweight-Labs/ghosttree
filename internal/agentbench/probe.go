package agentbench

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// probedTools are the memory tools an arm could discover and use. A tool that
// resolves in an arm that is not entitled to it invalidates that arm's runs,
// so the list must grow whenever a new arm is added.
var probedTools = []string{"ctx", "claude-mem", "agentmemory", "cognee", "mem0"}

// probedProcesses are process names that betray a memory worker running
// alongside the agent. The container's own entrypoint and the probe itself
// are not leaks and are filtered by name.
var probedProcesses = []string{"claude-mem", "agentmemory", "cognee", "ctx"}

// probeForbiddenURL is what the probe expects NOT to reach. Any host outside
// the allowlist would do; example.com is stable, cheap and answers 200 when it
// is reachable, so a seal that fails shows up as a plain success.
const probeForbiddenURL = "https://example.com/"

// ProbeSpec names the two endpoints the network part of the probe judges by.
// The model endpoint cannot be hard-coded: a campaign may run against the
// vendor API or against a gateway on a private address, and a probe that
// checks the wrong one either passes a sealed-off campaign or fails a good
// one.
type ProbeSpec struct {
	ModelURL     string
	ForbiddenURL string
}

func (s ProbeSpec) withDefaults() ProbeSpec {
	if s.ModelURL == "" {
		s.ModelURL = "https://api.anthropic.com/"
	}
	if s.ForbiddenURL == "" {
		s.ForbiddenURL = probeForbiddenURL
	}
	return s
}

// probeScript reports what the agent would find, not what the harness
// believes it prepared. Every line is `kind field...`; unknown kinds are
// ignored so the script can grow ahead of the evaluation.
func probeScriptFor(spec ProbeSpec) string {
	spec = spec.withDefaults()
	return `
printf 'path %s\n' "$PATH"
printf 'home %s\n' "$HOME"
printf 'cwd %s\n' "$(pwd)"

for t in ctx claude-mem agentmemory cognee mem0; do
  p=$(command -v "$t" 2>/dev/null) && printf 'tool %s %s\n' "$t" "$p"
done

if command -v ps >/dev/null 2>&1; then
  ps -eo pid=,comm= 2>/dev/null | while read -r pid comm; do
    printf 'proc %s %s\n' "$pid" "$comm"
  done
else
  printf 'procfail ps-missing\n'
fi

if command -v ss >/dev/null 2>&1; then
  ss -ltn 2>/dev/null | awk 'NR>1 {print "port " $4}'
elif command -v netstat >/dev/null 2>&1; then
  netstat -ltn 2>/dev/null | awk 'NR>2 {print "port " $4}'
else
  printf 'portfail ss-missing\n'
fi

if command -v jq >/dev/null 2>&1; then
  for f in "$HOME/.claude.json" "$HOME/.claude/settings.json" \
           "$HOME/.claude/settings.local.json" "$HOME/.config/claude/settings.json" \
           ./.mcp.json ./.claude/settings.json ./.claude/settings.local.json; do
    [ -f "$f" ] || continue
    jq -r '(.mcpServers // {}) | keys[]' "$f" 2>/dev/null | while read -r n; do
      [ -n "$n" ] && printf 'mcp %s %s\n' "$f" "$n"
    done
  done
else
  printf 'mcpfail jq-missing\n'
fi
if [ -f "$HOME/.codex/config.toml" ]; then
  grep -o '^\[mcp_servers\.[^]]*\]' "$HOME/.codex/config.toml" 2>/dev/null |
    sed 's/^\[mcp_servers\.//; s/\]$//' | while read -r n; do
      printf 'mcp %s %s\n' "$HOME/.codex/config.toml" "$n"
    done
fi

for d in / /etc /usr/bin /usr/local/bin /opt /opt/arm /opt/arm/common/bin \
         /opt/arm/ghosttree/bin /var /home; do
  [ -d "$d" ] && [ -w "$d" ] && printf 'writable %s\n' "$d"
done

probe_net() {
  code=$(curl -s -o /dev/null -m 10 -w '%{http_code}' "$2" 2>/dev/null) || code=000
  [ -n "$code" ] || code=000
  printf 'net %s %s %s\n' "$1" "$2" "$code"
}
if command -v curl >/dev/null 2>&1; then
  probe_net forbidden ` + spec.ForbiddenURL + `
  probe_net model ` + spec.ModelURL + `
else
  printf 'netfail curl-missing\n'
fi
exit 0
`
}

// CheckRuntimeLeakage runs the probe where the agent will run. The filesystem
// check in CheckLeakage cannot see this: a tool on the PATH, a worker left
// running, an MCP server the arm is not entitled to, a writable system
// directory or an open route to the internet are all invisible from outside
// the container.
func CheckRuntimeLeakage(ctx context.Context, runtime Runtime, ws Workspace, arm ArmName, allowed AllowedSurface, spec ProbeSpec) ([]LeakageFinding, error) {
	out, err := runtime.Command(ctx, ws, arm, []string{"sh", "-c", probeScriptFor(spec)}).Output()
	if err != nil {
		return nil, fmt.Errorf("probe for arm %q: %w", arm, err)
	}
	return evaluateProbe(arm, allowed, string(out)), nil
}

type probeState struct {
	sawPath bool
	home    string
	cwd     string
}

func evaluateProbe(arm ArmName, allowed AllowedSurface, probe string) []LeakageFinding {
	var findings []LeakageFinding
	report := func(kind, detail string) {
		findings = append(findings, LeakageFinding{Arm: arm, Kind: kind, Detail: detail})
	}

	var state probeState
	// Der erste Durchlauf sammelt nur home und cwd: ohne sie laesst sich nicht
	// entscheiden, ob ein beschreibbares Verzeichnis zum Arbeitsbereich gehoert.
	for _, line := range strings.Split(probe, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "path":
			state.sawPath = true
		case "home":
			state.home = fields[1]
		case "cwd":
			state.cwd = fields[1]
		}
	}

	for _, line := range strings.Split(probe, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "tool":
			if len(fields) >= 3 && !toolAllowed(fields[1], allowed) {
				report("visible_tool", fmt.Sprintf("%s resolves at %s", fields[1], strings.Join(fields[2:], " ")))
			}
		case "proc":
			if len(fields) >= 3 && processIsForeign(fields[2], allowed) {
				report("running_process", fmt.Sprintf("pid %s runs %s", fields[1], fields[2]))
			}
		case "port":
			if len(fields) >= 2 && !infrastructurePort(fields[1]) {
				report("open_port", "a socket is listening on "+fields[1])
			}
		case "mcp":
			if len(fields) >= 3 && !mcpServerAllowed(fields[2], allowed) {
				report("mcp_server", fmt.Sprintf("%s configures the MCP server %s", fields[1], fields[2]))
			}
		case "mcpfail":
			report("probe_failed", "the probe could not read MCP configuration: "+strings.Join(fields[1:], " "))
		case "procfail":
			report("probe_failed", "the probe could not list processes: "+strings.Join(fields[1:], " "))
		case "portfail":
			report("probe_failed", "the probe could not list listening sockets: "+strings.Join(fields[1:], " "))
		case "writable":
			if len(fields) >= 2 && !writableAllowed(fields[1], state) {
				report("writable_path", fields[1]+" is writable, so the agent can change its own environment")
			}
		case "net":
			if kind, detail := evaluateNet(fields, allowed); kind != "" {
				report(kind, detail)
			}
		case "netfail":
			report("probe_failed", "the probe could not test the network: "+strings.Join(fields[1:], " "))
		}
	}
	if !state.sawPath {
		report("probe_failed", "the probe produced no PATH line; a silent probe is not a clean result")
	}
	return findings
}

// evaluateNet judges one net line. The forbidden host must not answer; the
// model host must. A campaign that cannot reach the model fails every run, and
// finding that out before the first token is the point.
func evaluateNet(fields []string, allowed AllowedSurface) (kind, detail string) {
	if len(fields) < 4 {
		return "", ""
	}
	label, host := fields[1], fields[2]
	status, err := strconv.Atoi(fields[3])
	if err != nil {
		return "", ""
	}
	switch label {
	case "forbidden":
		// 000 heisst: keine Antwort. 403 und 407 kommen vom Proxy und sind
		// genau die erwartete Ablehnung. Alles andere gilt als durchgekommen,
		// auch ein 500 — im Zweifel lieber ein falscher Alarm als ein Arm, der
		// heimlich im Netz recherchiert.
		if status == 0 || status == 403 || status == 407 || allowed.OpenNetwork {
			return "", ""
		}
		return "network_open", fmt.Sprintf(
			"%s answered %d, so the run is not sealed to the model endpoint", host, status)
	case "model":
		if status == 0 {
			return "model_unreachable", fmt.Sprintf(
				"%s did not answer; every run in this arm would fail", host)
		}
	}
	return "", ""
}

// dockerEmbeddedDNS is the resolver Docker runs inside every container on a
// user-defined network. It listens, but it is the platform, not a memory
// worker — and reporting it would make every campaign fail its own probe.
// The address is fixed by Docker, so matching it exactly stays narrow: a
// worker on 127.0.0.1 is still caught.
const dockerEmbeddedDNS = "127.0.0.11:"

func infrastructurePort(socket string) bool {
	return strings.HasPrefix(socket, dockerEmbeddedDNS)
}

func toolAllowed(tool string, allowed AllowedSurface) bool {
	switch tool {
	case "ctx":
		return allowed.GhostTree
	case "claude-mem", "agentmemory":
		return allowed.MemoryDir
	default:
		return false
	}
}

// mcpServerAllowed mirrors toolAllowed for the MCP surface: ghosttree's server
// is the ghosttree arm's treatment, and every other configured server is
// something no arm in this campaign was granted.
func mcpServerAllowed(name string, allowed AllowedSurface) bool {
	if strings.Contains(strings.ToLower(name), "ghosttree") {
		return allowed.GhostTree
	}
	return false
}

// writableAllowed accepts the workspace and scratch space. Everything else —
// the tool directories above all — must be read-only, or an agent could edit
// the very allowlist that defines its arm.
func writableAllowed(dir string, state probeState) bool {
	for _, root := range []string{state.home, state.cwd, "/tmp", "/var/tmp", "/dev"} {
		if root == "" {
			continue
		}
		if dir == root || strings.HasPrefix(dir, strings.TrimSuffix(root, "/")+"/") {
			return true
		}
	}
	return false
}

func processIsForeign(comm string, allowed AllowedSurface) bool {
	for _, name := range probedProcesses {
		if !strings.Contains(comm, name) {
			continue
		}
		return !toolAllowed(name, allowed)
	}
	return false
}
