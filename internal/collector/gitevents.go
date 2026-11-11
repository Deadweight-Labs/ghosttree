package collector

import (
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/gitcmd"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/transcript"
)

// GitEventRecorder ist optional, wie ActivityRecorder: ein Uploader, der keine
// Git-Vorgänge melden kann, archiviert Transkripte trotzdem.
type GitEventRecorder interface {
	RecordSessionGitEvents(events []store.SessionGitEvent) (store.SessionGitResult, error)
}

// Welche Aufrufe der Collector als Git-Vorgang liest (IDEA-31).
//
// Erkannt werden nur Shell-Werkzeuge (Claude Code: Bash; Codex: exec_command und
// verwandte) und nur ein Befehl am Anfang eines einfachen Befehls (gitcmd). Der
// Text "git commit" in einem Auftrag an einen Sub-Agenten, in einer Datei, die
// ein Write-Aufruf schreibt, oder in einem echo ist kein Commit: eine erste
// Zählung über die Zeile "[tool call: …]" ergab deshalb 185 statt 178 Sessions.
//
// Ein Aufruf und sein Ergebnis stehen in zwei Zeilen, oft in zwei SyncFile-Läufen
// (jede Schreibung löst einen aus). Der Aufruf wartet deshalb im Speicher auf sein
// Ergebnis. Ein Neustart des Collectors zwischen beiden verliert den Vorgang; das
// ist hinzunehmen, weil der Bestand ein Backfill nachholen kann (P5) und ein
// Aufruf nur Sekunden wartet.
const (
	// maxPendingGitCalls begrenzt die wartenden Aufrufe je Transkript. Ein
	// Aufruf, dessen Ergebnis nie kommt (abgebrochene Session), verdrängt so den
	// ältesten.
	maxPendingGitCalls = 64
	// pendingGitCallTTL: ein Aufruf, der so lange auf sein Ergebnis wartet, ist
	// verloren.
	pendingGitCallTTL = 6 * time.Hour
	// maxQueuedGitEvents begrenzt die Vorgänge, die auf eine erreichbare Route
	// warten.
	maxQueuedGitEvents = 2000
)

var codexShellTools = map[string]bool{
	"exec_command": true, "shell": true, "shell_command": true, "container.exec": true,
	"local_shell": true, "exec": true, "unified_exec": true,
}

type pendingGitCall struct {
	seq     int
	started string
	branch  string
	calls   []gitcmd.Call
}

// gitScan liest die Git-Vorgänge eines Transkripts und hält fest, was noch auf
// sein Ergebnis oder auf die Route wartet. Nur im Speicher: der Fortschritt steht
// im Offset der Datei.
type gitScan struct {
	harness, externalID string
	pending             map[string]*pendingGitCall
	order               []string
	queue               []store.SessionGitEvent
}

func newGitScan(harness, externalID string) *gitScan {
	return &gitScan{harness: harness, externalID: externalID, pending: map[string]*pendingGitCall{}}
}

// mightHoldGitCall ist der billige Vorfilter je Zeile: ohne ein "git " oder
// "gh pr" im Text kann kein Aufruf darin stehen, und ohne wartende Aufrufe
// interessiert eine Ergebniszeile nicht.
func mightHoldGitCall(line string) bool {
	return strings.Contains(line, "git ") || strings.Contains(line, "gh pr") || strings.Contains(line, "gh -R") || strings.Contains(line, "gh --repo")
}

func mightHoldResult(line string) bool {
	return strings.Contains(line, "tool_result") || strings.Contains(line, "function_call_output") || strings.Contains(line, "item_completed")
}

// line liest eine Originalzeile des Transkripts (vor dem Schlanken).
func (g *gitScan) line(seq int, line string) {
	call, result := mightHoldGitCall(line), len(g.pending) > 0 && mightHoldResult(line)
	if !call && !result {
		return
	}
	blocks := transcript.Parse(g.harness, seq, line).Blocks
	if len(blocks) == 0 {
		return
	}
	var branch string
	for _, b := range blocks {
		switch b.Kind {
		case transcript.KindTool:
			if !call {
				continue
			}
			cmd := g.command(b, line)
			if cmd == "" {
				continue
			}
			calls := gitcmd.Parse(cmd)
			if len(calls) == 0 {
				continue
			}
			if branch == "" {
				branch = claudeBranch(g.harness, line)
			}
			p := &pendingGitCall{seq: seq, started: b.Time, branch: branch, calls: calls}
			if b.HasOutput {
				// Aufruf und Ergebnis in einer Zeile (Codex item_completed).
				g.finish(p, b.Output, b.Failed, b.Time)
				continue
			}
			g.hold(b.CallID, p)
		case transcript.KindResult:
			if p, ok := g.pending[b.CallID]; ok && b.CallID != "" {
				g.drop(b.CallID)
				g.finish(p, b.Output, b.Failed, b.Time)
			}
		}
	}
}

