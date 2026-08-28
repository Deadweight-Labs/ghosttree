package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const canonicalizeUsage = `usage: ctx canonicalize-scopes --db <path> [--aliases <file>] [--dry-run]

Rewrite every stored project name to its canonical form, widen requests to
project scope, and merge the duplicates that non-canonical names produced.
Writes a verified backup first. Run it once, with the server stopped.

--aliases takes a JSON object mapping an old canonical project to its current
one, for repositories that changed owner:

  {"github.com/old-owner/project": "github.com/new-owner/project"}`

func cmdCanonicalizeScopes(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("canonicalize-scopes", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	aliasPath := fs.String("aliases", "", "JSON file mapping old project names to current ones")
	dryRun := fs.Bool("dry-run", false, "report what would change, then roll back")
	fs.Usage = func() { fmt.Fprintln(stdout, canonicalizeUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" {
		fmt.Fprintln(stdout, canonicalizeUsage)
		return 2
	}

	aliases := map[string]string{}
	if *aliasPath != "" {
		raw, err := os.ReadFile(*aliasPath)
		if err != nil {
			fmt.Fprintf(stdout, "read aliases: %v\n", err)
			return 1
		}
		if err := json.Unmarshal(raw, &aliases); err != nil {
			fmt.Fprintf(stdout, "parse aliases: %v\n", err)
			return 1
		}
	}

	s, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return 1
	}
	defer s.Close()

	if *dryRun {
		report, err := s.PreviewCanonicalizeScopes(aliases)
		if err != nil {
			fmt.Fprintf(stdout, "dry run failed: %v\n", err)
			return 1
		}
		reportCanonicalize(stdout, report, true)
		return 0
	}

	backup := fmt.Sprintf("%s.backup-%s", *dbPath, time.Now().UTC().Format("20060102-150405"))
	if err := s.Backup(backup); err != nil {
		fmt.Fprintf(stdout, "backup failed: %v\n", err)
		return 1
	}
	if !reportVerifiedBackup(stdout, "scope canonicalisation", backup) {
		return 1
	}
	report, err := s.CanonicalizeScopes(aliases)
	if err != nil {
		fmt.Fprintf(stdout, "canonicalisation failed: %v\n", err)
		return 1
	}
	reportCanonicalize(stdout, report, false)
	return 0
}

func reportCanonicalize(stdout io.Writer, r store.CanonicalizeReport, preview bool) {
	verb := "rescoped"
	if preview {
		verb = "would rescope"
		fmt.Fprintln(stdout, "dry run: no changes were written")
	}
	fmt.Fprintf(stdout, "%s knowledge=%d requests=%d sessions=%d search=%d\n",
		verb, r.KnowledgeRescoped, r.RequestsRescoped, r.SessionsRescoped, r.DocumentsRescoped)
	fmt.Fprintf(stdout, "requests widened to project scope: %d\n", r.RequestsWidened)
	fmt.Fprintf(stdout, "duplicate requests merged: %d\n", r.RequestsMerged)
	if len(r.MergeSkipped) > 0 {
		// These carry work, evidence or relations of their own, which a merge
		// would silently discard. They need a human decision.
		fmt.Fprintf(stdout, "duplicates left for review (they carry work or evidence): %v\n", r.MergeSkipped)
	}
	fmt.Fprintf(stdout, "canonical projects now in the tree: %d\n", len(r.Projects))
	for _, p := range r.Projects {
		fmt.Fprintf(stdout, "  %s\n", p)
	}
}
