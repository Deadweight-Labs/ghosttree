package agentbench

import (
	"context"
	"strings"
	"testing"
)

func kinds(findings []LeakageFinding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Kind)
	}
	return out
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func TestEvaluateProbeReportsAToolTheArmMayNotSee(t *testing.T) {
	probe := "tool ctx /opt/arm/ghosttree/bin/ctx\n" +
		"path /opt/arm/common/bin:/usr/bin:/bin:/opt/arm/ghosttree/bin\n"

	findings := evaluateProbe(ArmBare, AllowedSurface{}, probe)

	if !contains(kinds(findings), "visible_tool") {
		t.Fatalf("bare must not be able to resolve ctx: %+v", findings)
	}
}

func TestEvaluateProbeAcceptsTheArmsOwnTool(t *testing.T) {
	probe := "tool ctx /opt/arm/ghosttree/bin/ctx\npath /usr/bin\n"

	findings := evaluateProbe(ArmGhosttree, AllowedSurface{GhostTree: true}, probe)

	if contains(kinds(findings), "visible_tool") {
		t.Fatalf("the ghosttree arm is entitled to ctx: %+v", findings)
	}
}

func TestEvaluateProbeReportsAForeignMemoryTool(t *testing.T) {
	probe := "tool claude-mem /opt/arm/claude-mem/bin/claude-mem\npath /usr/bin\n"

	findings := evaluateProbe(ArmGhosttree, AllowedSurface{GhostTree: true}, probe)

	if !contains(kinds(findings), "visible_tool") {
		t.Fatalf("a competing memory tool must be reported: %+v", findings)
	}
}

func TestEvaluateProbeReportsAForeignWorker(t *testing.T) {
	probe := "path /usr/bin\nproc 42 claude-mem-worker\n"

	findings := evaluateProbe(ArmGhosttree, AllowedSurface{GhostTree: true}, probe)

	if !contains(kinds(findings), "running_process") {
		t.Fatalf("a foreign memory worker must be reported: %+v", findings)
	}
}

func TestEvaluateProbeIgnoresTheAgentsOwnProcesses(t *testing.T) {
	probe := "path /usr/bin\nproc 1 /usr/local/bin/entrypoint.sh\nproc 7 ps\n"

	findings := evaluateProbe(ArmBare, AllowedSurface{}, probe)

	if contains(kinds(findings), "running_process") {
		t.Fatalf("the container's own entrypoint is not a leak: %+v", findings)
	}
}

func TestEvaluateProbeReportsAnOpenPort(t *testing.T) {
	probe := "path /usr/bin\nport 3113\n"

	findings := evaluateProbe(ArmBare, AllowedSurface{}, probe)

	if !contains(kinds(findings), "open_port") {
		t.Fatalf("a listening port means a worker is running: %+v", findings)
	}
}

func TestEvaluateProbeReportsAnEmptyProbe(t *testing.T) {
	findings := evaluateProbe(ArmBare, AllowedSurface{}, "")

	if !contains(kinds(findings), "probe_failed") {
		t.Fatalf("a probe that produced nothing must not read as a clean bill: %+v", findings)
	}
}

func TestProbeScriptCoversEveryKnownTool(t *testing.T) {
	for _, tool := range probedTools {
		if !strings.Contains(probeScript, tool) {
			t.Fatalf("the probe script must look for %q", tool)
		}
	}
}

func TestCheckRuntimeLeakageRunsTheProbeThroughTheRuntime(t *testing.T) {
	ws := Workspace{Root: t.TempDir(), Env: map[string]string{"PATH": "/usr/bin:/bin"}}
	ws.Repo = ws.Root

	findings, err := CheckRuntimeLeakage(context.Background(), LocalRuntime{}, ws, ArmBare, AllowedSurface{})
	if err != nil {
		t.Fatalf("the probe must run: %v", err)
	}
	// Auf der Wirtsmaschine ist das Ergebnis nicht vorhersagbar; gefordert ist
	// nur, dass die Sonde ueberhaupt lief und ein PATH zurueckkam.
	if contains(kinds(findings), "probe_failed") {
		t.Fatalf("the probe produced no output at all: %+v", findings)
	}
}

func TestEvaluateProbeReportsAnMCPServerTheArmMayNotSee(t *testing.T) {
	probe := "path /usr/bin\nhome /work/home\ncwd /work/repo\n" +
		"mcp /work/home/.claude.json ghosttree\n"

	findings := evaluateProbe(ArmBare, AllowedSurface{}, probe)

	if !contains(kinds(findings), "mcp_server") {
		t.Fatalf("an MCP server is a tool the arm did not earn: %+v", findings)
	}
}

