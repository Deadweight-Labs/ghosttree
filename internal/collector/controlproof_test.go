package collector

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A line as Claude Code 2.1.287 writes it when a PreToolUse hook answers with
// continue:false (measured 2026-10-02, evidence pause-hook-probe exp4); only the
// stopReason text is ghosttree's.
const stoppedLine = `{"parentUuid":"a97c","isSidechain":false,"attachment":{"type":"hook_stopped_continuation","message":"Paused by ghosttree (control #42)","hookName":"PreToolUse:Bash","toolUseID":"toolu_01XCjD7epYbrcvLBLsBRfQPf","hookEvent":"PreToolUse"},"type":"attachment","uuid":"46bb","timestamp":"2026-10-01T23:23:27.013Z","sessionId":"cd90","version":"2.1.287"}`

func TestControlProofFromDetectsHookStoppedContinuation(t *testing.T) {
	p, ok := ControlProofFrom([]byte(stoppedLine))
	if !ok || p.ControlID != 42 || p.ToolUseID != "toolu_01XCjD7epYbrcvLBLsBRfQPf" || p.HookName != "PreToolUse:Bash" {
		t.Fatalf("proof = %+v %v", p, ok)
	}
	for name, line := range map[string]string{
		"someone else's stop":     `{"type":"attachment","attachment":{"type":"hook_stopped_continuation","message":"paused by ghosttree","hookName":"PreToolUse:Bash","toolUseID":"t"}}`,
		"hook_success only":       `{"type":"attachment","attachment":{"type":"hook_success","stdout":"{\"continue\":false,\"stopReason\":\"Paused by ghosttree (control #42)\"}","toolUseID":"t"}}`,
		"wrong attachment type":   `{"type":"attachment","attachment":{"type":"hook_cancelled","message":"Paused by ghosttree (control #42)","toolUseID":"t"}}`,
		"user text quoting it":    `{"type":"user","message":{"role":"user","content":"hook_stopped_continuation Paused by ghosttree (control #42)"}}`,
		"missing tool use id":     `{"type":"attachment","attachment":{"type":"hook_stopped_continuation","message":"Paused by ghosttree (control #42)"}}`,
		"not json":                `Paused by ghosttree (control #42)`,
		"control id not a number": `{"type":"attachment","attachment":{"type":"hook_stopped_continuation","message":"Paused by ghosttree (control #x)","toolUseID":"t"}}`,
	} {
		if p, ok := ControlProofFrom([]byte(line)); ok {
			t.Errorf("%s must not count as proof: %+v", name, p)
		}
	}
}

type proofUp struct {
	fakeUp
	proofs []store.ControlEvent
	ids    []int64
	err    error
}

func (u *proofUp) RecordControlProof(controlID int64, ev store.ControlEvent) error {
	if u.err != nil {
		return u.err
	}
	u.ids = append(u.ids, controlID)
	u.proofs = append(u.proofs, ev)
	return nil
}

func TestSyncFileReportsProofBeforeTheOffsetAdvances(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "cd90.jsonl")
	user := `{"type":"user","cwd":"/tmp","gitBranch":"","sessionId":"cd90","message":{"role":"user","content":"go"}}`
	if err := os.WriteFile(fp, []byte(user+"\n"+stoppedLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := newTestState(dir)
	up := &proofUp{err: errors.New("server down")}
	if err := SyncFile(fp, "claude-code", up, st, "m"); err == nil {
		t.Fatal("a failed proof report must fail the sync so it is replayed")
	}
	if st.Files[fp].Offset != 0 {
		t.Fatalf("offset advanced to %d although the proof was not reported", st.Files[fp].Offset)
	}
	up.err = nil
	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
		t.Fatal(err)
	}
	if len(up.proofs) != 1 || up.ids[0] != 42 || up.proofs[0].Kind != store.ControlEventProof ||
		up.proofs[0].ToolUseID != "toolu_01XCjD7epYbrcvLBLsBRfQPf" || up.proofs[0].SessionID != "cd90" {
		t.Fatalf("proofs = %+v %v", up.proofs, up.ids)
	}
	// Replay-sicher: ein dritter Lauf meldet nichts mehr.
	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil || len(up.proofs) != 1 {
		t.Fatalf("third run: %v %d", err, len(up.proofs))
	}
}

func TestSyncFileWithoutAProofRecorderStillArchives(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "cd90.jsonl")
	_ = os.WriteFile(fp, []byte(stoppedLine+"\n"), 0o644)
	up := &fakeUp{}
	if err := SyncFile(fp, "claude-code", up, newTestState(dir), "m"); err != nil {
		t.Fatal(err)
	}
	if len(up.chunks[1]) != 1 {
		t.Fatalf("chunks = %+v", up.chunks)
	}
}
