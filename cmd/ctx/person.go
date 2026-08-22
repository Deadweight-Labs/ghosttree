package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func cmdPerson(args []string, stdout io.Writer) int {
	if len(args) == 0 || args[0] != "add" {
		fmt.Fprintln(stdout, "usage: ctx person add <name> --db <path>")
		return 2
	}
	fs := flag.NewFlagSet("person add", flag.ContinueOnError)
	fs.SetOutput(stdout)
	db := fs.String("db", "ghosttree.db", "path to the sqlite database")
	rest := args[1:]
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
