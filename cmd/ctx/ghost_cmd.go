package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
)

const ghostUsage = `usage: ctx ghost history <pfad> [anzahl]

  history   frühere Fassungen der Beschreibung eines Pfades, neueste zuerst`

// cmdGhost ist die Terminalseite der Dateibeschreibungen — für den Menschen,
// unabhängig davon, ob gerade ein Agent läuft. Bisher gibt es nur die Historie:
// alles andere ist im Baum unter .ghosttree/tree/ schon mit ls und cat zu
// haben, die Historie steht dort bewusst nicht.
func cmdGhost(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, ghostUsage)
		return 2
	}
	switch args[0] {
	case "history":
		return ghostHistory(args[1:], stdout)
	default:
		fmt.Fprintln(stdout, ghostUsage)
		return 2
	}
}

func ghostHistory(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, ghostUsage)
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "keine Konfiguration: %v (erst 'ctx setup')\n", err)
		return 1
	}
	cwd, _ := os.Getwd()
	gitCtx := collector.ResolveGitContext(cwd)
	if gitCtx.Project == "" || gitCtx.Root == "" {
		fmt.Fprintln(stdout, "kein Repository — eine Dateibeschreibung hängt an einem Projekt")
		return 1
	}

	rel := args[0]
	if rel == "." {
		rel = ""
	} else if filepath.IsAbs(rel) {
		if r, err := filepath.Rel(gitCtx.Root, rel); err == nil {
			rel = r
		}
	}
	rel = filepath.ToSlash(rel)

	limit := 0
	if len(args) > 1 {
		limit, _ = strconv.Atoi(args[1])
	}
	versions, err := client.New(cfg).GhostHistory(gitCtx.Project, rel, limit)
	if err != nil {
		fmt.Fprintf(stdout, "Historie nicht lesbar: %v\n", err)
		return 1
	}
	name := rel
	if name == "" {
		name = "(Repo-Wurzel)"
	}
	if len(versions) == 0 {
		fmt.Fprintf(stdout, "%s: keine früheren Fassungen\n", name)
		return 0
	}
	for _, v := range versions {
		fmt.Fprintf(stdout, "%s bis %s", shortDay(v.DescribedAt), shortDay(v.ReplacedAt))
		if v.Person != "" {
			fmt.Fprintf(stdout, "  %s", v.Person)
		}
		if v.Reason != "" && v.Reason != "ersetzt" {
			fmt.Fprintf(stdout, "  [%s]", v.Reason)
		}
		if v.LineCount > 0 {
			fmt.Fprintf(stdout, "  (%d Zeilen)", v.LineCount)
		}
		fmt.Fprintln(stdout)
		for _, line := range strings.Split(strings.TrimRight(v.Description, "\n"), "\n") {
			fmt.Fprintf(stdout, "    %s\n", line)
		}
		fmt.Fprintln(stdout)
	}
	return 0
}

func shortDay(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}
