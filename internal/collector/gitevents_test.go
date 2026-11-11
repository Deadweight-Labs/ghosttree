package collector

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitmsg"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// keys sind die Schlüssel, unter denen der Vorgang die Nachricht meldet: nie der Text.
func keys(msg string) (title, full string) {
	k := gitmsg.Of(msg)
	return k.Title, k.Full
}

// Die Testdaten testdata/git_claude.jsonl und git_codex.jsonl sind Transkriptzeilen
// in der Form, die Claude Code (2.1.x) und Codex (0.139) schreiben, mit
// ausgetauschten Namen, Kennungen und Pfaden. Sie enthalten, was im Bestand
// vorkommt: git commit -q ohne Ausgabe, mit Ausgabe, fehlgeschlagen, mit Heredoc,
// Push, gh pr create und gh pr merge, dazu die Erwähnungen, die keine Aufrufe sind
// (ein Auftrag an einen Sub-Agenten, ein Write, ein echo).

func readLines(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func scanAll(t *testing.T, harness, id, file string) []store.SessionGitEvent {
	t.Helper()
	g := newGitScan(harness, id)
	for seq, l := range readLines(t, file) {
		g.line(seq, l)
	}
	if len(g.pending) != 0 {
		t.Errorf("every call has its result in the file, still waiting: %v", g.order)
	}
	return g.queue
}

// withKeys trägt die Schlüssel einer Nachricht an einem Vorgang ein.
func withKeys(e store.SessionGitEvent, msg string) store.SessionGitEvent {
	e.MsgTitle, e.MsgFull = keys(msg)
	return e
}

func TestGitScanReadsClaudeTranscript(t *testing.T) {
	const id = "11111111-2222-4333-8444-555555555555"
	got := scanAll(t, "claude-code", id, "git_claude.jsonl")
	h := func(e store.SessionGitEvent) store.SessionGitEvent {
		e.Harness, e.SessionExternalID = "claude-code", id
		return e
	}
	title := func(msg string) string { t, _ := keys(msg); return t }
	want := []store.SessionGitEvent{
		// git commit -q: nichts in der Ausgabe, nur Zeit und Ergebnis.
		h(store.SessionGitEvent{Seq: 1, Kind: "commit", Op: "commit", StartedAt: "2026-11-08T16:06:33.854Z", EndedAt: "2026-11-08T16:06:33.965Z", OK: true, Branch: "feat/example", MsgTitle: title("fix: handle empty input")}),
		// Ohne -q steht die SHA in der Ausgabe.
		h(store.SessionGitEvent{Seq: 3, Kind: "commit", Op: "commit", StartedAt: "2026-11-08T16:10:00.100Z", EndedAt: "2026-11-08T16:10:00.420Z", OK: true, Branch: "feat/example", SHAs: []string{"1a2b3c4"}, MsgTitle: title("feat: add parser")}),
		// Fehlgeschlagen: der Vorgang bleibt (Abdeckung), mit ok=false.
		h(store.SessionGitEvent{Seq: 5, Kind: "commit", Op: "commit", StartedAt: "2026-11-08T16:11:00.000Z", EndedAt: "2026-11-08T16:11:00.200Z", OK: false, Branch: "feat/example", MsgTitle: title("wip")}),
		// Der Commit mit Heredoc ist einer; das "git push" im Text der Nachricht nicht.
		h(withKeys(store.SessionGitEvent{Seq: 11, Kind: "commit", Op: "commit", StartedAt: "2026-11-08T16:11:40.000Z", EndedAt: "2026-11-08T16:11:40.300Z", OK: true, Branch: "feat/example"},
			"fix: tidy the parser\n\nThe agent used to run git push here, which is not part of this commit.")),
		// Ein neuer Branch: kein Bereich, aber das Ref.
		h(store.SessionGitEvent{Seq: 13, Kind: "push", StartedAt: "2026-11-08T16:12:00.000Z", EndedAt: "2026-11-08T16:12:02.500Z", OK: true, Branch: "feat/example",
			PushRefs: []store.PushedRef{{Branch: "feat/example", Src: "feat/example", NewBranch: true}}}),
		h(store.SessionGitEvent{Seq: 15, Kind: "pr_create", StartedAt: "2026-11-08T16:13:00.000Z", EndedAt: "2026-11-08T16:13:03.100Z", OK: true, Branch: "feat/example", PR: 12, PRRemote: "github.com/example-org/example-repo"}),
		h(store.SessionGitEvent{Seq: 17, Kind: "pr_merge", StartedAt: "2026-11-08T17:00:00.000Z", EndedAt: "2026-11-08T17:00:04.000Z", OK: true, Branch: "feat/example", PR: 12}),
	}
	if !reflect.DeepEqual(got, want) {
		for i := range got {
			t.Logf("got  %d: %+v", i, got[i])
		}
		t.Fatalf("events differ:\n got %d events\nwant %d events", len(got), len(want))
	}
	// Auftrag an einen Sub-Agenten (7), Write (8) und echo (9) erwähnen den Befehl
	// und ergeben keine Zeile.
	for _, e := range got {
		if e.Seq == 7 || e.Seq == 8 || e.Seq == 9 {
			t.Errorf("a mention of git commit is not a call: %+v", e)
		}
	}
}

func TestGitScanReadsCodexTranscript(t *testing.T) {
	const id = "019f0000-0000-7000-8000-000000000001"
	got := scanAll(t, "codex", id, "git_codex.jsonl")
	h := func(e store.SessionGitEvent) store.SessionGitEvent {
		e.Harness, e.SessionExternalID = "codex", id
		return e
	}
	title := func(msg string) string { t, _ := keys(msg); return t }
	want := []store.SessionGitEvent{
		h(store.SessionGitEvent{Seq: 1, Kind: "commit", Op: "commit", StartedAt: "2026-11-08T10:01:00.100Z", EndedAt: "2026-11-08T10:01:00.140Z", OK: true, SHAs: []string{"711290f"}, MsgTitle: title("init project")}),
		h(store.SessionGitEvent{Seq: 3, Kind: "commit", Op: "commit", StartedAt: "2026-11-08T10:02:00.000Z", EndedAt: "2026-11-08T10:02:00.050Z", OK: false, MsgTitle: title("nothing new")}),
		// Der Befehl steht als Liste ["bash","-lc","…"]; das Ergebnis im alten
		// Umschlag {"output":…,"metadata":{"exit_code":0}}.
		h(store.SessionGitEvent{Seq: 5, Kind: "push", StartedAt: "2026-11-08T10:03:00.000Z", EndedAt: "2026-11-08T10:03:03.000Z", OK: true, Branch: "main", PushOld: "0a1b2c3", PushNew: "4d5e6f7",
			PushRefs: []store.PushedRef{{Branch: "main", Src: "main", Old: "0a1b2c3", New: "4d5e6f7"}}}),
		// Aufruf und Ergebnis in einer Zeile (item_completed), Befehl als Liste.
		h(store.SessionGitEvent{Seq: 9, Kind: "commit", Op: "commit", StartedAt: "2026-11-08T10:05:00.000Z", EndedAt: "2026-11-08T10:05:00.000Z", OK: true, MsgTitle: title("fix: quiet commit")}),
		h(store.SessionGitEvent{Seq: 9, Kind: "pr_create", StartedAt: "2026-11-08T10:05:00.000Z", EndedAt: "2026-11-08T10:05:00.000Z", OK: true, PR: 7, PRRemote: "github.com/example-org/example-repo"}),
	}
	if !reflect.DeepEqual(got, want) {
		for i := range got {
			t.Logf("got  %d: %+v", i, got[i])
		}
		t.Fatalf("events differ: %d vs %d", len(got), len(want))
	}
}

func TestGitScanKeepsACallUntilItsResultArrives(t *testing.T) {
	lines := readLines(t, "git_claude.jsonl")
	g := newGitScan("claude-code", "s")
	g.line(1, lines[1])
	if len(g.queue) != 0 || len(g.pending) != 1 {
		t.Fatalf("the call waits: queue %d, pending %d", len(g.queue), len(g.pending))
	}
	g.line(2, lines[2])
	if len(g.queue) != 1 || len(g.pending) != 0 {
		t.Fatalf("the result completes it: queue %d, pending %d", len(g.queue), len(g.pending))
	}
	// Dieselbe Zeile noch einmal (ein wiederholter Stapel) ändert nichts.
	g.line(1, lines[1])
	g.line(2, lines[2])
	if len(g.queue) != 1 {
		t.Fatalf("a replayed batch must not double the event: %+v", g.queue)
	}
}

func TestGitScanBoundsWaitingCalls(t *testing.T) {
	g := newGitScan("claude-code", "s")
	call := func(i int) string {
		return `{"type":"assistant","timestamp":"2099-01-01T00:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_` +
			strings.Repeat("x", 3) + string(rune('A'+i%26)) + string(rune('a'+i/26)) + `","name":"Bash","input":{"command":"git commit -qm x"}}]}}`
	}
	for i := 0; i < 3*maxPendingGitCalls; i++ {
		g.line(i, call(i))
	}
	if len(g.pending) > maxPendingGitCalls {
		t.Fatalf("waiting calls are bounded, got %d", len(g.pending))
	}
}

// gitUp ist ein Uploader, der auch Git-Vorgänge annimmt.
type gitUp struct {
	fakeUp
	events []store.SessionGitEvent
	err    error
	calls  int
}

func (g *gitUp) RecordSessionGitEvents(ev []store.SessionGitEvent) (store.SessionGitResult, error) {
	g.calls++
	if g.err != nil {
		return store.SessionGitResult{}, g.err
	}
	g.events = append(g.events, ev...)
	return store.SessionGitResult{Recorded: len(ev)}, nil
}

func writeGitTranscript(t *testing.T, name string, lines []string) string {
	t.Helper()
	dir := t.TempDir()
	fp := filepath.Join(dir, name)
	if err := os.WriteFile(fp, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return fp
}

func resetGitSupport(t *testing.T) {
	t.Helper()
	gitEventsUnsupported.Store(false)
	t.Cleanup(func() { gitEventsUnsupported.Store(false) })
}

func TestSyncFileSendsGitEventsAfterTheLinesAreUploaded(t *testing.T) {
	resetGitSupport(t)
	lines := readLines(t, "git_claude.jsonl")
	fp := writeGitTranscript(t, "11111111-2222-4333-8444-555555555555.jsonl", lines[:2]) // bis zum Aufruf
	st := newTestState(filepath.Dir(fp))
	up := &gitUp{}
	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
		t.Fatal(err)
	}
	if len(up.events) != 0 {
		t.Fatalf("the result has not arrived yet: %+v", up.events)
	}
	// Das Ergebnis kommt in einem zweiten Lauf: der Aufruf hat darauf gewartet.
	f, _ := os.OpenFile(fp, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(strings.Join(lines[2:], "\n") + "\n")
	f.Close()
	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
		t.Fatal(err)
	}
	if len(up.events) != 7 {
		t.Fatalf("want the seven git events of the transcript, got %d: %+v", len(up.events), up.events)
	}
	if up.events[0].SessionExternalID != "11111111-2222-4333-8444-555555555555" || up.events[0].Harness != "claude-code" || up.events[0].Seq != 1 {
		t.Errorf("first event: %+v", up.events[0])
	}
	if len(up.chunks[1]) != len(lines) {
		t.Errorf("the transcript is archived in full: %d of %d lines", len(up.chunks[1]), len(lines))
	}
}

func TestSyncFileKeepsEventsWhenTheServerIsUnreachableAndRetries(t *testing.T) {
	resetGitSupport(t)
	lines := readLines(t, "git_claude.jsonl")
	fp := writeGitTranscript(t, "11111111-2222-4333-8444-555555555555.jsonl", lines[:3])
	st := newTestState(filepath.Dir(fp))
	up := &gitUp{err: errors.New("connection refused")}
	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
		t.Fatalf("a failing git route must not fail the archive: %v", err)
	}
	if len(up.chunks[1]) != 3 {
		t.Fatalf("lines must be archived: %d", len(up.chunks[1]))
	}
	up.err = nil
	f, _ := os.OpenFile(fp, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(lines[3] + "\n")
	f.Close()
	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
		t.Fatal(err)
	}
	if len(up.events) != 1 || up.events[0].Seq != 1 {
		t.Fatalf("the waiting event goes with the next run: %+v", up.events)
	}
}

