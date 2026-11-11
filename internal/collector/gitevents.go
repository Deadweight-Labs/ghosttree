package collector

import (
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/gitcmd"
	"github.com/Deadweight-Labs/ghosttree/internal/gitmsg"
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
	// cwd ist das Arbeitsverzeichnis des Werkzeugs beim Aufruf (Claude Code schreibt
	// es je Zeile, sonst das der Session).
	cwd   string
	calls []gitcmd.Call
}

// gitScan liest die Git-Vorgänge eines Transkripts und hält fest, was noch auf
// sein Ergebnis oder auf die Route wartet. Nur im Speicher: der Fortschritt steht
// im Offset der Datei.
type gitScan struct {
	harness, externalID string
	pending             map[string]*pendingGitCall
	order               []string
	queue               []store.SessionGitEvent
	// holdUntil und holdFor: bis wann nicht erneut gefragt wird, nachdem der Server
	// die Vorgänge aus einem Grund abgelehnt hat, der sich nicht von selbst ändert.
	holdUntil time.Time
	holdFor   time.Duration
	// observer liest nach einem erfolgreichen Commit-Aufruf das Reflog (P2). nil:
	// es wird nichts beobachtet (Tests, Bestandslauf ohne Repo).
	observer *observer
	// sessionCwd ist das Arbeitsverzeichnis der Session, wo die Zeile keines nennt.
	sessionCwd string
	// historic: die Zeilen sind alt (Bestandslauf). Ein wartender Aufruf verfällt
	// dann nicht nach pendingGitCallTTL, denn die Uhr der Zeilen ist nicht die von heute.
	historic bool
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
			p := &pendingGitCall{seq: seq, started: b.Time, branch: branch, cwd: g.callCwd(b, line), calls: calls}
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

// callCwd ist das Arbeitsverzeichnis, in dem der Aufruf lief: das der Zeile (Claude
// Code), das, was Codex dem Aufruf als workdir mitgibt, sonst das der Session.
func (g *gitScan) callCwd(b transcript.Block, line string) string {
	var cwd string
	if g.harness == "codex" {
		cwd = codexWorkdir(b.Input)
	} else {
		cwd = claudeCwd(line)
	}
	if cwd == "" {
		cwd = g.sessionCwd
	}
	return cwd
}

// claudeCwd liest das Arbeitsverzeichnis, das Claude Code in jede Zeile schreibt.
func claudeCwd(line string) string {
	var l struct {
		CWD string `json:"cwd"`
	}
	if json.Unmarshal([]byte(line), &l) != nil {
		return ""
	}
	return l.CWD
}

// codexWorkdir liest workdir aus den Argumenten eines Codex-Aufrufs.
func codexWorkdir(arguments string) string {
	if !strings.HasPrefix(arguments, "{") {
		return ""
	}
	var a struct {
		Workdir string `json:"workdir"`
	}
	if json.Unmarshal([]byte(arguments), &a) != nil {
		return ""
	}
	return a.Workdir
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
			if t, err := time.Parse(time.RFC3339Nano, old.started); g.historic || err != nil || t.After(cutoff) {
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
			// Das Reflog nennt die volle SHA auch dann, wenn die Ausgabe keine hat
			// (git commit -q). Nur nach einem erfolgreichen Aufruf.
			if !failed && g.observer != nil {
				ev.Observed = g.observer.observeCall(p.cwd, c.Dir, p.started, ended)
			}
			// Die Nachricht des Befehls, als Schlüssel: nie der Text verlässt den Speicher.
			ev.MsgTitle, ev.MsgFull = commitMessageKeys(p.calls)
		case gitcmd.KindPush:
			// Auf der Gegenseite, nicht der Branch der Session: so läuft er gegen den
			// head_ref eines PRs. Der Bereich old..new sagt, welche Commits der Push brachte.
			ev.PushRefs, ev.Branch, ev.PushOld, ev.PushNew = pushedRefs(output)
		case gitcmd.KindPRCreate:
			ev.PRRemote, ev.PR = gitcmd.PullCreated(output)
		case gitcmd.KindPRMerge:
			ev.PR = gitcmd.PullMerged(output)
			ev.PRRemote = repoRemote(c.Repo)
		}
		g.add(ev)
	}
}