// command gibt den Shell-Text eines Werkzeugaufrufs zurück, leer für ein
// anderes Werkzeug.
func (g *gitScan) command(b transcript.Block, line string) string {
	if g.harness != "codex" {
		if b.Tool != "Bash" {
			return ""
		}
		return b.Input
	}
	if !codexShellTools[b.Tool] {
		return ""
	}
	if b.HasOutput {
		// item_completed: transcript hat eine Argumentliste mit Leerzeichen
		// verbunden und die Wortgrenzen verloren; die Liste lesen wir selbst.
		if cmd := codexItemCommand(line); cmd != "" {
			return cmd
		}
	}
	if strings.HasPrefix(b.Input, "{") {
		// transcript hält bei einem Aufruf mit einer Liste als Befehl das ganze
		// Argument-JSON; die Liste lesen wir hier selbst.
		return codexArgvCommand(b.Input)
	}
	return b.Input
}

// codexItemCommand liest den Befehl eines abgeschlossenen Codex-Befehls
// (payload.item.command, als Text oder als Liste).
func codexItemCommand(line string) string {
	var l struct {
		Payload struct {
			Item struct {
				Command json.RawMessage `json:"command"`
			} `json:"item"`
		} `json:"payload"`
	}
	if json.Unmarshal([]byte(line), &l) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(l.Payload.Item.Command, &s) == nil && s != "" {
		return s
	}
	var argv []string
	if json.Unmarshal(l.Payload.Item.Command, &argv) == nil && len(argv) > 0 {
		return argvCommand(argv)
	}
	return ""
}

// codexArgvCommand liest {"command": ["bash","-lc","…"]} bzw. {"command": "…"}.
func codexArgvCommand(arguments string) string {
	var a struct {
		Command json.RawMessage `json:"command"`
		Cmd     json.RawMessage `json:"cmd"`
	}
	if json.Unmarshal([]byte(arguments), &a) != nil {
		return ""
	}
	for _, raw := range []json.RawMessage{a.Cmd, a.Command} {
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return s
		}
		var argv []string
		if json.Unmarshal(raw, &argv) == nil && len(argv) > 0 {
			return argvCommand(argv)
		}
	}
	return ""
}

