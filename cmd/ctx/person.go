package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func cmdPerson(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		printPersonUsage(stdout)
		return 2
	}
	switch args[0] {
	case "add":
		return cmdPersonAdd(args[1:], stdout)
	case "whoami":
		return cmdPersonWhoAmI(args[1:], stdout)
	case "snapshot-access":
		return cmdPersonSnapshotAccess(args[1:], stdout)
	default:
		printPersonUsage(stdout)
		return 2
	}
}

func cmdPersonAdd(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("person add", flag.ContinueOnError)
	fs.SetOutput(stdout)
	db := fs.String("db", "ghosttree.db", "path to the sqlite database")
	rest := args
	var name string
	if len(rest) > 0 && rest[0] != "" && rest[0][0] != '-' {
		name, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if name == "" {
		fmt.Fprintln(stdout, "usage: ctx person add <name> --db <path>")
		return 2
	}
	st, err := store.Open(*db)
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return 1
	}
	defer st.Close()
	token, err := st.AddPerson(name)
	if err != nil {
		fmt.Fprintf(stdout, "add person: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "token: %s\n", token)
	fmt.Fprintln(stdout, "this token is shown once, store it now")
	return 0
}

func cmdPersonWhoAmI(args []string, stdout io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stdout, "usage: ctx person whoami")
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "load config: %v\n", err)
		return 1
	}
	principal, err := client.New(cfg).WhoAmI()
	if err != nil {
		fmt.Fprintf(stdout, "whoami: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s\t%s\n", principal.ID, principal.Label)
	return 0
}

func cmdPersonSnapshotAccess(args []string, stdout io.Writer) int {
	show := false
	if len(args) > 0 && args[0] == "show" {
		show = true
		args = args[1:]
	}
	var name string
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		name, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("person snapshot-access", flag.ContinueOnError)
	fs.SetOutput(stdout)
	db := fs.String("db", "ghosttree.db", "path to the sqlite database")
	project := fs.String("project", "", "project scope")
	read := fs.Bool("read", false, "allow snapshot reads")
	create := fs.Bool("create", false, "allow snapshot creation")
	releaseBind := fs.Bool("release-bind", false, "allow release-tag binding")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if name == "" || *project == "" || fs.NArg() != 0 || (show && (*read || *create || *releaseBind)) {
		printSnapshotAccessUsage(stdout)
		return 2
	}
	st, err := store.Open(*db)
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return 1
	}
	defer st.Close()
	principal, ok := st.PrincipalByName(name)
	if !ok {
		fmt.Fprintf(stdout, "person %q not found\n", name)
		return 1
	}
	if !show {
		if err := st.SetContextSnapshotAccess(name, *project, *read, *create, *releaseBind); err != nil {
			fmt.Fprintf(stdout, "set snapshot access: %v\n", err)
			return 1
		}
	}
	access, err := st.ContextSnapshotAccess(principal.ID, *project)
	if err != nil {
		fmt.Fprintf(stdout, "read snapshot access: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s\t%s\tproject=%s read=%t create=%t release-bind=%t\n",
		principal.ID, principal.Label, *project, access.Read, access.Create, access.ReleaseBind)
	return 0
}

func printPersonUsage(stdout io.Writer) {
	fmt.Fprintln(stdout, "usage: ctx person add <name> --db <path>")
	fmt.Fprintln(stdout, "       ctx person whoami")
	printSnapshotAccessUsage(stdout)
}

func printSnapshotAccessUsage(stdout io.Writer) {
	fmt.Fprintln(stdout, "       ctx person snapshot-access show <name> --project P --db FILE")
	fmt.Fprintln(stdout, "       ctx person snapshot-access <name> --project P --read --create [--release-bind] --db FILE")
}
