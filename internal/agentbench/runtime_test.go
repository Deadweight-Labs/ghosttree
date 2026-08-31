package agentbench

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLocalRuntimeRunsInTheWorkspaceRepo(t *testing.T) {
	ws := Workspace{Root: "/tmp/x", Repo: "/tmp/x/repo", Home: "/tmp/x/home",
		Env: map[string]string{"HOME": "/tmp/x/home", "PATH": "/usr/bin"}}

	cmd := LocalRuntime{}.Command(context.Background(), ws, ArmBare, []string{"claude", "-p", "hi"})

	if cmd.Dir != ws.Repo {
		t.Fatalf("local runs happen in the workspace repo, got %q", cmd.Dir)
	}
	if !slices.Contains(cmd.Env, "HOME=/tmp/x/home") {
		t.Fatalf("the workspace HOME must be passed through: %v", cmd.Env)
	}
	if slices.Contains(cmd.Env, "PATH="+"") {
		t.Fatalf("PATH must be set: %v", cmd.Env)
	}
}

func TestDockerRuntimeMountsTheWorkspaceAndPinsTheArm(t *testing.T) {
	ws := Workspace{Root: "/runs/pilot/ws-ghosttree", Repo: "/runs/pilot/ws-ghosttree/repo"}
	runtime := DockerRuntime{Image: "agentbench:dev"}

	cmd := runtime.Command(context.Background(), ws, ArmGhosttree, []string{"claude", "-p", "hi"})

	joined := strings.Join(cmd.Args, " ")
	for _, want := range []string{
		"docker", "run", "--rm",
		"-v " + ws.Root + ":/work",
		"-e AGENTBENCH_ARM=ghosttree",
		"agentbench:dev",
		"claude -p hi",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("docker invocation must contain %q:\n%s", want, joined)
		}
	}
	// Das Arbeitsverzeichnis im Container ist /work/repo, nicht der Wirtspfad.
	if !strings.Contains(joined, "-w /work/repo") {
		t.Fatalf("container workdir must be /work/repo:\n%s", joined)
	}
}

func TestDockerRuntimeDoesNotLeakTheHostEnvironment(t *testing.T) {
	ws := Workspace{Root: "/runs/ws", Repo: "/runs/ws/repo",
		Env: map[string]string{"HOME": "/runs/ws/home", "PATH": "/usr/bin"}}
	runtime := DockerRuntime{Image: "agentbench:dev"}

	cmd := runtime.Command(context.Background(), ws, ArmBare, []string{"claude"})

	// HOME und PATH setzt der entrypoint im Container; wuerden sie von aussen
	// durchgereicht, zeigten sie auf Wirtspfade, die es dort nicht gibt.
	joined := strings.Join(cmd.Args, " ")
	if strings.Contains(joined, "-e HOME=") || strings.Contains(joined, "-e PATH=") {
		t.Fatalf("host HOME/PATH must not be forwarded into the container:\n%s", joined)
	}
}

func TestDockerRuntimeForwardsTheModelCredential(t *testing.T) {
	runtime := DockerRuntime{Image: "agentbench:dev", PassEnv: []string{"ANTHROPIC_API_KEY"}}
	t.Setenv("ANTHROPIC_API_KEY", "secret-value")

	cmd := runtime.Command(context.Background(), Workspace{Root: "/runs/ws"}, ArmBare, []string{"claude"})

	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "-e ANTHROPIC_API_KEY") {
		t.Fatalf("the credential must reach the container:\n%s", joined)
	}
	// Der Wert gehoert NICHT in die Kommandozeile: die steht in jedem ps.
	if strings.Contains(joined, "secret-value") {
		t.Fatalf("the credential value must not appear in the command line:\n%s", joined)
	}
}

func TestContainerAgentUsesTheRuntime(t *testing.T) {
	ws := Workspace{Root: t.TempDir()}
	ws.Repo = filepath.Join(ws.Root, "repo")
	agent := NewClaudeCodeAgent("claude", ws, "", LocalRuntime{})
	if agent.runtime == nil {
		t.Fatal("the agent must carry a runtime so container and local runs share one path")
	}
}
