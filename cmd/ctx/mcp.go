package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/mcpserver"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// currentAxes derives the session context from the working directory and the
// configured machine name.
func currentAxes(machine string) scope.Axes {
	cwd, err := os.Getwd()
	if err != nil {
		return scope.Axes{Machine: machine}
	}
	project, branch := collector.GitInfo(cwd)
	return scope.Axes{Project: project, Branch: branch, Machine: machine}
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
	srv := mcpserver.NewServer(client.New(cfg), currentAxes(cfg.Machine))
	if err := mcpserver.Run(context.Background(), srv, version); err != nil {
		fmt.Fprintf(os.Stderr, "mcp: %v\n", err)
		return 1
	}
	return 0
}