// argvCommand macht aus einer Argumentliste einen Shell-Text. Ein Umschlag wie
// ["bash","-lc","git commit -m x"] ist schon der Text; sonst wird jedes Wort
// gequotet, damit gitcmd die Wortgrenzen wiederfindet.
func argvCommand(argv []string) string {
	switch base := argv[0][strings.LastIndexByte(argv[0], '/')+1:]; base {
	case "bash", "sh", "zsh", "dash", "ksh":
		for i := 1; i < len(argv)-1; i++ {
			if strings.HasPrefix(argv[i], "-") && !strings.HasPrefix(argv[i], "--") && strings.Contains(argv[i], "c") {
				return argv[i+1]
			}
		}
	}
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

// claudeBranch liest den Branch, auf dem Claude Code beim Aufruf stand ("gitBranch"
// steht in jeder Zeile). Eine Session wechselt den Branch; der Wert der Zeile ist
// genauer als der Branch bei der Anmeldung der Session.
func claudeBranch(harness, line string) string {
	if harness == "codex" {
		return ""
	}
	var l struct {
		GitBranch string `json:"gitBranch"`
	}
	if json.Unmarshal([]byte(line), &l) != nil {
		return ""
	}
	return l.GitBranch
}

func (g *gitScan) hold(id string, p *pendingGitCall) {
	if id == "" {
		return
	}
	cutoff := time.Now().Add(-pendingGitCallTTL)
	for len(g.order) > 0 {
		old, ok := g.pending[g.order[0]]
		if ok && len(g.pending) < maxPendingGitCalls {
			if t, err := time.Parse(time.RFC3339Nano, old.started); err != nil || t.After(cutoff) {
				break
			}
		}
		g.drop(g.order[0])
	}
	if _, dup := g.pending[id]; !dup {
		g.order = append(g.order, id)
	}
	g.pending[id] = p
}

func (g *gitScan) drop(id string) {
	delete(g.pending, id)
	for i, o := range g.order {
		if o == id {
			g.order = append(g.order[:i], g.order[i+1:]...)
			break
		}
	}
}

// finish macht aus einem Aufruf und seinem Ergebnis die Vorgänge, einen je Art.
func (g *gitScan) finish(p *pendingGitCall, output string, failed bool, ended string) {
	if p.started == "" {
		return
	}
	if ended == "" {
		ended = p.started
	}
	seen := map[string]bool{}
	for _, c := range p.calls {
		if seen[c.Kind] {
			continue
		}
		seen[c.Kind] = true
		ev := store.SessionGitEvent{
			Harness: g.harness, SessionExternalID: g.externalID, Seq: p.seq, Kind: c.Kind,
			StartedAt: p.started, EndedAt: ended, OK: !failed, Branch: p.branch,
		}
		switch c.Kind {
		case gitcmd.KindCommit:
			ev.Op = c.Op
			// Nur ein Befehl, der die Zeile "[branch sha] Titel" ausgibt, trägt eine
			// SHA in seiner Ausgabe.
			for _, k := range p.calls {
				if k.Kind == gitcmd.KindCommit && (k.Op == gitcmd.OpCommit || k.Op == gitcmd.OpAmend || k.Op == gitcmd.OpCherryPick) {
					ev.SHAs = gitcmd.CommitSHAs(output)
					break
				}
			}
		case gitcmd.KindPush:
			// Auf der Gegenseite, nicht der Branch der Session: so läuft er gegen den
			// head_ref eines PRs.
			ev.Branch = gitcmd.PushedBranch(output)
		case gitcmd.KindPRCreate:
			ev.PRRemote, ev.PR = gitcmd.PullCreated(output)
		case gitcmd.KindPRMerge:
			ev.PR = gitcmd.PullMerged(output)
			ev.PRRemote = repoRemote(c.Repo)
		}
		g.add(ev)
	}
}

func (g *gitScan) add(ev store.SessionGitEvent) {
	for i, q := range g.queue {
		if q.Seq == ev.Seq && q.Kind == ev.Kind {
			g.queue[i] = ev
			return
		}
	}
	g.queue = append(g.queue, ev)
	if len(g.queue) > maxQueuedGitEvents {
		g.queue = g.queue[len(g.queue)-maxQueuedGitEvents:]
	}
}

// gitEventsUnsupported: der Server kennt die Route nicht (älterer Server). Bis
// zum nächsten Start wird nichts mehr gesendet; ein Neustart nach dem Update des
// Servers holt es nach, denn der Zustand lebt nur im Speicher.
var gitEventsUnsupported atomic.Bool

var gitEventsWarn = &limitedLog{interval: time.Minute, now: time.Now, out: func(s string) { log.Print(s) }}

// send meldet die wartenden Vorgänge. Ein Fehler hält das Archivieren nicht auf:
// die Vorgänge bleiben in der Warteschlange und gehen mit dem nächsten Lauf.
func (g *gitScan) send(up Uploader) {
	if len(g.queue) == 0 {
		return
	}
	rec, ok := up.(GitEventRecorder)
	if !ok || gitEventsUnsupported.Load() {
		g.queue = g.queue[:0]
		return
	}
	for len(g.queue) > 0 {
		n := min(len(g.queue), store.MaxSessionGitEvents)
		_, err := rec.RecordSessionGitEvents(g.queue[:n])
		if err != nil {
			if errors.Is(err, store.ErrSessionGitEventsUnsupported) {
				gitEventsUnsupported.Store(true)
				g.queue = g.queue[:0]
				gitEventsWarn.printf("this server does not take git events of sessions yet; they are not sent until ctx watch restarts")
				return
			}
			if isRefused(err) {
				// Eine Ablehnung (fremde oder unbekannte Session, ungültiger Wert)
				// ändert sich nicht durch Wiederholen.
				g.queue = g.queue[n:]
				gitEventsWarn.printf("git events of session %s were refused: %v", g.externalID, err)
				continue
			}
			gitEventsWarn.printf("git events of session %s not recorded: %v", g.externalID, err)
			return
		}
		g.queue = g.queue[n:]
	}
}

// isRefused sagt, ob der Server den Stapel ablehnt (4xx außer 408 und 429).
func isRefused(err error) bool {
	var h interface{ HTTPStatus() int }
	if !errors.As(err, &h) {
		return false
	}
	s := h.HTTPStatus()
	return s >= 400 && s < 500 && s != 408 && s != 429
}

// repoRemote macht aus dem Wert von gh -R ("owner/name" oder "host/owner/name")
// die Remote, die der Server vergleicht. Ohne -R bleibt sie leer: der PR liegt im
// Repo des Arbeitsverzeichnisses, also im Projekt der Session. gh meint ohne Host
// github.com.
func repoRemote(repo string) string {
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	switch strings.Count(repo, "/") {
	case 1:
		return "github.com/" + repo
	case 2:
		return repo
	}
	return ""
}
