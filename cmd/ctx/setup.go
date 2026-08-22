package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
)

func cmdSetup(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(stdout)
	serverURL := fs.String("server", "", "ghosttree server URL, e.g. http://host:8474")
	token := fs.String("token", "", "bearer token from 'ctx person add'")
	machine := fs.String("machine", "", "machine name (default: hostname)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *serverURL == "" || *token == "" {
		fmt.Fprintln(stdout, "usage: ctx setup --server <url> --token <token> [--machine <name>]")
		return 2
	}
	cfg := config.Config{ServerURL: *serverURL, Token: *token, Machine: *machine}
	if cfg.Machine == "" {
		cfg.Machine, _ = os.Hostname()
	}
	if err := config.Save(cfg); err != nil {
		fmt.Fprintf(stdout, "save config: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s (machine %s)\n", config.Path(), cfg.Machine)
	if err := client.New(cfg).Health(); err != nil {
		fmt.Fprintf(stdout, "server check failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "server reachable")
	return 0
}