func TestSyncFileAgainstAnOlderServerWithoutTheRoute(t *testing.T) {
	resetGitSupport(t)
	lines := readLines(t, "git_claude.jsonl")
	fp := writeGitTranscript(t, "11111111-2222-4333-8444-555555555555.jsonl", lines[:3])
	st := newTestState(filepath.Dir(fp))
	up := &gitUp{err: store.ErrSessionGitEventsUnsupported}
	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
		t.Fatalf("an older server must not break the collector: %v", err)
	}
	if !gitEventsUnsupported.Load() || up.calls != 1 {
		t.Fatalf("one try, then silence: calls=%d unsupported=%v", up.calls, gitEventsUnsupported.Load())
	}
	f, _ := os.OpenFile(fp, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(strings.Join(lines[3:], "\n") + "\n")
	f.Close()
	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
		t.Fatal(err)
	}
	if up.calls != 1 {
		t.Fatalf("no further tries until the next start: %d calls", up.calls)
	}
	if len(up.chunks[1]) != len(lines) {
		t.Errorf("the transcript is archived in full: %d of %d", len(up.chunks[1]), len(lines))
	}
}

func TestSyncFileWithAnUploaderThatTakesNoGitEvents(t *testing.T) {
	resetGitSupport(t)
	lines := readLines(t, "git_claude.jsonl")
	fp := writeGitTranscript(t, "11111111-2222-4333-8444-555555555555.jsonl", lines)
	st := newTestState(filepath.Dir(fp))
	up := &fakeUp{}
	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
		t.Fatal(err)
	}
	if len(up.chunks[1]) != len(lines) {
		t.Fatalf("archived %d of %d", len(up.chunks[1]), len(lines))
	}
}
