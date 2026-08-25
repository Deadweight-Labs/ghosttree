package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/mcpserver"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// currentAxes derives the session context from the working directory and the
// configured machine name.
func currentAxes(machine string) scope.Axes {
	return currentGitContext(machine).axes
}

type harnessContext struct {
	axes       scope.Axes
	activation activation.Context
	// root ist die Repo-Wurzel und leer, wenn die Sitzung ausserhalb eines
	// Repos läuft. Ghost-Dateien brauchen sie, weil activation.Context.RepoPath
	// der Pfad *im* Repo ist und nicht die Wurzel.
	root string
}

func currentGitContext(machine string) harnessContext {
	cwd, err := os.Getwd()
	if err != nil {
		return harnessContext{axes: scope.Axes{Machine: machine}}
	}
	g := collector.ResolveGitContext(cwd)
	return harnessContext{
		axes:       scope.Axes{Project: g.Project, Branch: g.Branch, Lineage: g.Lineage, Machine: machine},
		activation: activation.Context{RepoPath: g.RepoPath},
		root:       g.Root,
	}
}

// treeSettle ist die Ruhezeit, nach der ein Schwung Beschreibungen als
// abgeschlossen gilt. Gegriffen, nicht gemessen: lang genug, dass ein Agent,
// der achtzehn Dateien am Stück beschreibt, einen Neuschrieb auslöst statt
// achtzehn; kurz genug, dass der Baum noch in derselben Sitzung dasteht.
const treeSettle = 2 * time.Second

// debounce sammelt schnell aufeinanderfolgende Aufrufe zu einem. Jeder Aufruf
// schiebt den Termin nach hinten; erst wenn es still wird, läuft f.
//
// Der Baum wird vollständig neu geschrieben, mit Entfernen und Umbenennen des
// Wurzelverzeichnisses. Achtzehn Beschreibungen am Stück waren achtzehn solcher
// Tauschvorgänge, und wer währenddessen im Baum las, griff kurz in einen Pfad,
// den es gerade nicht gab.
//
// Zwei Schlösser, nicht eins. Das erste schützt den Timer. Das zweite
// serialisiert die AUSFÜHRUNG, und das ist keine Vorsicht auf Verdacht:
// timer.Stop() greift nicht mehr, wenn der Timer bereits gefeuert hat, und f
// läuft dann schon in seiner eigenen Goroutine. Dauert dieser Lauf länger als
// die Ruhezeit — WriteTree holt den Baum über HTTP und startet für jede
// beschriebene Datei git-Prozesse, das überschreitet zwei Sekunden mühelos —,
// dann startet der nächste Timer einen zweiten Lauf daneben. Beide bauen im
// selben .tmp-Verzeichnis, und das RemoveAll des einen reisst dem anderen den
// Bau unter den Füssen weg.
//
// Verpasst der Timer das Sitzungsende, ist nichts verloren: der
// session-start-Hook schreibt den Baum beim nächsten Mal ohnehin, und der Baum
// ist eine Projektion — die Wahrheit steht in der Datenbank.
func debounce(after time.Duration, f func()) func() {
	var scheduleMu, runMu sync.Mutex
	var timer *time.Timer
	run := func() {
		runMu.Lock()
		defer runMu.Unlock()
		f()
	}
	return func() {
		scheduleMu.Lock()
		defer scheduleMu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(after, run)
	}
}

func cmdMCP(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stdout)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		// stderr: stdout is the JSON-RPC channel.
		fmt.Fprintf(os.Stderr, "load config: %v (run 'ctx setup' first)\n", err)
		return 1
	}
	hctx := currentGitContext(cfg.Machine)
	c := client.New(cfg)
	srv := mcpserver.NewServer(c, hctx.axes, hctx.activation)
	srv.SetRepoRoot(hctx.root)
	srv.SetAfterWrite(debounce(treeSettle, func() {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		// Fehler werden geschluckt: der Baum ist eine Projektion, und ein
		// misslungener Neuschrieb darf ein gelungenes Beschreiben nicht als
		// Fehlschlag aussehen lassen.
		_ = WriteTree(c, hctx.axes.Project, hctx.root, home)
	}))
	if err := mcpserver.Run(context.Background(), srv, version); err != nil {
		fmt.Fprintf(os.Stderr, "mcp: %v\n", err)
		return 1
	}
	return 0
}
