package collector

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Die Testdaten git_signals_claude.jsonl und git_signals_codex.jsonl haben die Form echter
// Transkriptzeilen mit ausgetauschten Namen. Sie zeigen, was der Collector für die Zuordnung
// über Nachricht und Push-Bereich meldet (P6): Schlüssel der Nachricht, nie ihren Text, und
// die Refs eines Push mit ihrem Bereich.

// kindNo trennt Commit und Push derselben Zeile (ein Aufruf "git commit … && git push").
func kindNo(kind string) int {
	if kind == "push" {
		return 1
	}
	return 0
}

func TestGitScanReportsMessageKeysAndPushRangesOfClaude(t *testing.T) {
	const id = "11111111-2222-4333-8444-555555555555"
	got := scanAll(t, "claude-code", id, "git_signals_claude.jsonl")
	byseq := map[int]store.SessionGitEvent{}
	for _, e := range got {
		if e.Kind == "commit" || e.Kind == "push" {
			byseq[e.Seq*10+kindNo(e.Kind)] = e
		}
	}
	// Zwei -m: Titel und Rumpf als Absätze.
	title, full := keys("feat(parser): read push ranges\n\nZweiter Absatz mit Details zum Bereich.")
	if e := byseq[0*10]; e.MsgTitle != title || e.MsgFull != full || full == "" {
		t.Errorf("two -m: %+v", e)
	}
	// -F - mit Heredoc.
	title, full = keys("fix(web): wrap long titles\n\n- the card no longer overflows\n- long words break at the edge")
	if e := byseq[2*10]; e.MsgTitle != title || e.MsgFull != full {
		t.Errorf("heredoc: %+v", e)
	}
	// "$(cat <<'EOF' … EOF)" mit ungeraden Anführungszeichen; der Push dahinter ist -q.
	title, full = keys("fix: it's quoted, don't panic\n\nsays \"hi")
	if e := byseq[4*10]; e.MsgTitle != title || e.MsgFull != full {
		t.Errorf("substitution: %+v", e)
	}
	if e := byseq[4*10+1]; e.Kind != "push" || len(e.PushRefs) != 0 || e.PushNew != "" {
		t.Errorf("the quiet push behind the commit has no range: %+v", e)
	}
	// Eine Ersetzung in der Nachricht: der Text steht erst nach der Shell fest, keine Schlüssel.
	if e := byseq[6*10]; e.Kind != "commit" || e.MsgTitle != "" || e.MsgFull != "" {
		t.Errorf("command substitution in the message: %+v", e)
	}
	// --signoff: git hängt eine Zeile an, nur der Titel gilt.
	title, _ = keys("chore: signed change")
	if e := byseq[8*10]; e.MsgTitle != title || e.MsgFull != "" {
		t.Errorf("signoff: %+v", e)
	}
	// Ein stiller Push gibt nichts aus: kein Ref, kein Bereich.
	if e := byseq[10*10+1]; e.Kind != "push" || len(e.PushRefs) != 0 || e.PushNew != "" || e.Branch != "" {
		t.Errorf("quiet push: %+v", e)
	}
	// Zwei Refs: der erste mit seinem (leeren) Bereich bestimmt Branch und PushOld/PushNew.
	e := byseq[12*10+1]
	wantRefs := []store.PushedRef{
		{Branch: "feat/a", Src: "feat/a", NewBranch: true},
		{Branch: "feat/b", Src: "feat/b", Old: "1a2b3c4", New: "5d6e7f8"},
	}
	if e.Branch != "feat/a" || e.PushNew != "" || !reflect.DeepEqual(e.PushRefs, wantRefs) {
		t.Errorf("two refs: %+v", e)
	}
	// Force-Push.
	e = byseq[14*10+1]
	if e.Branch != "feat/c" || e.PushOld != "2c23513" || e.PushNew != "f19aa8d" || len(e.PushRefs) != 1 || !e.PushRefs[0].Forced {
		t.Errorf("forced: %+v", e)
	}
	// Einer angenommen, einer abgelehnt: der Vorgang schlug fehl (Exit 1), meldet aber das Ref,
	// das ankam, und nur das.
	e = byseq[16*10+1]
	if e.OK || e.Branch != "feat/d" || e.PushOld != "9f8e7d6" || e.PushNew != "0a1b2c3" ||
		!reflect.DeepEqual(e.PushRefs, []store.PushedRef{{Branch: "feat/d", Src: "feat/d", Old: "9f8e7d6", New: "0a1b2c3"}}) {
		t.Errorf("rejected and accepted: %+v", e)
	}
}

