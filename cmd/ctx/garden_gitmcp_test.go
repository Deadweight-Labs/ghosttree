package main

import (
	"bytes"
	"testing"
)

func TestGardenGitMCPNeedsAnAbsoluteCheckout(t *testing.T) {
	for _, args := range [][]string{{}, {"--repo", "relativ"}, {"--repo", "/nicht/da/garantiert"}} {
		var out bytes.Buffer
		if code := cmdGarden(append([]string{"git-mcp"}, args...), &out); code == 0 {
			t.Errorf("git-mcp %v started", args)
		}
	}
}
