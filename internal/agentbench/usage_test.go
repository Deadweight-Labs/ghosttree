package agentbench

import (
	"os"
	"path/filepath"
	"testing"
)

// writeRawTranscript legt einen vollstaendigen Ereignisstrom ab. Anders als
// writeTranscript in regrade_test.go, das nur die Ergebniszeile schreibt: hier
// geht es um die Werkzeugaufrufe davor.
func writeRawTranscript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const readsTheTree = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/work/repo/.ghosttree/knowledge/k1.md"}}]}}
{"type":"result","subtype":"success","num_turns":2}
`

const readsOnlyCode = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"grep -rn invite /work/repo/internal"}}]}}
{"type":"result","subtype":"success","num_turns":2}
`

// Ein Arm, der ueber sein Material nur redet, hat es nicht benutzt. Der
// Unterschied entscheidet, ob ein Effekt einen Mechanismus hat.
const mentionsTheTreeInProse = `{"type":"assistant","message":{"content":[{"type":"text","text":"Vermutlich stuende das in .ghosttree/knowledge, aber ich suche im Code."}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"grep -rn REQ- /work/repo"}}]}}
{"type":"result","subtype":"success","num_turns":3}
`

func TestMemoryUsageCountsOnlyToolCallsThatTouchTheMaterial(t *testing.T) {
	dir := t.TempDir()
	records := []RunRecord{
		{Arm: ArmGhosttree, Category: CategoryLedgerState,
			Transcript: Transcript{RawPath: writeRawTranscript(t, dir, "a.jsonl", readsTheTree)}},
		{Arm: ArmGhosttree, Category: CategoryLedgerState,
			Transcript: Transcript{RawPath: writeRawTranscript(t, dir, "b.jsonl", readsOnlyCode)}},
		{Arm: ArmGhosttree, Category: CategoryLocalization,
			Transcript: Transcript{RawPath: writeRawTranscript(t, dir, "c.jsonl", readsOnlyCode)}},
	}

	usage := SummariseMemoryUsage(records)

	if len(usage) != 2 {
		t.Fatalf("eine Zeile je Arm und Kategorie: %+v", usage)
	}
	byCategory := map[string]MemoryUsage{}
	for _, u := range usage {
		byCategory[u.Category] = u
	}
	if got := byCategory[string(CategoryLedgerState)]; got.Touched != 1 || got.Runs != 2 {
		t.Fatalf("ledger_state: want 1/2, got %d/%d", got.Touched, got.Runs)
	}
	if got := byCategory[string(CategoryLocalization)]; got.Touched != 0 || got.Runs != 1 {
		t.Fatalf("localization: want 0/1, got %d/%d", got.Touched, got.Runs)
	}
}

func TestMemoryUsageIgnoresProseAboutTheMaterial(t *testing.T) {
	dir := t.TempDir()
	records := []RunRecord{{Arm: ArmGhosttree, Category: CategoryLedgerState,
		Transcript: Transcript{RawPath: writeRawTranscript(t, dir, "a.jsonl", mentionsTheTreeInProse)}}}

	usage := SummariseMemoryUsage(records)

	if len(usage) != 1 || usage[0].Touched != 0 {
		t.Fatalf("ueber den Baum reden ist kein Zugriff: %+v", usage)
	}
}

// Der Beinahe-Fehler, aus dem diese Datei entstanden ist: Auf Robcord-Zentrale
// brauchte ghosttree 5,4 Werkzeugaufrufe weniger als bare, mit einem Intervall,
// das die Null ausschliesst — und oeffnete den Baum in keinem einzigen Lauf.
func TestUsageWithoutMechanismNamesAnArmThatNeverOpenedItsMemory(t *testing.T) {
	idle := UsageWithoutMechanism([]MemoryUsage{
		{Arm: ArmGhosttree, Category: "localization", Runs: 8, Touched: 0},
		{Arm: ArmGhosttree, Category: "cross_cutting", Runs: 4, Touched: 0},
	})
	if len(idle) != 1 || idle[0] != ArmGhosttree {
		t.Fatalf("ein Arm ohne einen einzigen Zugriff muss gemeldet werden: %+v", idle)
	}

	quiet := UsageWithoutMechanism([]MemoryUsage{
		{Arm: ArmGhosttree, Category: "ledger_state", Runs: 8, Touched: 8},
		{Arm: ArmGhosttree, Category: "localization", Runs: 4, Touched: 0},
	})
	if len(quiet) != 0 {
		t.Fatalf("ein Arm, der sein Material dort nutzt wo es etwas hergibt, ist kein Befund: %+v", quiet)
	}
}

// Beim ersten Entwurf stand claudemd in memoryMarkers und die Tabelle meldete
// "0 von 34 Laeufen" — ein selbstbewusster Befund darueber, dass der Arm sein
// Material ignoriert habe, waehrend es ihm die ganze Zeit im Kontext stand.
// Claude Code laedt CLAUDE.md und die Auto-Memory selbst; ein ausbleibender
// Werkzeugaufruf sagt dort nichts.
func TestMemoryUsageIgnoresArmsWhoseMaterialIsLoadedForThem(t *testing.T) {
	dir := t.TempDir()
	records := []RunRecord{
		{Arm: ArmClaudeMD, Category: CategoryLedgerState,
			Transcript: Transcript{RawPath: writeRawTranscript(t, dir, "a.jsonl", readsOnlyCode)}},
		{Arm: ArmClaudeNative, Category: CategoryLedgerState,
			Transcript: Transcript{RawPath: writeRawTranscript(t, dir, "b.jsonl", readsOnlyCode)}},
		{Arm: ArmBare, Category: CategoryLedgerState,
			Transcript: Transcript{RawPath: writeRawTranscript(t, dir, "c.jsonl", readsOnlyCode)}},
	}

	if usage := SummariseMemoryUsage(records); len(usage) != 0 {
		t.Fatalf("nur Arme mit aktiv zu oeffnendem Material gehoeren in die Tabelle: %+v", usage)
	}
}

func TestMemoryUsageSkipsRunsWhoseTranscriptIsGone(t *testing.T) {
	records := []RunRecord{{Arm: ArmGhosttree, Category: CategoryLedgerState,
		Transcript: Transcript{RawPath: "/nonexistent/never-written.jsonl"}}}

	if usage := SummariseMemoryUsage(records); len(usage) != 0 {
		t.Fatalf("ein unlesbarer Lauf ist kein Beleg dafuer, dass der Arm sein Gedaechtnis ignoriert hat: %+v", usage)
	}
}
