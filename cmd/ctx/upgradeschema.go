package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const upgradeUsage = `usage: ctx upgrade-schema --db <path>

Rebuild the knowledge table for trust tiers. Writes a backup next to the
database first. Run it once, with the server stopped.`

func cmdUpgradeSchema(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("upgrade-schema", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	fs.Usage = func() { fmt.Fprintln(stdout, upgradeUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" {
		fmt.Fprintln(stdout, upgradeUsage)
		return 2
	}
	trustBackup, err := store.UpgradeSchema(*dbPath)
	if err != nil {
		fmt.Fprintf(stdout, "upgrade failed: %v\n", err)
		return 1
	}
	typesBackup, err := store.UpgradeTypes(*dbPath)
	if err != nil {
		fmt.Fprintf(stdout, "type upgrade failed: %v\n", err)
		return 1
	}
	if trustBackup == "" && typesBackup == "" {
		fmt.Fprintln(stdout, "schema already current, nothing to do")
		return 0
	}
	if trustBackup != "" {
		fmt.Fprintf(stdout, "trust schema backup written to %s\n", trustBackup)
	}
	if typesBackup != "" {
		fmt.Fprintf(stdout, "type schema backup written to %s\n", typesBackup)
	}
	fmt.Fprintln(stdout, "schema upgraded")
	return 0
}
