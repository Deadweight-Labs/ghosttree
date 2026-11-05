package collector

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveSoonBundlesWritesAndSaveIfDirtyCatchesUp(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	backoffNow = func() time.Time { return now }
	t.Cleanup(func() { backoffNow = time.Now })
	st := newTestState(t.TempDir())
	st.file("a.jsonl").Offset = 1
	if err := st.SaveSoon(); err != nil {
		t.Fatal(err)
	}
	st.file("a.jsonl").Offset = 2
	if err := st.SaveSoon(); err != nil {
		t.Fatal(err)
	}
	if got := loadOffset(t, st.path, "a.jsonl"); got != 1 {
		t.Fatalf("second SaveSoon within %s wrote the file: offset %d", saveEvery, got)
	}
	if err := st.SaveIfDirty(); err != nil {
		t.Fatal(err)
	}
	if got := loadOffset(t, st.path, "a.jsonl"); got != 2 {
		t.Fatalf("SaveIfDirty did not write the bundled state: offset %d", got)
	}
}

func TestSaveSkipsUnchangedState(t *testing.T) {
	st := newTestState(t.TempDir())
	st.file("a.jsonl").Offset = 1
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(st.path); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.path); !os.IsNotExist(err) {
		t.Fatalf("unchanged state was written again: %v", err)
	}
}

func TestSyncFileWithoutNewBytesReadsNoIdentity(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(p, []byte(`{"type":"user","message":{"role":"user","content":"hi"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := newTestState(dir)
	up := &fakeUp{}
	if err := SyncFile(p, "claude-code", up, st, "m"); err != nil {
		t.Fatal(err)
	}
	delete(st.idents, p)
	if err := SyncFile(p, "claude-code", up, st, "m"); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.idents[p]; ok {
		t.Fatal("a sweep over an unchanged transcript resolved its identity (header read, git)")
	}
}

func loadOffset(t *testing.T, path, file string) int64 {
	t.Helper()
	st, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.file(file).Offset
}
