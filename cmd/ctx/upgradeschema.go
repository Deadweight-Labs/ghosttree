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
	requestBackup, err := store.UpgradeRequestDomain(*dbPath)
	if err != nil {
		fmt.Fprintf(stdout, "request domain upgrade failed: %v\n", err)
		return 1
	}
	usageBackup, err := store.UpgradeUsageTelemetry(*dbPath)
	if err != nil {
		fmt.Fprintf(stdout, "usage telemetry upgrade failed: %v\n", err)
		return 1
	}
	distillBackup, err := store.UpgradeDistillationVersion(*dbPath)
	if err != nil {
		fmt.Fprintf(stdout, "distillation version upgrade failed: %v\n", err)
		return 1
	}
	added, err := store.AddedColumns(*dbPath)
	if err != nil {
		fmt.Fprintf(stdout, "column upgrade failed: %v\n", err)
		return 1
	}
	for _, column := range added {
		fmt.Fprintf(stdout, "added column %s\n", column)
	}
	// Adding observed_at leaves it empty, and an empty observation time is the
	// silent version of the bug it was added for. Dating the existing stock is
	// part of the upgrade, not a follow-up somebody has to remember.
	dated, err := backfillObservations(*dbPath)
	if err != nil {
		fmt.Fprintf(stdout, "dating existing entries failed: %v\n", err)
		return 1
	}
	if dated > 0 {
		fmt.Fprintf(stdout, "dated %d entries from their earliest evidence\n", dated)
	}
	if len(added) == 0 && dated == 0 && trustBackup == "" && typesBackup == "" && requestBackup == "" && usageBackup == "" && distillBackup == "" {
		fmt.Fprintln(stdout, "schema already current, nothing to do")
		return 0
	}
	if trustBackup != "" {
		if !reportVerifiedBackup(stdout, "trust schema", trustBackup) {
			return 1
		}
	}
	if typesBackup != "" {
		if !reportVerifiedBackup(stdout, "type schema", typesBackup) {
			return 1
		}
	}
	if requestBackup != "" {
		if !reportVerifiedBackup(stdout, "request domain", requestBackup) {
			return 1
		}
	}
	if usageBackup != "" {
		if !reportVerifiedBackup(stdout, "usage telemetry", usageBackup) {
			return 1
		}
	}
	if distillBackup != "" {
		if !reportVerifiedBackup(stdout, "distillation version", distillBackup) {
			return 1
		}
	}
	fmt.Fprintln(stdout, "schema upgraded")
	return 0
}

func backfillObservations(dbPath string) (int64, error) {
	st, err := store.Open(dbPath)
	if err != nil {
		return 0, err
	}
	defer st.Close()
	return st.BackfillObservedAt()
}

func reportVerifiedBackup(stdout io.Writer, label, path string) bool {
	digest, err := store.FileSHA256(path)
	if err != nil {
		fmt.Fprintf(stdout, "%s backup checksum failed: %v\n", label, err)
		return false
	}
	fmt.Fprintf(stdout, "%s backup written and verified: %s sha256=%s\n", label, path, digest)
	return true
}
