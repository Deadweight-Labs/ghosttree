package main

import (
	"fmt"
	"io"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
)

const sessionUsage = `usage: ctx session share <id> [--off]

  share  give the members of the project read access to this session's transcript
         (--off takes the release back). Only the owner of a session may do this;
         project owners and leads read every transcript of their project anyway.`

// cmdSession bündelt Befehle zu Sessions. Bisher: Freigabe des Transkripts.
func cmdSession(args []string, stdout io.Writer) int {
	if len(args) == 0 || args[0] != "share" {
		fmt.Fprintln(stdout, sessionUsage)
		return 2
	}
	rest := args[1:]
	shared := true
	var idArg string
	for _, a := range rest {
		switch {
		case a == "--off":
			shared = false
		case len(a) > 0 && a[0] == '-':
			fmt.Fprintln(stdout, sessionUsage)
			return 2
		case idArg == "":
			idArg = a
		default:
			fmt.Fprintln(stdout, sessionUsage)
			return 2
		}
	}
	id, err := strconv.ParseInt(idArg, 10, 64)
	if err != nil || id <= 0 {
		fmt.Fprintln(stdout, sessionUsage)
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	if err := client.New(cfg).ShareSession(id, shared); err != nil {
		fmt.Fprintf(stdout, "share session: %v\n", err)
		return 1
	}
	if shared {
		fmt.Fprintf(stdout, "session %d shared with the members of its project\n", id)
	} else {
		fmt.Fprintf(stdout, "session %d is private again\n", id)
	}
	return 0
}
