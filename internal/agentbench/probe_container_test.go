package agentbench

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// dockerImage names the image to probe. The test only runs when it is set,
// because a campaign machine has the image and a laptop running `go test`
// may not. Set AGENTBENCH_DOCKER_IMAGE=agentbench:dev to include it.
func dockerImage(t *testing.T) string {
	t.Helper()
	image := os.Getenv("AGENTBENCH_DOCKER_IMAGE")
	if image == "" {
		t.Skip("set AGENTBENCH_DOCKER_IMAGE to probe a real container")
	}
	return image
}

func containerWorkspace(t *testing.T) Workspace {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o777); err != nil {
		t.Fatal(err)
	}
	return Workspace{Root: root, Repo: repo}
}

func TestProbeInsideContainerHidesForeignToolsFromBare(t *testing.T) {
	runtime := DockerRuntime{Image: dockerImage(t)}

	findings, err := CheckRuntimeLeakage(context.Background(), runtime,
		containerWorkspace(t), ArmBare, AllowedSurface{}, ProbeSpec{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	for _, f := range findings {
		if f.Kind == "visible_tool" || f.Kind == "probe_failed" {
			t.Fatalf("the bare arm must see no memory tool: %+v", findings)
		}
	}
}

func TestProbeInsideContainerGrantsGhosttreeItsTool(t *testing.T) {
	runtime := DockerRuntime{Image: dockerImage(t)}

	findings, err := CheckRuntimeLeakage(context.Background(), runtime,
		containerWorkspace(t), ArmGhosttree, AllowedSurface{GhostTree: true}, ProbeSpec{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	for _, f := range findings {
		if f.Kind == "probe_failed" {
			t.Fatalf("the probe did not run: %+v", findings)
		}
		if f.Kind == "visible_tool" {
			t.Fatalf("ghosttree is entitled to ctx and to nothing else: %+v", findings)
		}
	}
}

func TestProbeInsideContainerCatchesAToolTheArmMayNotHave(t *testing.T) {
	runtime := DockerRuntime{Image: dockerImage(t)}

	// Derselbe Container, aber als ghosttree-Arm gestartet und als bare
	// bewertet: genau der Fehler, den ein falsch gesetztes AGENTBENCH_ARM
	// erzeugen wuerde.
	out, err := runtime.Command(context.Background(), containerWorkspace(t),
		ArmGhosttree, []string{"sh", "-c", probeScriptFor(ProbeSpec{})}).Output()
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	findings := evaluateProbe(ArmBare, AllowedSurface{}, string(out))

	var caught bool
	for _, f := range findings {
		if f.Kind == "visible_tool" {
			caught = true
		}
	}
	if !caught {
		t.Fatalf("a ctx visible to a bare arm must be caught: %+v", findings)
	}
}
