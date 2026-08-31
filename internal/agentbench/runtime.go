package agentbench

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// Runtime decides where a run actually executes. Local and container runs
// share one code path so that the only difference between them is the
// isolation, not the invocation.
type Runtime interface {
	Command(ctx context.Context, ws Workspace, arm ArmName, argv []string) *exec.Cmd
}

// LocalRuntime runs on the host. Correct for tests and for inspecting a
// single run by hand; not correct for a campaign, because the host's global
// agent rules and installed tools reach into every arm.
type LocalRuntime struct{}

func (LocalRuntime) Command(ctx context.Context, ws Workspace, _ ArmName, argv []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = ws.Repo
	cmd.Env = envSlice(ws.Env)
	return cmd
}

// DockerRuntime runs each invocation in a throwaway container. The workspace
// is mounted at /work; HOME, XDG_CONFIG_HOME and the per-arm PATH are set by
// the image's entrypoint, not forwarded from here — host paths would not
// exist inside the container.
type DockerRuntime struct {
	Image string
	// Network is passed to docker run. Empty means Docker's default, which
	// gives the run the full internet. EnsureSealedNetwork returns the name of
	// an internal network instead, which has no route out at all.
	Network string
	// ProxyURL is the only way out of that internal network. Set together with
	// Network it seals the run to the allowed domains; set alone it does
	// nothing, because Docker's default network reaches everything anyway.
	ProxyURL string
	// PassEnv names host variables forwarded by name only. The value is never
	// placed on the command line, where every ps would show it.
	PassEnv []string
	// User is passed to docker run. Empty means the calling user. Without it
	// the container writes into the mounted workspace as root and the caller
	// can no longer delete it — after a campaign that is one undeletable
	// directory per run.
	User string
}

func (d DockerRuntime) Command(ctx context.Context, ws Workspace, arm ArmName, argv []string) *exec.Cmd {
	args := []string{"run", "--rm", "-v", ws.Root + ":/work", "-w", "/work/repo",
		"-e", "AGENTBENCH_ARM=" + string(arm), "--user", d.user()}
	if d.Network != "" {
		args = append(args, "--network", d.Network)
	}
	if d.ProxyURL != "" {
		args = append(args,
			"-e", "HTTPS_PROXY="+d.ProxyURL, "-e", "https_proxy="+d.ProxyURL,
			"-e", "HTTP_PROXY="+d.ProxyURL, "-e", "http_proxy="+d.ProxyURL,
			"-e", "NO_PROXY=localhost,127.0.0.1", "-e", "no_proxy=localhost,127.0.0.1")
	}
	for _, name := range d.PassEnv {
		args = append(args, "-e", name)
	}
	args = append(args, d.Image)
	args = append(args, argv...)
	return exec.CommandContext(ctx, "docker", args...)
}

func (d DockerRuntime) user() string {
	if d.User != "" {
		return d.User
	}
	return fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
}
