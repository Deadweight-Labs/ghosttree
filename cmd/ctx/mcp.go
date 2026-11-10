package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/mcpserver"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshotmirror"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// currentAxes derives the session context from the working directory and the
// configured machine name.
func currentAxes(machine string) scope.Axes {
	return currentGitContext(machine).axes
}

func currentSessionRef() string {
	for _, key := range []string{"CODEX_SESSION_ID", "CODEX_THREAD_ID", "CLAUDE_CODE_SESSION_ID", "OPENCODE_SESSION_ID"} {
		if ref := os.Getenv(key); ref != "" {
			return ref
		}
	}
	return ""
}

// mcpUsageHarness is the harness whose ghosttree tool calls ctx mcp counts
// itself. Claude Code's are counted by its PostToolUse hook, which also knows
// whether a subagent made the call.
func mcpUsageHarness() string {
	switch {
	case os.Getenv("CODEX_SESSION_ID") != "" || os.Getenv("CODEX_THREAD_ID") != "":
		return "codex"
	case os.Getenv("OPENCODE_SESSION_ID") != "":
		return "opencode"
	}
	return ""
}

// coordAgentOverride ist die Koordinationsidentität aus GHOSTTREE_AGENT_ID, die
// `ctx claude` setzt, damit ctx mcp und ctx channel derselbe Agent sind. Sie
// ersetzt NICHT die Harness-Session-ID (Suche, Snapshots und unterbrochene
// Arbeit brauchen die echte). Ein codex oder opencode, das in einer
// ctx-claude-Session startet, erbt die Variable, trägt aber ein fremdes
// Provider-Präfix und ignoriert sie. Ein dort ohne Launcher gestartetes claude
// ist nicht zu unterscheiden und teilt sich die Identität.
func coordAgentOverride() string {
	id := strings.TrimSpace(os.Getenv(agentIDEnv))
	if id == "" {
		return ""
	}
	harness := ""
	switch {
	case os.Getenv("CODEX_SESSION_ID") != "" || os.Getenv("CODEX_THREAD_ID") != "":
		harness = "codex"
	case os.Getenv("OPENCODE_SESSION_ID") != "":
		harness = "opencode"
	case os.Getenv("CLAUDE_CODE_SESSION_ID") != "":
		harness = "claude"
	}
	if harness != "" && !strings.HasPrefix(id, harness+":") {
		return ""
	}
	return id
}

// agentRoleFromEnv liest die per ctx claude --role angeforderte Rolle. Sie
// gilt nur zusammen mit einer akzeptierten Launcher-Identität: eine geerbte
// Variable ohne passende Agenten-ID wird ignoriert. Der Server kappt die Rolle
// ohnehin live am Rang des Kontos.
func agentRoleFromEnv() string {
	if coordAgentOverride() == "" {
		return ""
	}
	role := strings.TrimSpace(os.Getenv(agentRoleEnv))
	if !store.ValidAgentRole(role) {
		return ""
	}
	return role
}

// agentSessionFromEnv ist die vom Launcher vorgegebene Session-UUID. Wie die
// Rolle gilt sie nur zusammen mit einer akzeptierten Launcher-Identität.
func agentSessionFromEnv() string {
	if coordAgentOverride() == "" {
		return ""
	}
	id := strings.TrimSpace(os.Getenv(sessionIDEnv))
	if !store.ValidExternalID(id) {
		return ""
	}
	return id
}

// registeredSessionID ist die Session, die ein Agent bei der Anmeldung meldet:
// die des Launchers, sonst die Session-ID des Harness. Der Server prüft beim
// Lesen, ob sie zum selben Konto gehört.
func registeredSessionID() string {
	if id := agentSessionFromEnv(); id != "" {
		return id
	}
	if id := currentSessionRef(); store.ValidExternalID(id) {
		return id
	}
	return ""
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
	if hooksOff() {
		// A test harness's agents read what they need and write nothing.
		c.SetReadOnly()
	}
	// Jeder Schreibzugriff dieser Sitzung trägt den Git-Stand des
	// Arbeitsverzeichnisses mit (REQ-597). Das Verzeichnis wird je Aufruf
	// gelesen, weil der Agent HEAD zwischen zwei Schreibzugriffen verschiebt;
	// ohne Git ist der Anker leer und der Schreibzugriff läuft ohne.
	c.SetAnchorSource(func() gitanchor.Anchor {
		cwd, err := os.Getwd()
		if err != nil {
			return gitanchor.Anchor{}
		}
		return collector.ResolveAnchor(cwd)
	})
	// Dieselbe Sitzung geht auf den verankerten Routen als Kennung mit; Pfade und
	// Bereiche ermittelt der Server (REQ-600).
	c.SetOriginSession(currentSessionRef())
	srv := mcpserver.NewServer(c, hctx.axes, hctx.activation)
	srv.SetLineageResolver(func(branch string) []string {
		cwd, err := os.Getwd()
		if err != nil {
			return nil
		}
		return collector.BranchLineageOf(cwd, branch)
	})
	// Branch und Kette werden je Aufruf neu gelesen: der Agent legt mitten in
	// der Sitzung einen Branch an (REQ-601, PAIN-57).
	srv.SetLive(func() mcpserver.Live {
		cwd, err := os.Getwd()
		if err != nil {
			return mcpserver.Live{}
		}
		g := collector.ResolveGitContext(cwd)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return mcpserver.Live{Branch: g.Branch, Lineage: g.Lineage, CWD: cwd, Trunk: collector.IsDefaultBranch(ctx, cwd, g.Branch)}
	})
	srv.SetSessionRef(currentSessionRef())
	srv.SetUsageHarness(mcpUsageHarness())
	srv.SetCoordRef(coordAgentOverride())
	srv.SetAgentRole(agentRoleFromEnv())
	srv.SetSessionID(agentSessionFromEnv())
	srv.SetRepoRoot(hctx.root)
	srv.SetAfterSnapshot(func(ctx context.Context, project string) error {
		return snapshotmirror.Rebuild(ctx, mcpSnapshotLister{client: c}, hctx.root, project)
	})
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

type mcpSnapshotLister struct{ client *client.Client }

func (l mcpSnapshotLister) ListContextSnapshots(ctx context.Context, filter snapshot.ListFilter) (snapshot.SnapshotPage, error) {
	return l.client.ContextSnapshots(ctx, filter)
}
