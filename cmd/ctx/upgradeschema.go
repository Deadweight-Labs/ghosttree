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
	backup, err := store.UpgradeSchema(*dbPath)
	if err != nil {
		fmt.Fprintf(stdout, "upgrade failed: %v\n", err)
		return 1
	}
	if backup == "" {
		fmt.Fprintln(stdout, "schema already current, nothing to do")
		return 0
	}
	fmt.Fprintf(stdout, "backup written to %s\nschema upgraded\n", backup)
	return 0
}
