package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/ghost"
	"github.com/Deadweight-Labs/ghosttree/internal/prose"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const ghostUsage = `usage: ctx ghost history <pfad> [anzahl] [--voll]

  history   was sich an der Beschreibung eines Pfades geändert hat, neueste zuerst
  --voll    statt der Änderung den Wortlaut jeder früheren Fassung`

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

// splitVollFlag trennt die Flagge von den Argumenten. Sie darf überall stehen:
// wer die Zahl schon getippt hat, soll sie nicht umstellen müssen.
func splitVollFlag(args []string) ([]string, bool) {
	var rest []string
	voll := false
	for _, a := range args {
		if a == "--voll" || a == "-voll" {
			voll = true
			continue
		}
		rest = append(rest, a)
	}
	return rest, voll
}

func ghostHistory(args []string, stdout io.Writer) int {
	args, voll := splitVollFlag(args)
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
	// Die Kette statt der blossen Historie: der Nachfolger der neuesten
	// abgelösten Fassung ist die Beschreibung, die heute gilt.
	chain, err := client.New(cfg).GhostChain(gitCtx.Project, rel, limit)
	if err != nil {
		fmt.Fprintf(stdout, "Historie nicht lesbar: %v\n", err)
		return 1
	}
	printHistory(stdout, rel, chain, voll)
	return 0
}

// printHistory ist die Terminalseite: dieselbe Auskunft wie für den Agenten,
// nur ohne Markdown. Vorgabe ist die Änderung, nicht der Wortlaut — zwei
// Prosablöcke nebeneinander lassen den Leser die Arbeit tun, die das Werkzeug
// tun sollte (REQ-180).
func printHistory(stdout io.Writer, name string, chain []store.GhostVersion, voll bool) {
	if name == "" {
		name = "(Repo-Wurzel)"
	}
	if len(chain) < 2 {
		fmt.Fprintf(stdout, "%s: keine früheren Fassungen\n", name)
		return
	}
	if voll {
		for _, v := range chain[1:] {
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
		return
	}

	for _, s := range ghost.HistorySteps(chain) {
		if s.Event != "" {
			fmt.Fprintf(stdout, "%s  %s\n\n", shortDay(s.At), s.Event)
			continue
		}
		fmt.Fprint(stdout, shortDay(s.At))
		if s.Person != "" {
			fmt.Fprintf(stdout, "  %s", s.Person)
		}
		if s.Current {
			fmt.Fprint(stdout, "  (aktuelle Fassung)")
		}
		fmt.Fprintln(stdout)
		if prose.Unchanged(s.Changes) {
			fmt.Fprint(stdout, "    kein Unterschied im Text\n\n")
			continue
		}
		fmt.Fprintf(stdout, "    %s\n", prose.Summary(s.Changes))
		for _, line := range strings.Split(strings.TrimRight(prose.Render(s.Changes), "\n"), "\n") {
			fmt.Fprintf(stdout, "    %s\n", line)
		}
		fmt.Fprintln(stdout)
	}
	fmt.Fprintln(stdout, "(Wortlaut der früheren Fassungen: --voll)")
}

func shortDay(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}
