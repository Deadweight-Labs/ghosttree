package agentbench

import (
	"context"
	"fmt"
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

const probeScript = `
printf 'path %s\n' "$PATH"
for t in ctx claude-mem agentmemory cognee mem0; do
  p=$(command -v "$t" 2>/dev/null) && printf 'tool %s %s\n' "$t" "$p"
done
ps -eo pid=,comm= 2>/dev/null | while read -r pid comm; do
  printf 'proc %s %s\n' "$pid" "$comm"
done
(ss -ltn 2>/dev/null || netstat -ltn 2>/dev/null) | awk 'NR>1 {print "port " $4}'
exit 0
`

// CheckRuntimeLeakage runs the probe where the agent will run. The filesystem
// check in CheckLeakage cannot see this: a tool on the PATH, a worker left
// running or a listening port are all invisible from outside the container.
func CheckRuntimeLeakage(ctx context.Context, runtime Runtime, ws Workspace, arm ArmName, allowed AllowedSurface) ([]LeakageFinding, error) {
	out, err := runtime.Command(ctx, ws, arm, []string{"sh", "-c", probeScript}).Output()
	if err != nil {
		return nil, fmt.Errorf("probe for arm %q: %w", arm, err)
	}
	return evaluateProbe(arm, allowed, string(out)), nil
}

func evaluateProbe(arm ArmName, allowed AllowedSurface, probe string) []LeakageFinding {
	var findings []LeakageFinding
	report := func(kind, detail string) {
		findings = append(findings, LeakageFinding{Arm: arm, Kind: kind, Detail: detail})
	}

	var sawPath bool
	for _, line := range strings.Split(probe, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "path":
			sawPath = true
		case "tool":
			if !toolAllowed(fields[1], allowed) {
				report("visible_tool", fmt.Sprintf("%s resolves at %s", fields[1], strings.Join(fields[2:], " ")))
			}
		case "proc":
			if len(fields) >= 3 && processIsForeign(fields[2], allowed) {
				report("running_process", fmt.Sprintf("pid %s runs %s", fields[1], fields[2]))
			}
		case "port":
			report("open_port", "a socket is listening on "+fields[1])
		}
	}
	if !sawPath {
		report("probe_failed", "the probe produced no PATH line; a silent probe is not a clean result")
	}
	return findings
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

func processIsForeign(comm string, allowed AllowedSurface) bool {
	for _, name := range probedProcesses {
		if !strings.Contains(comm, name) {
			continue
		}
		return !toolAllowed(name, allowed)
	}
	return false
}
