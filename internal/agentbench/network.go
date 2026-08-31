package agentbench

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultAllowedDomains is the smallest set a Claude Code run needs. Anything
// beyond it is research the arms would do unequally: an arm that can read the
// web can compensate for a missing memory, and the comparison stops measuring
// memory.
var DefaultAllowedDomains = []string{"api.anthropic.com"}

// NetworkSpec describes the sealed network a campaign runs in: an internal
// Docker network with no route out, plus one proxy that reaches exactly the
// allowed domains.
type NetworkSpec struct {
	Name       string
	ProxyImage string
	ProxyName  string
	Allowed    []string
}

// SealedNetwork is what a runtime needs to place a container inside the seal.
type SealedNetwork struct {
	Name     string
	ProxyURL string
	Allowed  []string
}

func (s NetworkSpec) withDefaults() NetworkSpec {
	if s.Name == "" {
		s.Name = "agentbench-sealed"
	}
	if s.ProxyImage == "" {
		s.ProxyImage = "agentbench-proxy:dev"
	}
	if s.ProxyName == "" {
		s.ProxyName = "agentbench-proxy"
	}
	if len(s.Allowed) == 0 {
		s.Allowed = DefaultAllowedDomains
	}
	return s
}

// EnsureSealedNetwork creates the internal network and the proxy if they do
// not exist yet, and waits until the proxy answers. It is idempotent: a second
// campaign on the same machine reuses both.
func EnsureSealedNetwork(ctx context.Context, spec NetworkSpec) (SealedNetwork, error) {
	spec = spec.withDefaults()
	sealed := SealedNetwork{
		Name:     spec.Name,
		ProxyURL: fmt.Sprintf("http://%s:3128", spec.ProxyName),
		Allowed:  spec.Allowed,
	}

	if !dockerHas(ctx, "network", spec.Name) {
		// --internal entfernt das Gateway: aus diesem Netz gibt es keinen Weg
		// nach draussen ausser ueber einen Container, der zusaetzlich in einem
		// zweiten Netz haengt.
		if err := dockerRun(ctx, "network", "create", "--internal", spec.Name); err != nil {
			return SealedNetwork{}, err
		}
	}

	if !dockerHas(ctx, "container", spec.ProxyName) {
		// Der Proxy startet im Standardnetz (dort hat er Internetzugang) und
		// wird danach zusaetzlich ins interne Netz gehaengt. Umgekehrt ginge es
		// nicht: ein Container im internen Netz kann keine Namen aufloesen.
		if err := dockerRun(ctx, "run", "-d", "--name", spec.ProxyName,
			"--restart", "unless-stopped",
			"-e", "AGENTBENCH_ALLOWED_DOMAINS="+strings.Join(spec.Allowed, " "),
			spec.ProxyImage); err != nil {
			return SealedNetwork{}, err
		}
		if err := dockerRun(ctx, "network", "connect", spec.Name, spec.ProxyName); err != nil {
			return SealedNetwork{}, err
		}
	}
	if err := waitForProxy(ctx, spec); err != nil {
		return SealedNetwork{}, err
	}
	return sealed, nil
}

// TeardownSealedNetwork removes the proxy and the network. A campaign does not
// have to call it; leaving both in place makes the next campaign start faster.
func TeardownSealedNetwork(ctx context.Context, spec NetworkSpec) error {
	spec = spec.withDefaults()
	if dockerHas(ctx, "container", spec.ProxyName) {
		if err := dockerRun(ctx, "rm", "-f", spec.ProxyName); err != nil {
			return err
		}
	}
	if dockerHas(ctx, "network", spec.Name) {
		return dockerRun(ctx, "network", "rm", spec.Name)
	}
	return nil
}

// waitForProxy blocks until the proxy accepts a connection from inside the
// sealed network. Starting a campaign against a proxy that is not up yet would
// fail the first runs and only those, which is the worst kind of noise.
func waitForProxy(ctx context.Context, spec NetworkSpec) error {
	deadline := time.Now().Add(30 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		// --entrypoint sh: der Wartecontainer benutzt nur das Abbild als
		// Beiwerk. Mit dem Proxy-Entrypoint wuerde er selbst eine Allowlist
		// verlangen und die Warteschleife immer scheitern lassen.
		// Geprueft wird der ganze Weg durch den Proxy nach draussen, nicht nur
		// ein offener Port: ein Proxy, der annimmt und dann nichts weiterleitet,
		// bestuende den Porttest.
		probe := fmt.Sprintf("curl -s -o /dev/null -m 5 -x http://%s:3128 https://%s/",
			spec.ProxyName, spec.Allowed[0])
		cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", spec.Name,
			"--entrypoint", "sh", spec.ProxyImage, "-c", probe)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		last = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("proxy %q did not come up on the sealed network: %v", spec.ProxyName, last)
}

func dockerHas(ctx context.Context, kind, name string) bool {
	out, err := exec.CommandContext(ctx, "docker", kind, "inspect", name).Output()
	return err == nil && len(out) > 0
}

func dockerRun(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
