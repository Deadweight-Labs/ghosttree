package main

import (
	"fmt"
	"io"
)

const machineUsage = `usage: ctx machine list --db <path>
       ctx machine release <name> --db <path>
       ctx machine transfer <name> <account> --db <path>`

// cmdMachine arbeitet wie account direkt auf der Datenbank: Maschinennamen
// freizugeben oder umzuhängen braucht Zugriff auf den Server-Host.
func cmdMachine(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, machineUsage)
		return 2
	}
	switch args[0] {
	case "list":
		_, _, db, ok := accountFlags("machine list", args[1:], stdout, nil)
		if !ok {
			fmt.Fprintln(stdout, machineUsage)
			return 2
		}
		st, ok := openAccountStore(*db, stdout)
		if !ok {
			return 1
		}
		defer st.Close()
		machines, err := st.ListMachines("")
		if err != nil {
			fmt.Fprintf(stdout, "list machines: %v\n", err)
			return 1
		}
		for _, m := range machines {
			fmt.Fprintf(stdout, "%s\t%s\tlast seen %s\n", m.Name, m.Owner, m.LastSeen)
		}
		return 0
	case "release":
		_, name, db, ok := accountFlags("machine release", args[1:], stdout, nil)
		if !ok || name == "" {
			fmt.Fprintln(stdout, machineUsage)
			return 2
		}
		st, ok := openAccountStore(*db, stdout)
		if !ok {
			return 1
		}
		defer st.Close()
		if err := st.ReleaseMachine(name); err != nil {
			fmt.Fprintf(stdout, "release machine: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "machine %s released\n", name)
		return 0
	case "transfer":
		if len(args) < 3 {
			fmt.Fprintln(stdout, machineUsage)
			return 2
		}
		_, name, db, ok := accountFlags("machine transfer", append([]string{args[1]}, args[3:]...), stdout, nil)
		if !ok || name == "" || args[2] == "" || args[2][0] == '-' {
			fmt.Fprintln(stdout, machineUsage)
			return 2
		}
		st, ok := openAccountStore(*db, stdout)
		if !ok {
			return 1
		}
		defer st.Close()
		if err := st.TransferMachine(name, args[2]); err != nil {
			fmt.Fprintf(stdout, "transfer machine: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "machine %s now belongs to %s\n", name, args[2])
		return 0
	}
	fmt.Fprintln(stdout, machineUsage)
	return 2
}
