package collector

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// sameIDUp gibt einer Kennung immer dieselbe Nummer, wie der Server.
type sameIDUp struct {
	ids      map[string]int64
	sessions []store.Session
}

func (f *sameIDUp) UpsertSession(s store.Session) (int64, error) {
	if f.ids == nil {
		f.ids = map[string]int64{}
	}
	f.sessions = append(f.sessions, s)
	if id, ok := f.ids[s.ExternalID]; ok {
		return id, nil
	}
	f.ids[s.ExternalID] = int64(len(f.ids) + 1)
	return f.ids[s.ExternalID], nil
}

func (f *sameIDUp) AppendChunks(int64, []store.Chunk) error { return nil }

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

// Ein Harness wie T3 schreibt zuerst Zeilen ohne Arbeitsverzeichnis. Wird die
// Datei in diesem Moment registriert, fehlt der Session das Projekt. Sobald das
// Arbeitsverzeichnis im Transkript steht, registriert der Collector sie einmal
// neu, danach nicht mehr.
func TestSessionRegisteredBeforeItsWorkingDirectoryGetsItLater(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "late.jsonl")
	appendLine(t, fp, `{"type":"custom-title","customTitle":"Arbeite in NurView","sessionId":"late"}`)
	appendLine(t, fp, `{"type":"queue-operation","operation":"enqueue","sessionId":"late"}`)
	st := newTestState(dir)
	up := &sameIDUp{}
	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
		t.Fatal(err)
	}
	if len(up.sessions) != 1 || up.sessions[0].CWD != "" {
		t.Fatalf("first registration: %+v", up.sessions)
	}
	if v := st.file(fp).MetadataVersion; v != metadataPartial {
		t.Fatalf("metadata version after a registration without cwd = %d", v)
	}

	// Noch kein Arbeitsverzeichnis: keine weitere Registrierung.
	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
		t.Fatal(err)
	}
	if len(up.sessions) != 1 {
		t.Fatalf("registered again without anything new: %d", len(up.sessions))
	}

	appendLine(t, fp, `{"type":"user","cwd":"`+dir+`","gitBranch":"main","sessionId":"late","message":{"role":"user","content":"los"}}`)
	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
		t.Fatal(err)
	}
	if len(up.sessions) != 2 || up.sessions[1].CWD != dir {
		t.Fatalf("no second registration with the working directory: %+v", up.sessions)
	}
	if v := st.file(fp).MetadataVersion; v != metadataComplete {
		t.Fatalf("metadata version after the late registration = %d", v)
	}
	if st.file(fp).SessionID != 1 {
		t.Fatalf("session number changed: %d", st.file(fp).SessionID)
	}

	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
		t.Fatal(err)
	}
	if len(up.sessions) != 2 {
		t.Fatalf("registered a third time: %d", len(up.sessions))
	}
}

// Eine ruhende Datei aus der Zeit vor dieser Unterscheidung (Version 1) wird
// nicht neu registriert: das würde last_seen_at setzen und sie als aktiv zeigen.
func TestOldPartialSessionIsNotRegisteredAgain(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "old.jsonl")
	appendLine(t, fp, `{"type":"user","cwd":"`+dir+`","sessionId":"old","message":{"role":"user","content":"x"}}`)
	old := time.Now().Add(-2 * lateMetadataWindow)
	if err := os.Chtimes(fp, old, old); err != nil {
		t.Fatal(err)
	}
	st := newTestState(dir)
	fs := st.file(fp)
	fs.SessionID, fs.MetadataVersion = 7, metadataPartial
	if fi, err := os.Stat(fp); err == nil {
		fs.Offset = fi.Size()
	}
	up := &sameIDUp{}
	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
		t.Fatal(err)
	}
	if len(up.sessions) != 0 {
		t.Fatalf("a resting session was registered again: %+v", up.sessions)
	}
}