// commitMessageKeys liest die Schlüssel der Nachricht eines Tool-Aufrufs. Trägt er mehr als
// einen Commit mit Nachricht (ein Skript, das zweimal committet), ist der Vorgang keinem
// Text zuzuordnen: dann keine Schlüssel. Ein Commit, dem git Zeilen anhängt (--signoff,
// --trailer), hat nur den Titel als sicheren Teil.
func commitMessageKeys(calls []gitcmd.Call) (title, full string) {
	var found *gitcmd.Call
	for i := range calls {
		c := &calls[i]
		if c.Kind != gitcmd.KindCommit || (c.Op != gitcmd.OpCommit && c.Op != gitcmd.OpAmend && c.Op != gitcmd.OpMerge) {
			continue
		}
		if found != nil {
			return "", ""
		}
		found = c
	}
	if found == nil || found.Message == "" {
		return "", ""
	}
	k := gitmsg.Of(found.Message)
	if found.MessageAdded {
		k.Full = ""
	}
	return k.Title, k.Full
}

// pushedRefs liest die Refs, die ein Push verändert hat: alle für den Server, und den
// ersten (mit seinem Bereich) für Branch, PushOld und PushNew des Vorgangs.
func pushedRefs(output string) (refs []store.PushedRef, branch, oldSHA, newSHA string) {
	for _, r := range gitcmd.PushRefs(output) {
		if !r.Pushed() {
			continue
		}
		refs = append(refs, store.PushedRef{Branch: r.Dst, Src: r.Src, Old: r.Old, New: r.New, NewBranch: r.NewBranch, Forced: r.Forced})
	}
	if len(refs) == 0 {
		return nil, "", "", ""
	}
	return refs, refs[0].Branch, refs[0].Old, refs[0].New
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

// HistoryScan liest die gespeicherten Zeilen einer abgeschlossenen Session mit derselben
// Erkennung wie der Collector (ctx db backfill-session-git). Es liest nur: kein Reflog,
// keine Route. Die Zeilen sind die geschlankten der Datenbank; slim lässt Aufrufe,
// Zeit, cwd, Branch und die kurzen Ergebnisse, auf die die Erkennung baut, stehen.
type HistoryScan struct{ g *gitScan }

func NewHistoryScan(harness, externalID string) *HistoryScan {
	g := newGitScan(harness, externalID)
	g.historic = true
	return &HistoryScan{g: g}
}

// Line nimmt eine Zeile in der Reihenfolge ihrer Nummern.
func (h *HistoryScan) Line(seq int, raw string) { h.g.line(seq, raw) }

// Events nennt die erkannten Vorgänge; ein Aufruf ohne Ergebnis in den Zeilen fehlt.
func (h *HistoryScan) Events() []store.SessionGitEvent { return h.g.queue }

// gitEventsUnsupported: der Server kennt die Route nicht (älterer Server). Bis
// zum nächsten Start wird nichts mehr gesendet; ein Neustart nach dem Update des
// Servers holt es nach, denn der Zustand lebt nur im Speicher.
var gitEventsUnsupported atomic.Bool

var gitEventsWarn = &limitedLog{interval: time.Minute, now: time.Now, out: func(s string) { log.Print(s) }}

// Wie send mit einer Antwort des Servers umgeht.
const (
	// maxGitSendAttempts: so viele Anfragen höchstens je send, auch wenn ein Stapel
	// halbiert werden muss, um den unbrauchbaren Vorgang zu finden. Was darüber
	// hinausgeht, bleibt in der Warteschlange und geht mit dem nächsten Lauf.
	maxGitSendAttempts = 40
	// gitHoldMin und gitHoldMax: nach einer Antwort, die sich nicht von selbst ändert
	// (Recht, Maschinenbindung, Tagesgrenze), fragt der Collector erst nach einer Pause
	// wieder, die sich bis zur Obergrenze verdoppelt.
	gitHoldMin = time.Minute
	gitHoldMax = time.Hour
)

// send meldet die wartenden Vorgänge. Ein Fehler hält das Archivieren nicht auf:
// die Vorgänge bleiben in der Warteschlange und gehen mit dem nächsten Lauf.
//
// Verworfen wird nie mehr als ein Vorgang, der selbst unbrauchbar ist: antwortet
// der Server mit 400, 413 oder 422, wird der Stapel halbiert, bis der Vorgang
// einzeln feststeht; nur der wird mit seinem Grund protokolliert und verworfen. Jede
// andere Ablehnung (401, 403, 409, 429 …) ändert sich nicht durch das Ändern des
// Stapels und trifft die Session als Ganzes: die Vorgänge bleiben zurückgestellt, die
// Meldung wird mit wachsender Pause wiederholt, und der Grund steht im Protokoll.
func (g *gitScan) send(up Uploader) {
	if len(g.queue) == 0 {
		return
	}
	rec, ok := up.(GitEventRecorder)
	if !ok || gitEventsUnsupported.Load() {
		g.queue = g.queue[:0]
		return
	}
	if time.Now().Before(g.holdUntil) {
		return
	}
	size := min(len(g.queue), store.MaxSessionGitEvents)
	for attempts := 0; len(g.queue) > 0; attempts++ {
		if attempts >= maxGitSendAttempts {
			gitEventsWarn.printf("git events of session %s: still looking for the event the server refuses; %d wait for the next run", g.externalID, len(g.queue))
			return
		}
		n := min(size, len(g.queue))
		res, err := rec.RecordSessionGitEvents(g.queue[:n])
		if err == nil {
			g.queue = g.queue[n:]
			g.holdFor = 0
			if res.Rejected > 0 || res.Capped > 0 {
				gitEventsWarn.printf("git events of session %s: the server dropped %d invalid (%s) and %d over its limits", g.externalID, res.Rejected, res.Reason, res.Capped)
			}
			size = min(len(g.queue), store.MaxSessionGitEvents)
			continue
		}
		if errors.Is(err, store.ErrSessionGitEventsUnsupported) {
			gitEventsUnsupported.Store(true)
			g.queue = g.queue[:0]
			gitEventsWarn.printf("this server does not take git events of sessions yet; they are not sent until ctx watch restarts")
			return
		}
		switch status := httpStatusOf(err); {
		case status == 400 || status == 413 || status == 422:
			if n > 1 {
				size = (n + 1) / 2
				continue
			}
			// Der Vorgang allein ist unbrauchbar.
			ev := g.queue[0]
			g.queue = g.queue[1:]
			gitEventsWarn.printf("git event %s at line %d of session %s was refused and dropped: %v", ev.Kind, ev.Seq, g.externalID, err)
			size = min(len(g.queue), store.MaxSessionGitEvents)
		case status >= 400 && status < 500:
			g.pause()
			gitEventsWarn.printf("git events of session %s were refused (%d) and wait: %v", g.externalID, status, err)
			return
		default:
			gitEventsWarn.printf("git events of session %s not recorded: %v", g.externalID, err)
			return
		}
	}
}

// pause legt die Pause fest, nach der send wieder fragt.
func (g *gitScan) pause() {
	switch {
	case g.holdFor <= 0:
		g.holdFor = gitHoldMin
	case g.holdFor < gitHoldMax:
		g.holdFor = min(2*g.holdFor, gitHoldMax)
	}
	g.holdUntil = time.Now().Add(g.holdFor)
}

// httpStatusOf liest den Statuscode eines Fehlers der Route, 0 ohne einen.
func httpStatusOf(err error) int {
	var h interface{ HTTPStatus() int }
	if errors.As(err, &h) {
		return h.HTTPStatus()
	}
	return 0
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
