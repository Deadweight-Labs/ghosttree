package agentbench

import (
	"context"
	"os"
	"testing"
)

// proxyImage names the sealing proxy. Like dockerImage it gates the test:
// building both images is a campaign-machine step, not a `go test` step.
func proxyImage(t *testing.T) string {
	t.Helper()
	image := os.Getenv("AGENTBENCH_PROXY_IMAGE")
	if image == "" {
		t.Skip("set AGENTBENCH_PROXY_IMAGE to test the sealed network")
	}
	return image
}

// TestSealedNetworkLetsTheModelThroughAndNothingElse is the test the whole
// seal exists for. It asserts both directions: a blocked host is not a
// success if the model is blocked too, and a reachable model is not a success
// if everything else is reachable as well.
func TestSealedNetworkLetsTheModelThroughAndNothingElse(t *testing.T) {
	agent, proxy := dockerImage(t), proxyImage(t)
	ctx := context.Background()
	spec := NetworkSpec{Name: "agentbench-sealed-test", ProxyName: "agentbench-proxy-test", ProxyImage: proxy}
	t.Cleanup(func() {
		if err := TeardownSealedNetwork(context.Background(), spec); err != nil {
			t.Logf("teardown: %v", err)
		}
	})

	sealed, err := EnsureSealedNetwork(ctx, spec)
	if err != nil {
		t.Fatalf("the sealed network must come up: %v", err)
	}

	runtime := DockerRuntime{Image: agent, Network: sealed.Name, ProxyURL: sealed.ProxyURL}
	findings, err := CheckRuntimeLeakage(ctx, runtime, containerWorkspace(t), ArmBare, AllowedSurface{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	for _, f := range findings {
		switch f.Kind {
		case "network_open":
			t.Fatalf("the seal did not hold: %+v", f)
		case "model_unreachable":
			t.Fatalf("the seal blocks the model, so every run would fail: %+v", f)
		case "probe_failed":
			t.Fatalf("the probe could not test the network: %+v", f)
		}
	}
}

// TestUnsealedNetworkIsDetected proves the probe can tell the two apart. A
// probe that reports "sealed" for every configuration would be worthless, and
// the only way to know is to run it against a container that is not sealed.
func TestUnsealedNetworkIsDetected(t *testing.T) {
	runtime := DockerRuntime{Image: dockerImage(t)}

	findings, err := CheckRuntimeLeakage(context.Background(), runtime,
		containerWorkspace(t), ArmBare, AllowedSurface{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	var caught bool
	for _, f := range findings {
		if f.Kind == "network_open" {
			caught = true
		}
	}
	if !caught {
		t.Fatalf("a container on Docker's default network reaches everything and must be reported: %+v", findings)
	}
}
