package main

import (
	"fmt"
	"io"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
)

const configUsage = `usage: ctx config get [channel]
       ctx config set channel stable|dev`

// cmdConfig reads and writes the few client settings that are not login data.
// The token is never printed.
func cmdConfig(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, configUsage)
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "config missing (%s) - run 'ctx setup' or 'ctx login'\n", config.Path())
		return 1
	}
	switch args[0] {
	case "get":
		switch {
		case len(args) == 1:
			fmt.Fprintf(stdout, "server  %s\nmachine %s\nchannel %s\n", cfg.ServerURL, cfg.Machine, cfg.UpdateChannel())
			return 0
		case len(args) == 2 && args[1] == "channel":
			fmt.Fprintln(stdout, cfg.UpdateChannel())
			return 0
		}
	case "set":
		if len(args) == 3 && args[1] == "channel" {
			if !config.ValidChannel(args[2]) {
				fmt.Fprintf(stdout, "unknown channel %q: use stable or dev\n", args[2])
				return 2
			}
			cfg.Channel = args[2]
			if err := config.Save(cfg); err != nil {
				fmt.Fprintf(stdout, "save config: %v\n", err)
				return 1
			}
			fmt.Fprintf(stdout, "channel %s\n", cfg.UpdateChannel())
			return 0
		}
	}
	fmt.Fprintln(stdout, configUsage)
	return 2
}
