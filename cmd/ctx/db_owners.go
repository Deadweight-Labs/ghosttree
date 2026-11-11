package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const dbMigrateOwnersUsage = `usage: ctx db migrate-owners --db <path> --dry-run [--scratch <dir>] [--plan <file>] [--json]
       ctx db migrate-owners --db <path> --apply --backup <file> [--plan <file>] [--json]
       ctx db migrate-owners --db <path> --apply --no-backup [--plan <file>] [--json]

Moves an existing instance to owners as on GitHub (REQ-623): what the claimed
prefix of the organization with the GitHub source covers stays there;
everything else goes to the own namespace of the person who used it first
(the default owner when nobody did); throwaway remotes (local paths, reserved
test hosts) are archived, never deleted; sessions without an account go to
the instance owner; a remote several people wrote to stays where it is
(contested) until the plan's "to" assigns it; the organization takes the login of its GitHub source as its
address, with a redirect from the old one.

It snapshots who sees which project before and after, in one transaction.
Only two differences are allowed: a service loses what "shared with the org"
gave it, and a person gains projects in their own namespace. Any other one
rolls everything back and is listed. --dry-run is the same run, rolled back,
on a copy of the database in --scratch (default: the temp folder) that is
removed afterwards: the database itself is only read, and no schema step of
this version touches it.

--apply refuses while another process has the database open (stop the
server), then writes a copy of the file as it is (--backup, VACUUM INTO a file
that must not exist) before anything opens it; --no-backup only if you made
one yourself.

--plan is a JSON file with the choices (all optional):
  {"org": "default", "org_slug": "...", "org_name": "...", "default_owner": "robin",
   "keep": ["github.com/x"], "to": {"github.com/x/y": "philipp"},
   "merge": {"a": "b"}, "rename": {"a": "b"},
   "archive": ["github.com/e2e"], "archive_orgs": ["e2e-test"],
   "archive_machines": ["old-laptop"], "disable_accounts": ["e2e-host"],
   "services": {"gardener": "", "ops-runner": "org-slug"},
   "skip_unclaimed": false, "skip_sessions": false, "follow_github": false}`

func cmdDBMigrateOwners(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("db migrate-owners", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	dry := fs.Bool("dry-run", false, "run and roll back, report what would happen")
	apply := fs.Bool("apply", false, "run and keep the result")
	backup := fs.String("backup", "", "with --apply: write a copy of the database here first")
	noBackup := fs.Bool("no-backup", false, "with --apply: you made a copy yourself")
	planPath := fs.String("plan", "", "JSON file with the choices")
	scratch := fs.String("scratch", os.TempDir(), "with --dry-run: folder for the copy the dry run works on")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	fs.Usage = func() { fmt.Fprintln(stdout, dbMigrateOwnersUsage) }
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *dbPath == "" || *dry == *apply || (*apply && (*backup == "") == !*noBackup) {
		fmt.Fprintln(stdout, dbMigrateOwnersUsage)
		return 2
	}
	var plan store.OwnerMigrationPlan
	if *planPath != "" {
		raw, err := os.ReadFile(*planPath)
		if err != nil {
			fmt.Fprintf(stdout, "read plan: %v\n", err)
			return 1
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&plan); err != nil {
			fmt.Fprintf(stdout, "plan %s: %v\n", *planPath, err)
			return 2
		}
	}
	if _, err := os.Stat(*dbPath); err != nil {
		fmt.Fprintf(stdout, "database: %v\n", err)
		return 1
	}
	// Applying needs the server stopped, and the copy is of the file as it
	// was: taken before opening the store runs its schema steps on it.
	if *apply {
		if err := store.EnsureNotInUse(*dbPath); err != nil {
			fmt.Fprintf(stdout, "refused: %v\n", err)
			return 1
		}
	}
	if *apply && *backup != "" {
		if _, err := os.Stat(*backup); err == nil {
			fmt.Fprintf(stdout, "backup %s exists already; choose a new file\n", *backup)
			return 1
		}
		if err := store.BackupDatabaseFile(*dbPath, *backup); err != nil {
			fmt.Fprintf(stdout, "backup: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "backup written to %s\n", *backup)
	}
	open := *dbPath
	if *dry {
		dir, err := os.MkdirTemp(*scratch, "migrate-owners-dry-")
		if err != nil {
			fmt.Fprintf(stdout, "scratch folder: %v\n", err)
			return 1
		}
		defer os.RemoveAll(dir)
		open = filepath.Join(dir, "copy.db")
		if err := store.BackupDatabaseFile(*dbPath, open); err != nil {
			fmt.Fprintf(stdout, "copy for the dry run: %v\n", err)
			return 1
		}
	}
	st, err := store.OpenWithOptions(open, store.OpenOptions{MaxOpenConns: 1})
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return 1
	}
	defer st.Close()
	rep, err := st.MigrateOwners(plan, *apply)
	if *asJSON {
		out, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Fprintln(stdout, string(out))
	} else {
		printOwnerReport(stdout, rep, *apply)
	}
	switch {
	case errors.Is(err, store.ErrMigrationDeviates):
		fmt.Fprintln(stdout, "refused: nothing was changed")
		return 1
	case err != nil:
		fmt.Fprintf(stdout, "migrate owners: %v\n", err)
		return 1
	}
	return 0
}

func printOwnerReport(w io.Writer, rep store.OwnerMigrationReport, apply bool) {
	mode := "dry run (rolled back)"
	if apply && rep.Applied {
		mode = "applied"
	}
	fmt.Fprintf(w, "migrate-owners: %s\n", mode)
	fmt.Fprintf(w, "organization %s", rep.Org)
	if rep.OrgMoved != "" {
		fmt.Fprintf(w, ", address %s (old one redirects)", rep.OrgMoved)
	}
	fmt.Fprintf(w, "; %d projects stay\n", rep.Stayed)
	fmt.Fprintf(w, "held: %d entries of others held back in projects that are created or change owner\n", rep.Held)
	fmt.Fprintf(w, "contested: %d remotes several people wrote to stay where they are\n", len(rep.Contested))
	switch rep.DisplaySource {
	case store.DisplayGitHub:
		fmt.Fprintln(w, "name and address follow GitHub from now on")
	case store.DisplayUser:
		fmt.Fprintln(w, "name and address stay as set (an owner can let them follow GitHub)")
	}
	list := func(title string, m map[string][]string) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "%s %s: %d\n", title, k, len(m[k]))
			for _, r := range m[k] {
				fmt.Fprintf(w, "  %s\n", r)
			}
		}
	}
	list("moved to namespace", rep.Moved)
	list("claimed for namespace", rep.Claimed)
	lines := func(title string, xs []string) {
		if len(xs) == 0 {
			return
		}
		fmt.Fprintf(w, "%s: %d\n", title, len(xs))
		for _, x := range xs {
			fmt.Fprintf(w, "  %s\n", x)
		}
	}
	lines("renamed", rep.Renamed)
	lines("merged", rep.Merged)
	lines("archived projects", rep.Archived)
	lines("archived machines", rep.ArchivedMachines)
	lines("disabled accounts", rep.Disabled)
	lines("services", rep.Services)
	fmt.Fprintf(w, "sessions without an account given to the instance owner: %d\n", rep.SessionsStamped)
	list("gains (own namespace)", rep.Gained)
	list("gains (org member)", rep.GainedInOrg)
	list("losses", rep.Lost)
	lines("contested (several people used it; name the owner with \"to\" in the plan)", rep.Contested)
	lines("NOT ALLOWED", rep.Unexpected)
}
