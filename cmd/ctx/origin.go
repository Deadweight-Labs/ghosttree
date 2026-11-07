package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const originUsage = `usage: ctx origin backfill --db <path> [--dry-run] [--json]

Trage den Entstehungskontext (Zeitpunkt, Session, Bereiche) für Einträge nach,
die beim Schreiben keinen bekommen haben: Wissen, Ghost-Beschreibungen, Requests
und Pains. Gelesen werden die Session des Eintrags und ihre Pfadaktivität
(path_activity) bis zum Zeitpunkt des Eintrags. Wo die Session unbekannt ist
oder keine Pfaddaten mehr da sind (die Aufräumung löscht sie nach sieben Tagen),
bleibt der Eintrag ohne Kontext, es wird nichts geraten.

Idempotent: ein zweiter Lauf ändert nichts. --dry-run zählt nur. Läuft nicht
beim Start des Servers, sondern nur auf Anforderung.`

func cmdOrigin(args []string, stdout io.Writer) int {
	if len(args) == 0 || args[0] != "backfill" {
		fmt.Fprintln(stdout, originUsage)
		return 2
	}
	fs := flag.NewFlagSet("origin backfill", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	dryRun := fs.Bool("dry-run", false, "count what would be filled, write nothing")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	fs.Usage = func() { fmt.Fprintln(stdout, originUsage) }
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *dbPath == "" {
		fmt.Fprintln(stdout, originUsage)
		return 2
	}
	s, err := store.OpenWithOptions(*dbPath, store.OpenOptions{MaxOpenConns: 1})
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return 1
	}
	defer s.Close()
	rep, err := s.BackfillOrigins(*dryRun)
	if err != nil {
		fmt.Fprintf(stdout, "backfill failed: %v\n", err)
		return 1
	}
	if *asJSON {
		raw, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Fprintln(stdout, string(raw))
		return 0
	}
	if rep.DryRun {
		fmt.Fprintln(stdout, "dry run: nothing was written")
	}
	verb := "filled"
	if rep.DryRun {
		verb = "would fill"
	}
	for _, row := range []struct {
		name string
		k    store.OriginBackfillKind
	}{{"knowledge", rep.Knowledge}, {"ghosts", rep.Ghosts}, {"requests", rep.Requests}, {"pains", rep.Pains}} {
		fmt.Fprintf(stdout, "%-10s candidates=%d %s=%d left empty=%d\n", row.name, row.k.Candidates, verb, row.k.Filled, row.k.Empty)
	}
	return 0
}