func TestGitScanReportsMessageKeysAndPushRangesOfCodex(t *testing.T) {
	const id = "019f0000-0000-7000-8000-000000000002"
	got := scanAll(t, "codex", id, "git_signals_codex.jsonl")
	byseq := map[int]store.SessionGitEvent{}
	for _, e := range got {
		if e.Kind == "commit" || e.Kind == "push" {
			byseq[e.Seq*10+kindNo(e.Kind)] = e
		}
	}
	title, full := keys("feat(api): add the export route\n\nThe route streams rows.")
	if e := byseq[0*10]; e.MsgTitle != title || e.MsgFull != full {
		t.Errorf("two -m: %+v", e)
	}
	title, full = keys("fix(api): reject empty ids\n\nA blank id used to reach the store.")
	if e := byseq[2*10]; e.MsgTitle != title || e.MsgFull != full {
		t.Errorf("bash -lc heredoc: %+v", e)
	}
	// Der Befehl nennt HEAD:feat/export, die Ausgabe den Bereich auf der Gegenseite.
	if e := byseq[3*10+1]; e.Branch != "feat/export" || e.PushOld != "3b4c5d6" || e.PushNew != "7e8f9a0" {
		t.Errorf("push of HEAD: %+v", e)
	}
	if e := byseq[4*10+1]; e.Branch != "feat/export" || e.PushOld != "7e8f9a0" || e.PushNew != "c1d2e3f" || !e.PushRefs[0].Forced {
		t.Errorf("forced push: %+v", e)
	}
}

// Was die Route empfängt, enthält nie den Text einer Nachricht: nur die beiden Schlüssel.
func TestGitScanNeverCarriesTheMessageText(t *testing.T) {
	for _, c := range []struct{ harness, id, file string }{
		{"claude-code", "11111111-2222-4333-8444-555555555555", "git_signals_claude.jsonl"},
		{"codex", "019f0000-0000-7000-8000-000000000002", "git_signals_codex.jsonl"},
	} {
		for _, e := range scanAll(t, c.harness, c.id, c.file) {
			dump := strings.ToLower(e.MsgTitle + e.MsgFull)
			for _, word := range []string{"parser", "export", "title", "route", "panic"} {
				if strings.Contains(dump, word) {
					t.Errorf("%s: event %d carries text: %q", c.file, e.Seq, dump)
				}
			}
		}
	}
}

// Auf den gespeicherten Zeilen (Bestandslauf) findet die Erkennung dieselben Nachrichten und
// Bereiche wie live.
func TestHistoryScanFindsTheSameSignals(t *testing.T) {
	for _, c := range []struct{ harness, id, file string }{
		{"claude-code", "11111111-2222-4333-8444-555555555555", "git_signals_claude.jsonl"},
		{"codex", "019f0000-0000-7000-8000-000000000002", "git_signals_codex.jsonl"},
	} {
		t.Run(c.harness, func(t *testing.T) {
			live := scanAll(t, c.harness, c.id, c.file)
			got := historyEvents(c.harness, c.id, readLines(t, c.file))
			if !reflect.DeepEqual(got, live) {
				for i := range got {
					t.Logf("history %d: %+v", i, got[i])
				}
				for i := range live {
					t.Logf("live    %d: %+v", i, live[i])
				}
				t.Fatalf("history found %d events, the live scan %d", len(got), len(live))
			}
		})
	}
}

// Ein Skript, das zweimal committet, lässt sich keiner Nachricht zuordnen.
func TestGitScanDropsTheMessageOfAScriptWithTwoCommits(t *testing.T) {
	g := newGitScan("claude-code", "s")
	call := claudeCall("toolu_A", `git commit -q -m "one" && git commit -q --allow-empty -m "two"`, "2026-11-09T10:00:00.000Z", "/tmp/nowhere")
	g.line(0, call)
	g.line(1, claudeResult("toolu_A", "2026-11-09T10:00:01.000Z", "/tmp/nowhere"))
	if len(g.queue) != 1 || g.queue[0].MsgTitle != "" || g.queue[0].MsgFull != "" {
		t.Fatalf("%+v", g.queue)
	}
}
