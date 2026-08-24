package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/mcpserver"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// currentAxes derives the session context from the working directory and the
// configured machine name.
func currentAxes(machine string) scope.Axes {
	return currentGitContext(machine).axes
}

type harnessContext struct {
	axes       scope.Axes
	activation activation.Context
}

func currentGitContext(machine string) harnessContext {
	cwd, err := os.Getwd()
	if err != nil {
		return harnessContext{axes: scope.Axes{Machine: machine}}
	}
	g := collector.ResolveGitContext(cwd)
	return harnessContext{
		axes:       scope.Axes{Project: g.Project, Branch: g.Branch, Lineage: g.Lineage, Machine: machine},
		activation: activation.Context{RepoPath: g.RepoPath},
	}
}

func cmdMCP(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stdout)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		// stderr: stdout is the JSON-RPC channel.
		fmt.Fprintf(os.Stderr, "load config: %v (run 'ctx setup' first)\n", err)
		return 1
	}
	hctx := currentGitContext(cfg.Machine)
	srv := mcpserver.NewServer(client.New(cfg), hctx.axes, hctx.activation)
	if err := mcpserver.Run(context.Background(), srv, version); err != nil {
		fmt.Fprintf(os.Stderr, "mcp: %v\n", err)
		return 1
	}
	return 0
}