func TestEvaluateProbeAcceptsGhosttreesOwnMCPServer(t *testing.T) {
	probe := "path /usr/bin\nhome /work/home\ncwd /work/repo\n" +
		"mcp /work/home/.claude.json ghosttree\n"

	findings := evaluateProbe(ArmGhosttree, AllowedSurface{GhostTree: true}, probe)

	if contains(kinds(findings), "mcp_server") {
		t.Fatalf("the ghosttree arm is entitled to its own server: %+v", findings)
	}
}

func TestEvaluateProbeReportsAWritableToolDirectory(t *testing.T) {
	// Ein beschreibbares /opt/arm hiesse: der Agent kann die Allowlist
	// aendern, die seinen Arm definiert.
	probe := "path /usr/bin\nhome /work/home\ncwd /work/repo\nwritable /opt/arm\n"

	findings := evaluateProbe(ArmGhosttree, AllowedSurface{GhostTree: true}, probe)

	if !contains(kinds(findings), "writable_path") {
		t.Fatalf("a writable tool directory breaks the arm boundary: %+v", findings)
	}
}

func TestEvaluateProbeAcceptsAWritableWorkspace(t *testing.T) {
	probe := "path /usr/bin\nhome /work/home\ncwd /work/repo\n" +
		"writable /work/home\nwritable /work/repo/sub\nwritable /tmp\n"

	findings := evaluateProbe(ArmBare, AllowedSurface{}, probe)

	if contains(kinds(findings), "writable_path") {
		t.Fatalf("the agent must be able to work: %+v", findings)
	}
}

func TestEvaluateProbeReportsAnOpenNetwork(t *testing.T) {
	probe := "path /usr/bin\nhome /work/home\ncwd /work/repo\n" +
		"net forbidden example.com 200\nnet model api.anthropic.com 401\n"

	findings := evaluateProbe(ArmBare, AllowedSurface{}, probe)

	if !contains(kinds(findings), "network_open") {
		t.Fatalf("a run that reaches the open web can research its way around a missing memory: %+v", findings)
	}
}

func TestEvaluateProbeAcceptsASealedNetwork(t *testing.T) {
	// 403 ist die Antwort des Proxys auf eine nicht gelistete Domain.
	probe := "path /usr/bin\nhome /work/home\ncwd /work/repo\n" +
		"net forbidden example.com 403\nnet model api.anthropic.com 401\n"

	findings := evaluateProbe(ArmBare, AllowedSurface{}, probe)

	if len(findings) != 0 {
		t.Fatalf("a sealed network that reaches the model is exactly right: %+v", findings)
	}
}

func TestEvaluateProbeReportsAnUnreachableModel(t *testing.T) {
	probe := "path /usr/bin\nhome /work/home\ncwd /work/repo\n" +
		"net forbidden example.com 000\nnet model api.anthropic.com 000\n"

	findings := evaluateProbe(ArmBare, AllowedSurface{}, probe)

	if !contains(kinds(findings), "model_unreachable") {
		t.Fatalf("a seal that also blocks the model fails every run: %+v", findings)
	}
}

func TestEvaluateProbeReportsAMissingProbeTool(t *testing.T) {
	probe := "path /usr/bin\nhome /work/home\ncwd /work/repo\nmcpfail jq-missing\n"

	findings := evaluateProbe(ArmBare, AllowedSurface{}, probe)

	if !contains(kinds(findings), "probe_failed") {
		t.Fatalf("a probe that could not look is not a probe that found nothing: %+v", findings)
	}
}

func TestEvaluateProbeIgnoresDockersOwnResolver(t *testing.T) {
	// 127.0.0.11 ist Dockers eingebetteter DNS. Meldete die Sonde ihn,
	// scheiterte jede Kampagne an ihrer eigenen Pruefung.
	probe := "path /usr/bin\nhome /work/home\ncwd /work/repo\nport 127.0.0.11:36557\n"

	findings := evaluateProbe(ArmBare, AllowedSurface{}, probe)

	if contains(kinds(findings), "open_port") {
		t.Fatalf("the platform's resolver is not a memory worker: %+v", findings)
	}
}

func TestEvaluateProbeStillReportsALocalWorkerPort(t *testing.T) {
	probe := "path /usr/bin\nhome /work/home\ncwd /work/repo\nport 127.0.0.1:8765\n"

	findings := evaluateProbe(ArmBare, AllowedSurface{}, probe)

	if !contains(kinds(findings), "open_port") {
		t.Fatalf("a worker on loopback must still be caught: %+v", findings)
	}
}

func TestEvaluateProbeReportsAMissingProcessTool(t *testing.T) {
	probe := "path /usr/bin\nhome /work/home\ncwd /work/repo\nprocfail ps-missing\n"

	findings := evaluateProbe(ArmBare, AllowedSurface{}, probe)

	if !contains(kinds(findings), "probe_failed") {
		t.Fatalf("a probe without ps found no processes because it could not look: %+v", findings)
	}
}
