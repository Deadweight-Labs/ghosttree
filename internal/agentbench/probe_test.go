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
